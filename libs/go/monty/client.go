// Package monty runs untrusted Python in the Monty sandbox.
//
// Monty executes Python in a worker process that is isolated from the host: a
// stack overflow or an allocator abort inside the sandbox takes the worker
// down and nothing else. This package speaks Monty's parent/worker protocol so
// a Go program can feed it snippets and get values, printed output and Python
// exceptions back, with host functions and filesystem calls serviced by the
// Go side.
//
// Two transports, one API. A local worker is a `monty subprocess` child,
// pooled and reused; a remote worker is a monty-server WebSocket, single-use.
// They differ only in framing, so everything above [Conn] is shared:
//
//	pool, err := monty.NewPool(monty.Config{Binary: "/usr/local/bin/monty"})
//	defer pool.Close()
//	session, err := pool.Checkout(ctx)
//	defer session.Close()
//	res, err := session.Run(ctx, "sum(range(10))", monty.RunOptions{})
//
// For a remote server, supply a NewConnFunc -- see the remote subpackage,
// which provides one over a WebSocket.
//
// A [Session] is a REPL: successive Runs share globals, and a Python
// exception does not end the session. A Session is not safe for concurrent
// use, because the protocol has no way to express two turns in flight.
package monty

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// ProtocolVersion is the wire schema version this client speaks. Monty does
// not negotiate: a worker that serves a different range answers Configure
// with a FatalError naming it. Peers interoperate as long as their versions
// match, so this tracks Monty's monty-proto, not the monty package version.
const ProtocolVersion = 5

// resetTimeout bounds the Reset handshake a session performs on its way back
// to the pool. A worker that does not answer is discarded rather than waited
// on -- the alternative is a Close that hangs on a wedged process.
const resetTimeout = 5 * time.Second

// defaultSuspensionLimit bounds a Run that loops through host calls forever.
// Monty enforces its own default, but the host's limit is the one that
// protects a host function with side effects.
const defaultSuspensionLimit = 1000

// Limits are the sandbox resource limits a session is created with. A zero
// field leaves that limit to the worker, which is usually not what a host
// running untrusted code wants -- set MaxMemoryBytes and MaxFeedDuration
// explicitly.
type Limits struct {
	// MaxMemoryBytes caps the sandbox's heap. Strongly recommended.
	MaxMemoryBytes uint64
	// MaxFeedDuration caps sandbox execution time for one Run.
	MaxFeedDuration time.Duration
	// MaxTurnDuration caps one request/response turn, suspensions included.
	MaxTurnDuration time.Duration
	// MaxRecursionDepth caps the sandbox's call stack.
	MaxRecursionDepth uint64
	// MaxSuspensions caps host calls serviced during one Run. Zero uses
	// defaultSuspensionLimit.
	MaxSuspensions int
	// MaxTotalSleep caps the cumulative time this host spends servicing the
	// sandbox's sleep calls.
	MaxTotalSleep time.Duration
}

func (l Limits) wire() *pb.ResourceLimits {
	out := &pb.ResourceLimits{}
	if l.MaxMemoryBytes > 0 {
		out.MaxMemoryBytes = u64(l.MaxMemoryBytes)
	}
	if l.MaxRecursionDepth > 0 {
		out.MaxRecursionDepth = u64(l.MaxRecursionDepth)
	}
	if l.MaxSuspensions > 0 {
		out.MaxSuspensions = u64(uint64(l.MaxSuspensions))
	}
	if l.MaxFeedDuration > 0 {
		out.MaxFeedDurationMicros = u64(uint64(l.MaxFeedDuration.Microseconds()))
	}
	if l.MaxTurnDuration > 0 {
		out.MaxTurnDurationMicros = u64(uint64(l.MaxTurnDuration.Microseconds()))
	}
	if l.MaxTotalSleep > 0 {
		out.MaxTotalSleepMicros = u64(uint64(l.MaxTotalSleep.Microseconds()))
	}
	return out
}

