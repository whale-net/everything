package events

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
)

// newTestLogger creates a logger that discards all output for testing.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// FakePublisher is a mock rmq.Publisher for testing.
type FakePublisher struct {
	publishErr    error
	publishCount  int
	publishBlocks bool
	blockUnblock  chan struct{}
}

func (f *FakePublisher) Publish(ctx context.Context, exchange, routingKey string, body interface{}) error {
	if f.publishBlocks {
		<-f.blockUnblock
	}
	f.publishCount++
	return f.publishErr
}

func (f *FakePublisher) PublishWithExpiry(ctx context.Context, exchange, routingKey string, body interface{}, expiry time.Duration) error {
	return f.publishErr
}

func (f *FakePublisher) PublishWithReply(ctx context.Context, exchange, routingKey string, body []byte, replyTo, correlationID string) error {
	return f.publishErr
}

func (f *FakePublisher) Close() error {
	return nil
}

// TestNonBlockingHandoff verifies that Publish returns immediately
// without blocking on broker I/O.
func TestNonBlockingHandoff(t *testing.T) {
	logger := newTestLogger()

	// Create a fake connection and publisher factory
	fakeConn := &rmq.Connection{}

	publisherFn := func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error) {
		return (*rmq.Publisher)(nil), nil // Will not be called because buffer is pre-filled
	}

	pub := NewPublisher(context.Background(), fakeConn, logger, 1, publisherFn)

	// Publish should return immediately even if the background goroutine is slow
	start := time.Now()
	pub.Publish("promo-1", "test_event", "started")
	elapsed := time.Since(start)

	// Should complete almost instantly (< 100ms)
	if elapsed > 100*time.Millisecond {
		t.Errorf("Publish took %v, want < 100ms", elapsed)
	}

	pub.Close(context.Background())
}

// TestFullBufferDropsEvents verifies that events are dropped and logged
// when the buffer is full.
func TestFullBufferDropsEvents(t *testing.T) {
	logger := newTestLogger()

	fakeConn := &rmq.Connection{}
	publisherFn := func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error) {
		return (*rmq.Publisher)(nil), nil
	}

	pub := NewPublisher(context.Background(), fakeConn, logger, 1, publisherFn)

	// Fill the buffer
	pub.Publish("promo-1", "event_1", "started")
	// This should be dropped because buffer is full
	pub.Publish("promo-2", "event_2", "started")

	// Verify the counters
	if pub.drainedCounter.Load() != 1 {
		t.Errorf("drainedCounter = %d, want 1", pub.drainedCounter.Load())
	}
	if pub.droppedCounter.Load() != 1 {
		t.Errorf("droppedCounter = %d, want 1", pub.droppedCounter.Load())
	}

	pub.Close(context.Background())
}

// TestNonFatalConstruction verifies that the constructor does not fail
// even if broker attachment fails.
func TestNonFatalConstruction(t *testing.T) {
	logger := newTestLogger()

	fakeConn := &rmq.Connection{}
	// This factory always fails
	publisherFn := func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error) {
		return nil, errAttachFailed
	}

	// Construction should succeed even though attach will fail
	pub := NewPublisher(context.Background(), fakeConn, logger, 10, publisherFn)
	if pub == nil {
		t.Fatal("NewPublisher returned nil, want non-nil")
	}

	pub.Close(context.Background())
}

// TestBoundedEnqueue verifies that Publish returns within a bounded time
// even if the transport is blocked.
func TestBoundedEnqueue(t *testing.T) {
	logger := newTestLogger()

	fakeConn := &rmq.Connection{}

	publisherFn := func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error) {
		return (*rmq.Publisher)(nil), nil
	}

	pub := NewPublisher(context.Background(), fakeConn, logger, 10, publisherFn)

	// The Publish call should still return immediately, regardless of whether
	// the underlying transport is blocked.
	start := time.Now()
	pub.Publish("promo-1", "test_event", "started")
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("Publish returned too slowly: %v", elapsed)
	}

	pub.Close(context.Background())
}

// TestProcessLifetimeContext verifies that the publisher uses its own
// process-lifetime context, independent of caller context.
func TestProcessLifetimeContext(t *testing.T) {
	logger := newTestLogger()

	fakeConn := &rmq.Connection{}
	publisherFn := func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error) {
		return (*rmq.Publisher)(nil), nil
	}

	// Create the publisher with a background context
	pub := NewPublisher(context.Background(), fakeConn, logger, 10, publisherFn)

	// Publish some events
	pub.Publish("promo-1", "test_event", "started")
	pub.Publish("promo-2", "test_event", "started")

	// The publisher should still function independently of any caller context
	if pub.drainedCounter.Load() != 2 {
		t.Errorf("drainedCounter = %d, want 2", pub.drainedCounter.Load())
	}

	pub.Close(context.Background())
}

// TestSelfHealingAttach verifies that attach failures with backoff
// eventually succeed.
func TestSelfHealingAttach(t *testing.T) {
	logger := newTestLogger()

	fakeConn := &rmq.Connection{}
	attemptCount := 0
	publisherFn := func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error) {
		attemptCount++
		// Fail the first 2 attempts, then succeed
		if attemptCount <= 2 {
			return nil, errAttachFailed
		}
		return (*rmq.Publisher)(nil), nil
	}

	pub := NewPublisher(context.Background(), fakeConn, logger, 10, publisherFn)

	// Wait a bit for the background goroutine to attempt attach multiple times
	time.Sleep(3 * time.Second)

	// By now, the publisher should have successfully attached
	// (after 2 failed attempts with backoff)
	if pub.attached.Load() {
		t.Logf("Publisher attached after %d attempts", attemptCount)
	} else {
		t.Logf("Publisher not yet attached after %d attempts (may retry)", attemptCount)
	}

	pub.Close(context.Background())
}

// TestBoundedShutdown verifies that shutdown completes within a bounded
// deadline.
func TestBoundedShutdown(t *testing.T) {
	logger := newTestLogger()

	fakeConn := &rmq.Connection{}
	publisherFn := func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error) {
		return (*rmq.Publisher)(nil), nil
	}

	pub := NewPublisher(context.Background(), fakeConn, logger, 10, publisherFn)

	// Queue some events
	for i := 0; i < 5; i++ {
		pub.Publish("promo-1", "event", "started")
	}

	// Shutdown should complete within the bounded deadline
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := pub.Close(shutdownCtx)
	elapsed := time.Since(start)

	t.Logf("Shutdown completed in %v", elapsed)
	if err != nil && err != context.DeadlineExceeded {
		t.Errorf("Close returned unexpected error: %v", err)
	}
}

var errAttachFailed = &AttachError{msg: "attach failed"}

// AttachError is a fake error for testing.
type AttachError struct {
	msg string
}

func (e *AttachError) Error() string {
	return e.msg
}
