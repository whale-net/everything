package remote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/whale-net/everything/libs/go/monty"
	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// wsServer is a monty-server stand-in: it records every binary message it is
// sent and answers each one with whatever the test scripted.
type wsServer struct {
	*httptest.Server

	mu        sync.Mutex
	handshake http.Header
	messages  [][]byte
	types     []int

	// answer returns the reply for a request, or nil to stop the loop.
	answer func(req *pb.ParentRequest) *pb.ChildEvent
	// reject, when set, is written instead of a WebSocket upgrade.
	reject int
}

func newWSServer(t *testing.T, answer func(req *pb.ParentRequest) *pb.ChildEvent) *wsServer {
	t.Helper()
	s := &wsServer{answer: answer}
	upgrader := websocket.Upgrader{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.handshake = r.Header.Clone()
		reject := s.reject
		s.mu.Unlock()

		if reject != 0 {
			w.WriteHeader(reject)
			return
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()

		for {
			typ, body, err := ws.ReadMessage()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.messages = append(s.messages, body)
			s.types = append(s.types, typ)
			s.mu.Unlock()

			var req pb.ParentRequest
			if err := proto.Unmarshal(body, &req); err != nil {
				return
			}
			ev := s.answer(&req)
			if ev == nil {
				return
			}
			out, err := proto.Marshal(ev)
			if err != nil {
				return
			}
			if err := ws.WriteMessage(websocket.BinaryMessage, out); err != nil {
				return
			}
		}
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *wsServer) received() ([][]byte, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.messages...), append([]int(nil), s.types...)
}

func (s *wsServer) handshakeHeader() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handshake
}

// wsURL is the server's address with the scheme a WebSocket dialer wants.
func (s *wsServer) wsURL() string {
	return "ws" + strings.TrimPrefix(s.Server.URL, "http")
}

// configureAndComplete answers Configure with Ok and anything else with a
// Complete carrying value, which is the smallest conversation a full round trip
// needs.
func configureAndComplete(value int64) func(*pb.ParentRequest) *pb.ChildEvent {
	arena, roots, err := monty.EncodeArena([]any{value})
	if err != nil {
		panic(err)
	}
	complete := &pb.ChildEvent{Kind: &pb.ChildEvent_Complete{Complete: &pb.Complete{
		Value: roots[0], Values: arena,
	}}}
	return func(req *pb.ParentRequest) *pb.ChildEvent {
		if req.GetConfigure() != nil {
			return &pb.ChildEvent{Kind: &pb.ChildEvent_Ok{Ok: &pb.Ok{}}}
		}
		return complete
	}
}

// TestRoundTrip: a whole Pool -> Checkout -> Run against a real WebSocket
// server, which is the only way to cover both the framing and the shared
// session state machine at once.
func TestRoundTrip(t *testing.T) {
	var fedCode string
	var answer func(*pb.ParentRequest) *pb.ChildEvent
	answer = configureAndComplete(int64(7))
	inner := answer
	answer = func(req *pb.ParentRequest) *pb.ChildEvent {
		if req.GetFeed() != nil {
			fedCode = req.GetFeed().GetCode()
		}
		return inner(req)
	}

	ts := newWSServer(t, answer)

	pool, err := monty.NewPool(monty.Config{
		NewConn:    Dialer(ts.wsURL()),
		ScriptName: "remote_test.py",
	})
	require.NoError(t, err)
	defer pool.Close()

	s, err := pool.Checkout(context.Background())
	require.NoError(t, err)
	defer s.Close()

	res, err := s.Run(context.Background(), "1 + 6", monty.RunOptions{})
	require.NoError(t, err)
	assert.Equal(t, int64(7), res.Value)
	assert.Equal(t, "1 + 6", fedCode, "the code reached the server intact")
}

// TestFramingIsBareProtobuf: the WebSocket transport's only difference from the
// subprocess one is framing -- one bare protobuf per binary message, with no
// 4-byte length prefix, because the WebSocket frame already delimits it.
func TestFramingIsBareProtobuf(t *testing.T) {
	ts := newWSServer(t, configureAndComplete(int64(1)))

	limits := monty.Limits{MaxMemoryBytes: 4096, MaxSuspensions: 5}
	pool, err := monty.NewPool(monty.Config{
		NewConn:    Dialer(ts.wsURL()),
		ScriptName: "remote_test.py",
		Limits:     limits,
	})
	require.NoError(t, err)
	defer pool.Close()

	s, err := pool.Checkout(context.Background())
	require.NoError(t, err)
	defer s.Close()

	msgs, types := ts.received()
	require.Len(t, msgs, 1)
	assert.Equal(t, websocket.BinaryMessage, types[0])

	mem := uint64(limits.MaxMemoryBytes)
	susp := uint64(limits.MaxSuspensions)
	want, err := proto.Marshal(&pb.ParentRequest{Kind: &pb.ParentRequest_Configure{Configure: &pb.Configure{
		ScriptName: "remote_test.py",
		Limits: &pb.ResourceLimits{
			MaxMemoryBytes: &mem,
			MaxSuspensions: &susp,
		},
		ProtocolVersion: monty.ProtocolVersion,
	}}})
	require.NoError(t, err)
	assert.Equal(t, want, msgs[0],
		"the message body is exactly the marshalled ParentRequest, byte for byte")
}