// Config describes a pool of workers.
type Config struct {
	// NewConn opens one worker connection. Leave it nil to use the built-in
	// subprocess transport, which needs Binary. The remote subpackage
	// supplies a NewConnFunc for a monty-server WebSocket.
	NewConn NewConnFunc

	// Binary is the `monty` executable for the built-in subprocess
	// transport. When empty it is resolved from $MONTY_BIN and then $PATH.
	// Pass it explicitly when running untrusted code: a binary found on
	// $PATH is a binary the host chose, not one it vetted.
	Binary string

	// ScriptName appears in tracebacks from this pool's sessions.
	ScriptName string

	// Limits are applied to every session the pool creates.
	Limits Limits

	// TypeCheck runs a type checker over each snippet before executing it.
	// A rejected snippet raises a TypingError and never runs.
	TypeCheck bool

	// PrintFlushInterval bounds how long the worker may buffer print()
	// output before streaming it. Zero uses the worker's default; a
	// negative value asks for line buffering, one event per completed line.
	PrintFlushInterval time.Duration

	// MaxWorkers caps concurrent sessions. Zero means 1.
	MaxWorkers int

	// CheckoutTimeout bounds acquiring a session, on top of the caller's
	// context. Zero means no bound beyond the context.
	CheckoutTimeout time.Duration
}

// Pool is a set of workers shared across sessions. It is safe for concurrent
// use.
type Pool struct {
	cfg      Config
	newConn  NewConnFunc
	reusable bool

	// slots bounds concurrent checkouts. A worker is only created once a
	// slot is held, so a cancelled checkout cannot leave the pool oversized.
	slots chan struct{}

	mu     sync.Mutex
	idle   []Conn
	closed bool
}

// NewPool validates cfg and returns a pool. It opens no workers: the first
// Checkout does, so a caller that never checks out pays nothing.
func NewPool(cfg Config) (*Pool, error) {
	newConn := cfg.NewConn
	reusable := true
	if newConn == nil {
		binary, err := resolveBinary(cfg.Binary)
		if err != nil {
			return nil, err
		}
		cfg.Binary = binary
		newConn = func(ctx context.Context, c *Config) (Conn, error) {
			return newSubprocessConn(ctx, c.Binary)
		}
	} else {
		// A caller-supplied transport is opaque: it may be single-use, the
		// way a monty-server worker is, and handing a spent connection to a
		// later session would fail its Configure.
		reusable = false
	}

	max := cfg.MaxWorkers
	if max <= 0 {
		max = 1
	}
	p := &Pool{
		cfg:      cfg,
		newConn:  newConn,
		reusable: reusable,
		slots:    make(chan struct{}, max),
	}
	return p, nil
}

// resolveBinary finds the worker executable, preferring an explicit path over
// $MONTY_BIN over $PATH.
func resolveBinary(explicit string) (string, error) {
	if explicit != "" {
		if _, err := exec.LookPath(explicit); err != nil {
			return "", fmt.Errorf("monty: worker binary %q: %w", explicit, err)
		}
		return explicit, nil
	}
	if env := os.Getenv("MONTY_BIN"); env != "" {
		if _, err := exec.LookPath(env); err != nil {
			return "", fmt.Errorf("monty: $MONTY_BIN=%q: %w", env, err)
		}
		return env, nil
	}
	path, err := exec.LookPath("monty")
	if err != nil {
		return "", fmt.Errorf("monty: no worker binary: set Config.Binary or $MONTY_BIN: %w", err)
	}
	return path, nil
}

// Checkout acquires a session, waiting for a free worker if the pool is at
// capacity. Closing the session returns the worker to the pool, or discards
// it if the transport is single-use.
func (p *Pool) Checkout(ctx context.Context) (*Session, error) {
	if p.cfg.CheckoutTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.CheckoutTimeout)
		defer cancel()
	}

	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}

	select {
	case p.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("monty: waiting for a worker: %w", ctx.Err())
	}

	conn, err := p.acquire(ctx)
	if err != nil {
		<-p.slots
		return nil, err
	}

	session, err := newSession(ctx, conn, p.cfg, p.reusable)
	if err != nil {
		_ = conn.Close()
		<-p.slots
		return nil, err
	}
	session.pool = p
	return session, nil
}

// acquire takes an idle worker or starts a new one.
func (p *Pool) acquire(ctx context.Context) (Conn, error) {
	for {
		p.mu.Lock()
		for len(p.idle) > 0 {
			conn := p.idle[len(p.idle)-1]
			p.idle = p.idle[:len(p.idle)-1]
			// A worker that died while idle must be replaced, not handed to
			// a session whose first Configure would fail on a dead pipe.
			if conn.Alive() {
				p.mu.Unlock()
				return conn, nil
			}
			p.mu.Unlock()
			_ = conn.Close()
			p.mu.Lock()
		}
		p.mu.Unlock()
		return p.newConn(ctx, &p.cfg)
	}
}

