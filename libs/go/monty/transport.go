package monty

import (
	"context"
	"fmt"
	"io"
	"sync"

	pb "github.com/whale-net/everything/libs/go/monty/protos"
)

// Conn is one live worker connection -- a `monty subprocess` child, or a
// monty-server WebSocket. The two differ only in framing and in whether the
// connection can be reused, so everything above this interface (the session
// state machine, suspension handling, deadline backstops) is identical.
//
// Implementations must be safe for one sender and one receiver at a time.
// Monty has no multiplexing: the parent writes one request and reads until a
// turn-ending event, so a Conn is never asked to interleave two turns.
type Conn interface {
	// Send writes one request. A cancelled context leaves the connection in
	// an unknown state and the caller must discard it.
	Send(ctx context.Context, req *pb.ParentRequest) error

	// Recv returns the next child event, blocking until one arrives, ctx is
	// done, or the connection dies. A connection that ends cleanly between
	// frames yields ErrCrashed: a worker that exits without a FatalError
	// crashed, and only a worker that was shut down cleanly knows to say so.
	Recv(ctx context.Context) (*pb.ChildEvent, error)

	// Close releases the connection. It is safe to call more than once.
	Close() error

	// Alive reports whether the worker is still running. A pool consults it
	// before handing an idle connection to a new session, so a worker that
	// died while idle is replaced instead of failing its first Configure.
	Alive() bool
}

// NewConnFunc opens one connection. Config.NewConn takes one of these, which
// is how the remote subpackage supplies a WebSocket transport without the
// core package depending on a WebSocket library.
type NewConnFunc func(ctx context.Context, cfg *Config) (Conn, error)

// EventStream is the read side of a Conn: a goroutine that turns a blocking
// read into a channel of events. A transport outside this package builds one
// with NewEventStream.
//
// The goroutine is not an optimisation, it is the cancellation contract.
// A read on a pipe or socket cannot be interrupted, so a caller that
// abandons a turn mid-frame would otherwise lose whatever the reader had
// already consumed. Reading on its own goroutine keeps partial-frame state
// there instead, so a cancelled Recv leaves the connection readable.
type EventStream struct {
	events chan readResult
	// next is the blocking read: one frame for stdio, one message for a
	// WebSocket. It is only ever called from the stream's own goroutine.
	next func() (*pb.ChildEvent, error)

	mu       sync.Mutex
	closedCh chan struct{}
}

type readResult struct {
	ev  *pb.ChildEvent
	err error
}

func NewEventStream(next func() (*pb.ChildEvent, error)) *EventStream {
	s := &EventStream{
		// Depth 1: the protocol is strict alternation, so a reader that
		// runs ahead by one event is already as far ahead as it can usefully
		// be. Prints can precede a turn-ender, so a shallow buffer here would
		// stall the worker rather than the reader.
		events:   make(chan readResult, 1),
		next:     next,
		closedCh: make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *EventStream) run() {
	for {
		ev, err := s.next()
		if err != nil {
			// io.EOF at a frame boundary is a worker that vanished: Monty
			// documents that only a FatalError precedes an orderly exit.
			if err == io.EOF {
				err = fmt.Errorf("%w: worker exited without a fatal error", ErrCrashed)
			}
			s.deliver(readResult{err: err})
			return
		}
		if !s.deliver(readResult{ev: ev}) {
			return
		}
	}
}

// deliver hands a result to the reader, reporting false once the stream is
// closed and the goroutine should stop.
func (s *EventStream) deliver(r readResult) bool {
	select {
	case s.events <- r:
		return true
	case <-s.closedCh:
		return false
	}
}

func (s *EventStream) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closedCh:
	default:
		close(s.closedCh)
	}
}

// recv returns the next event, or ctx.Err() if the caller gives up first.
func (s *EventStream) Recv(ctx context.Context) (*pb.ChildEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-s.events:
		return r.ev, r.err
	}
}