// TestHandshakeHeaderArrives: identity a reverse proxy carries is a handshake
// header, so it has to survive the dial.
func TestHandshakeHeaderArrives(t *testing.T) {
	ts := newWSServer(t, configureAndComplete(int64(1)))

	pool, err := monty.NewPool(monty.Config{
		NewConn: Dialer(ts.wsURL(), WithHeader("X-Monty-Token", "s3cret")),
	})
	require.NoError(t, err)
	defer pool.Close()

	s, err := pool.Checkout(context.Background())
	require.NoError(t, err)
	defer s.Close()

	assert.Equal(t, "s3cret", ts.handshakeHeader().Get("X-Monty-Token"))
}

func TestWithHeaders(t *testing.T) {
	ts := newWSServer(t, configureAndComplete(int64(1)))

	pool, err := monty.NewPool(monty.Config{
		NewConn: Dialer(ts.wsURL(), WithHeaders(http.Header{
			"X-Monty-Client": []string{"a"},
			"X-Monty-Tag":    []string{"b1", "b2"},
		})),
	})
	require.NoError(t, err)
	defer pool.Close()

	s, err := pool.Checkout(context.Background())
	require.NoError(t, err)
	defer s.Close()

	h := ts.handshakeHeader()
	assert.Equal(t, "a", h.Get("X-Monty-Client"))
	assert.Equal(t, []string{"b1", "b2"}, h.Values("X-Monty-Tag"))
}

// TestDialFailureNamesTheStatus: monty-server rejects at the handshake, not in
// the protocol, so the status code is the only useful diagnostic.
func TestDialFailureNamesTheStatus(t *testing.T) {
	ts := newWSServer(t, configureAndComplete(int64(1)))
	ts.mu.Lock()
	ts.reject = http.StatusServiceUnavailable
	ts.mu.Unlock()

	url := ts.wsURL()
	conn, err := Dialer(url)(context.Background(), &monty.Config{})
	require.Error(t, err)
	assert.Nil(t, conn)
	assert.Contains(t, err.Error(), "503")
	assert.Contains(t, err.Error(), url)
}

// TestDialFailureNoResponse: a server that is simply not there has no status to
// report, so the dial error stands on its own.
func TestDialFailureNoResponse(t *testing.T) {
	ts := newWSServer(t, configureAndComplete(int64(1)))
	url := ts.wsURL()
	ts.Close()

	conn, err := Dialer(url)(context.Background(), &monty.Config{})
	require.Error(t, err)
	assert.Nil(t, conn)
	assert.NotContains(t, err.Error(), "HTTP ")
}

// TestServerTextFrameIsProtocolViolation: this protocol is binary only.
func TestServerTextFrameIsProtocolViolation(t *testing.T) {
	ts := &wsServer{}
	ts.answer = func(req *pb.ParentRequest) *pb.ChildEvent { return nil }
	upgrader := websocket.Upgrader{}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		// Drain the Configure, then answer in text.
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
		_ = ws.WriteMessage(websocket.TextMessage, []byte("hello?"))
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	pool, err := monty.NewPool(monty.Config{NewConn: Dialer(wsURL)})
	require.NoError(t, err)
	defer pool.Close()

	s, err := pool.Checkout(context.Background())
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, monty.ErrProtocol)
	assert.Contains(t, err.Error(), "text frame")
}

// TestServerCloseIsACrash: a WebSocket close is a WebSocket-shaped EOF, which
// is the same "worker went away" the subprocess transport reports.
func TestServerCloseIsACrash(t *testing.T) {
	upgrader := websocket.Upgrader{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
		_ = ws.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseAbnormalClosure, ""))
		ws.Close()
	}))
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http")

	pool, err := monty.NewPool(monty.Config{NewConn: Dialer(wsURL)})
	require.NoError(t, err)
	defer pool.Close()

	s, err := pool.Checkout(context.Background())
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, monty.ErrCrashed)
}

// TestCheckoutConfigureLimitFailure: a worker that answers Configure with a
// FatalError is a version mismatch, and the error has to say so.
func TestConfigureFatalErrorIsVersionMismatch(t *testing.T) {
	ts := newWSServer(t, func(*pb.ParentRequest) *pb.ChildEvent {
		return &pb.ChildEvent{Kind: &pb.ChildEvent_FatalError{
			FatalError: &pb.FatalError{Message: "worker speaks protocol version 3, not 5"},
		}}
	})

	pool, err := monty.NewPool(monty.Config{NewConn: Dialer(ts.wsURL())})
	require.NoError(t, err)
	defer pool.Close()

	s, err := pool.Checkout(context.Background())
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, monty.ErrVersion)
}

func TestSendAfterCloseFails(t *testing.T) {
	ts := newWSServer(t, configureAndComplete(int64(1)))

	dial := Dialer(ts.wsURL())
	conn, err := dial(context.Background(), &monty.Config{})
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	err = conn.Send(context.Background(), &pb.ParentRequest{})
	assert.ErrorIs(t, err, monty.ErrClosed)
	assert.False(t, conn.Alive())
	require.NoError(t, conn.Close(), "Close is safe to call twice")
}

func TestSendOnCancelledContext(t *testing.T) {
	ts := newWSServer(t, configureAndComplete(int64(1)))

	dial := Dialer(ts.wsURL())
	conn, err := dial(context.Background(), &monty.Config{})
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, conn.Send(ctx, &pb.ParentRequest{}), context.Canceled)
}
