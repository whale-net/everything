package rmq

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// TestChannel_ZeroValue verifies that Channel() on a zero-value Connection
// (no dial function, nil conn) returns a clear error instead of panicking.
func TestChannel_ZeroValue(t *testing.T) {
	c := &Connection{}
	_, err := c.Channel()
	if err == nil {
		t.Fatal("expected error from Channel() on zero-value Connection, got nil")
	}
}

// TestChannel_DialErrorPropagated verifies that when conn is nil (simulating a
// closed connection) and the dial function returns an error, that error is
// surfaced by Channel().
func TestChannel_DialErrorPropagated(t *testing.T) {
	dialErr := errors.New("broker unreachable")
	c := &Connection{
		conn: nil,
		dial: func() (*amqp.Connection, error) {
			return nil, dialErr
		},
	}

	_, err := c.Channel()
	if err == nil {
		t.Fatal("expected error from Channel() when dial fails, got nil")
	}
	if !errors.Is(err, dialErr) {
		t.Errorf("expected wrapped dialErr in Channel() error, got: %v", err)
	}
}

// TestChannel_DialCalledOnNilConn verifies that when conn is nil, Channel()
// invokes the dial function exactly once before trying to open a channel.
func TestChannel_DialCalledOnNilConn(t *testing.T) {
	dialCalls := 0
	c := &Connection{
		conn: nil,
		dial: func() (*amqp.Connection, error) {
			dialCalls++
			return nil, errors.New("stop here")
		},
	}

	c.Channel() //nolint:errcheck // we only care that dial was called
	if dialCalls != 1 {
		t.Errorf("expected dial to be called once, got %d", dialCalls)
	}
}

// TestChannel_ConcurrentCallsWhenClosed verifies that many goroutines calling
// Channel() simultaneously on a closed connection do not produce a data race
// and that the dial function is serialized (never called concurrently).
//
// Run with: go test -race ./libs/go/rmq/...
func TestChannel_ConcurrentCallsWhenClosed(t *testing.T) {
	t.Parallel()

	var dialCalls atomic.Int32
	var inDial atomic.Int32 // detects concurrent dial invocations

	ready := make(chan struct{})
	c := &Connection{
		conn: nil,
		dial: func() (*amqp.Connection, error) {
			if inDial.Add(1) > 1 {
				t.Error("dial called concurrently — mutex not protecting reconnect")
			}
			dialCalls.Add(1)
			inDial.Add(-1)
			return nil, errors.New("no broker in unit test")
		},
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			<-ready
			c.Channel() //nolint:errcheck
		}()
	}

	close(ready)
	wg.Wait()

	if n := dialCalls.Load(); n == 0 {
		t.Error("expected dial to be called at least once")
	}
}

// TestChannel_ConcurrentClose verifies that Close() and Channel() called
// concurrently from many goroutines do not race on the conn field.
//
// Run with: go test -race ./libs/go/rmq/...
func TestChannel_ConcurrentClose(t *testing.T) {
	t.Parallel()

	dialCount := atomic.Int32{}
	c := &Connection{
		conn: nil,
		dial: func() (*amqp.Connection, error) {
			dialCount.Add(1)
			return nil, errors.New("no broker")
		},
	}

	var wg sync.WaitGroup
	ready := make(chan struct{})

	// Half the goroutines call Channel(), half call Close().
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			if i%2 == 0 {
				c.Channel() //nolint:errcheck
			} else {
				c.Close() //nolint:errcheck
			}
		}(i)
	}

	close(ready)
	wg.Wait()
}

// TestClose_ZeroValue verifies Close() is safe on a zero-value Connection.
func TestClose_ZeroValue(t *testing.T) {
	c := &Connection{}
	if err := c.Close(); err != nil {
		t.Errorf("unexpected error from Close() on zero-value Connection: %v", err)
	}
}

// TestGetConnection_ZeroValue verifies GetConnection() is safe on a zero-value Connection.
func TestGetConnection_ZeroValue(t *testing.T) {
	c := &Connection{}
	if conn := c.GetConnection(); conn != nil {
		t.Errorf("expected nil from GetConnection() on zero-value Connection, got %v", conn)
	}
}
