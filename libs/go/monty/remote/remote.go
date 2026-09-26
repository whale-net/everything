// Package remote runs Monty snippets against a monty-server ("Full Monty")
// over a WebSocket, instead of a local `monty subprocess` child.
//
// The wire protocol is the same one the local transport speaks -- the schema
// is public and language-neutral -- and the only difference is framing: a
// WebSocket carries one protobuf body per binary message, with no length
// prefix. Code written against the core package runs unchanged either way.
//
//	pool, err := monty.NewPool(monty.Config{NewConn: remote.Dialer("wss://monty.internal/")})
//
// The server has no authentication of its own and speaks plain ws:// by
// default, so terminate TLS and authenticate at an ingress. Whatever that
// ingress needs -- a bearer token, a client certificate, an mTLS header --
// goes in as a handshake header:
//
//	remote.Dialer(url, remote.WithHeader("Authorization", "Bearer "+token))
package remote

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/whale-net/everything/libs/go/monty"
	pb "github.com/whale-net/everything/libs/go/monty/protos"
	"google.golang.org/protobuf/proto"
)

// defaultDialTimeout bounds the handshake when the caller's context has no
// deadline of its own.
const defaultDialTimeout = 30 * time.Second

// maxMessageSize matches the core package's frame cap, so a message the
// protocol itself would accept is never refused by the WebSocket layer.
const maxMessageSize = 256 << 20

// Option configures a Dialer.
type Option func(*options)

type options struct {
	header http.Header
	dialer *websocket.Dialer
}

// WithHeader adds a header to the WebSocket handshake. The server identifies
// a caller by peer IP, so a client behind a shared NAT shares a quota unless
// the server was started with --trust-forwarded-for; a header is how a
// reverse proxy in front of it carries identity.
func WithHeader(name, value string) Option {
	return func(o *options) { o.header.Set(name, value) }
}

// WithHeaders adds every entry of h to the handshake.
func WithHeaders(h http.Header) Option {
	return func(o *options) {
		for k, vs := range h {
			for _, v := range vs {
				o.header.Add(k, v)
			}
		}
	}
}

// WithDialer replaces the underlying WebSocket dialer, for a custom TLS
// config or a proxy.
func WithDialer(d *websocket.Dialer) Option {
	return func(o *options) { o.dialer = d }
}

// Dialer returns a monty.NewConnFunc that opens one session per call
// against url. It is meant to be handed to monty.Config.NewConn:
//
//	monty.NewPool(monty.Config{NewConn: remote.Dialer("wss://monty.internal/")})
//
// Every connection is single-use: a monty-server hands out one worker per
// connection, resets it between sessions, and never hands the same one to
// two callers, so the pool will not try to reuse one.
func Dialer(url string, opts ...Option) monty.NewConnFunc {
	o := &options{header: http.Header{}}
	for _, opt := range opts {
		opt(o)
	}
	return func(ctx context.Context, _ *monty.Config) (monty.Conn, error) {
		dialer := o.dialer
		if dialer == nil {
			dialer = &websocket.Dialer{Proxy: http.ProxyFromEnvironment}
		}
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultDialTimeout)
			defer cancel()
		}
		conn, resp, err := dialer.DialContext(ctx, url, o.header)
		if err != nil {
			if resp != nil {
				// The server rejects at the handshake, not in the protocol:
				// 503 when it is at capacity, 429 when the caller is over
				// its per-client quota. Both are worth naming.
				return nil, fmt.Errorf("monty: dial %s: %w (HTTP %d)", url, err, resp.StatusCode)
			}
			return nil, fmt.Errorf("monty: dial %s: %w", url, err)
		}
		// Monty frames can be far larger than a WebSocket library expects.
		conn.SetReadLimit(maxMessageSize)
		return newConn(conn), nil
	}
}

// wsConn is a monty.Conn over a WebSocket.
type wsConn struct {
	ws     *websocket.Conn
	stream *monty.EventStream

	writeMu sync.Mutex
	closed  bool
}

func newConn(ws *websocket.Conn) monty.Conn {
	c := &wsConn{ws: ws}
	c.stream = monty.NewEventStream(func() (*pb.ChildEvent, error) {
		for {
			typ, body, err := ws.ReadMessage()
			if err != nil {
				// A WebSocket close is a WebSocket-shaped EOF: the same
				// "worker went away mid-session" the core package reports
				// for a subprocess that exits without a fatal error.
				return nil, fmt.Errorf("%w: %v", monty.ErrCrashed, err)
			}
			switch typ {
			case websocket.BinaryMessage:
				var ev pb.ChildEvent
				if err := proto.Unmarshal(body, &ev); err != nil {
					return nil, fmt.Errorf("%w: undecodable event: %v", monty.ErrProtocol, err)
				}
				if ev.Kind == nil {
					return nil, fmt.Errorf("%w: event carried no kind", monty.ErrProtocol)
				}
				return &ev, nil
			case websocket.TextMessage:
				// The pool only ever sends binary frames, and a server that
				// answers in text is not speaking this protocol.
				return nil, fmt.Errorf("%w: server sent a text frame", monty.ErrProtocol)
			default:
				// Ping and Pong are answered by the reader itself, which is
				// what keeps a keepalive from dropping an idle session.
				continue
			}
		}
	})
	return c
}

func (c *wsConn) Send(ctx context.Context, req *pb.ParentRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("monty: marshal request: %w", err)
	}
	if len(body) > maxMessageSize {
		return fmt.Errorf("monty: request is %d bytes, over the %d byte cap", len(body), maxMessageSize)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return monty.ErrClosed
	}
	// A cancelled context cannot abort a write that is already under way,
	// and the protocol has no way to resynchronise a half-sent frame, so the
	// write is allowed to finish.
	if err := c.ws.WriteMessage(websocket.BinaryMessage, body); err != nil {
		return fmt.Errorf("monty: send request: %w", err)
	}
	return nil
}

func (c *wsConn) Recv(ctx context.Context) (*pb.ChildEvent, error) {
	return c.stream.Recv(ctx)
}

// Alive reports whether the connection is still open. A remote worker is
// always single-use, so this only guards against handing a dead connection
// to a session's Configure.
func (c *wsConn) Alive() bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return !c.closed
}

func (c *wsConn) Close() error {
	c.writeMu.Lock()
	if c.closed {
		c.writeMu.Unlock()
		return nil
	}
	c.closed = true
	c.writeMu.Unlock()

	c.stream.Close()
	// Best effort: a server that has already gone away has nothing to hear a
	// close frame, and the deadline keeps Close from hanging on a half-open
	// TCP connection.
	_ = c.ws.SetWriteDeadline(time.Now().Add(time.Second))
	_ = c.ws.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	return c.ws.Close()
}