// release ends a session's claim on a worker. A healthy worker goes back to
// the idle queue; anything else is torn down. Either way the session's slot
// comes back, so the semaphore counts busy sessions and idle workers sit
// outside it.
func (p *Pool) release(conn Conn) {
	defer func() { <-p.slots }()

	p.mu.Lock()
	if p.closed || !conn.Alive() {
		p.mu.Unlock()
		_ = conn.Close()
		return
	}
	p.idle = append(p.idle, conn)
	p.mu.Unlock()
}

// discard frees the slot a torn-down worker was holding. The connection has
// already been closed by its session.
func (p *Pool) discard() {
	<-p.slots
}

// Close shuts every idle worker down and refuses further checkouts.
// Sessions already checked out are unaffected.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.mu.Unlock()

	var firstErr error
	for _, conn := range idle {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// newSession configures a fresh worker and wraps it in a Session.
func newSession(ctx context.Context, conn Conn, cfg Config, reusable bool) (*Session, error) {
	limits := cfg.Limits
	if limits.MaxSuspensions <= 0 {
		limits.MaxSuspensions = defaultSuspensionLimit
	}
	s := &Session{conn: conn, limits: limits, reusable: reusable}

	conf := &pb.Configure{
		ScriptName:      cfg.ScriptName,
		Limits:          limits.wire(),
		TypeCheck:       cfg.TypeCheck,
		ProtocolVersion: ProtocolVersion,
	}
	if cfg.PrintFlushInterval > 0 {
		ms := cfg.PrintFlushInterval.Milliseconds()
		if ms < 1 {
			// 0 is not "unset" on the wire -- it is the line-buffering
			// sentinel -- so a sub-millisecond interval rounds up rather
			// than silently becoming one event per line.
			ms = 1
		}
		conf.PrintFlushIntervalMs = u32(uint32(ms))
	} else if cfg.PrintFlushInterval < 0 {
		// Zero is not "unset" on the wire -- it is the line-buffering
		// sentinel -- so a negative interval has to travel as an explicit 0.
		conf.PrintFlushIntervalMs = u32(0)
	}

	if err := conn.Send(ctx, &pb.ParentRequest{Kind: &pb.ParentRequest_Configure{Configure: conf}}); err != nil {
		return nil, err
	}
	ev, err := conn.Recv(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case ev.GetOk() != nil:
		return s, nil
	case ev.GetFatalError() != nil:
		return nil, fmt.Errorf("%w: %s", ErrVersion, ev.GetFatalError().GetMessage())
	default:
		return nil, fmt.Errorf("%w: worker answered Configure with %T", ErrProtocol, ev.GetKind())
	}
}

// Close ends the session. A reusable worker is reset and handed back to the
// pool still running; anything else is torn down. Close is safe to call twice.
func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true

	// A session that abandoned a turn left the worker's protocol state
	// unknown, so it is not reusable however healthy the process looks.
	if s.pool != nil && s.reusable && !s.broken && s.conn.Alive() {
		// Reset drops the session's state without paying for a new process.
		// A failure here is not worth reporting: the connection is about to
		// be discarded either way.
		ctx, cancel := context.WithTimeout(context.Background(), resetTimeout)
		defer cancel()
		if err := s.conn.Send(ctx, &pb.ParentRequest{Kind: &pb.ParentRequest_Reset_{Reset_: &pb.Reset{}}}); err == nil {
			if _, err := s.conn.Recv(ctx); err == nil {
				s.pool.release(s.conn)
				return nil
			}
		}
	}

	err := s.conn.Close()
	if s.pool != nil {
		s.pool.discard()
	}
	return err
}

// Closed reports whether the session has been closed or abandoned after a
// failed turn.
func (s *Session) Closed() bool { return s.closed }

// IsPythonError reports whether err is a Python exception raised inside the
// sandbox, as opposed to a transport or protocol failure.
func IsPythonError(err error) bool {
	var pe *PythonError
	return errors.As(err, &pe)
}

func u64(v uint64) *uint64 { return &v }
func u32(v uint32) *uint32 { return &v }
