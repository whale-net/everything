package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/whale-net/everything/libs/go/rmq"
)

// EventPayload is the small structured JSON payload for app-registry events.
// It contains the affected promotion ID and advisory event-kind/status field
// for logging and diagnostics. Consumers re-read state at delivery.
type EventPayload struct {
	PromotionID string `json:"promotion_id"`
	EventKind   string `json:"event_kind"`   // Advisory: e.g. "promotion_started", "promotion_completed"
	EventStatus string `json:"event_status"` // Advisory: e.g. "pending", "success", "failed"
}

// publishRequest is an internal struct for events enqueued to the buffer.
type publishRequest struct {
	payload EventPayload
	// done is closed when the publish completes (or is dropped).
	// This allows callers to await the result if needed (though
	// the non-blocking model means most don't).
	done chan error
}

// Publisher is the app-registry-owned event publisher component. It is
// shared by app-registry-server and writeback-worker binaries.
//
// The Publisher enqueues events to a bounded in-process buffer and returns
// immediately to the caller. The actual broker publish happens on a
// background goroutine using the component's own process-lifetime context,
// not the caller's context. If the buffer is full, events are dropped and
// logged; if the broker is unreachable, the component attaches in the
// background with backoff, starting the process immediately without waiting.
//
// Property 1 (non-blocking hand-off): Publish returns after enqueuing to a
// bounded buffer, performing no broker I/O on the caller's goroutine and
// never blocking on a full buffer.
//
// Property 2 (process-lifetime context): The background publish runs on a
// context that lives for the lifetime of the process, not the caller's
// context. Cancelling the caller's context does not cancel an in-flight
// broker publish that has already been accepted for delivery.
//
// Property 3 (non-fatal construction, background self-healing): Construction
// does not fail the host process. Connect, channel open, and ExchangeDeclare
// happen in the background with backoff; the process starts and serves
// immediately. Publishes enqueued while unattached are dropped and logged;
// the component attaches with no operator intervention once the broker is
// reachable and the exchange is declarable.
//
// Property 4 (bounded, best-effort shutdown): On process shutdown, the
// component drains within a bounded deadline. Anything undrained is dropped
// and logged. No shutdown path blocks the Temporal worker's or the gRPC
// server's own shutdown beyond that bound.
//
// Broker-side publish failures are best-effort and may not be observable at
// all (e.g., a publish to a not-yet-declared exchange returns nil while the
// broker closes the channel asynchronously with a 404). This is accepted
// under the best-effort model.
type Publisher struct {
	mu sync.Mutex
	// Immutable after construction:
	bufferSize     int
	doneCtx        context.Context
	doneCancel     context.CancelFunc
	logger         *slog.Logger
	conn           *rmq.Connection
	exchangeName   string
	declareArgs    func() (kind string, durable, autoDelete, internal, noWait bool, args amqp.Table)
	newPublisherFn func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error)

	// Mutable state:
	buffer chan *publishRequest
	// attached is atomically set/cleared by the background goroutine
	// to track whether the publisher is currently attached to the broker.
	attached atomic.Bool
	// publisher is protected by mu; only the background goroutine
	// and Shutdown (via Close) access it.
	publisher *rmq.Publisher
	// drainedCounter tracks how many publishes were successfully enqueued
	// to the background queue for delivery.
	drainedCounter atomic.Int64
	// droppedCounter tracks how many publishes were dropped due to buffer
	// being full or other internal errors.
	droppedCounter atomic.Int64
	// shutdownOnce ensures Shutdown is called at most once.
	shutdownOnce sync.Once
	// shutdownDone is closed when shutdown completes.
	shutdownDone chan struct{}
}

// NewPublisher constructs a new Publisher for the app-registry exchange.
// It does not fail the host process; any errors happen in the background
// with automatic backoff and recovery.
//
// The newPublisherFn parameter allows tests to inject a fake rmq.NewPublisherWithExchange
// for testing attach failures and backoff behavior without a real broker.
func NewPublisher(ctx context.Context, conn *rmq.Connection, logger *slog.Logger, bufferSize int, newPublisherFn func(conn *rmq.Connection, exchange string) (*rmq.Publisher, error)) *Publisher {
	doneCtx, doneCancel := context.WithCancel(context.Background())

	p := &Publisher{
		bufferSize:     bufferSize,
		doneCtx:        doneCtx,
		doneCancel:     doneCancel,
		logger:         logger,
		conn:           conn,
		exchangeName:   ExchangeName,
		declareArgs:    DeclareArgs,
		newPublisherFn: newPublisherFn,
		buffer:         make(chan *publishRequest, bufferSize),
		shutdownDone:   make(chan struct{}),
	}

	// Start the background publish goroutine. This goroutine lives for
	// the lifetime of the process and attaches in the background with
	// exponential backoff until the broker is reachable.
	go p.backgroundPublisher()

	return p
}

// Publish enqueues an event for publication. It returns immediately after
// enqueuing to the bounded buffer, performing no broker I/O. If the buffer
// is full, the event is dropped and logged.
func (p *Publisher) Publish(promotionID, eventKind, eventStatus string) {
	payload := EventPayload{
		PromotionID: promotionID,
		EventKind:   eventKind,
		EventStatus: eventStatus,
	}

	req := &publishRequest{
		payload: payload,
		done:    make(chan error, 1), // Buffered so backgroundPublisher never blocks sending the result
	}

	// Try to enqueue the request to the buffer.
	select {
	case p.buffer <- req:
		// Successfully enqueued; caller returns immediately.
		p.drainedCounter.Add(1)
	default:
		// Buffer is full; drop the event and log it.
		p.droppedCounter.Add(1)
		p.logger.Warn("event buffer full, dropping publish", "promotion_id", promotionID, "event_kind", eventKind)
		req.done <- fmt.Errorf("buffer full")
	}
}

// backgroundPublisher runs on its own goroutine for the lifetime of the
// process, draining the buffer and publishing events to the broker with
// automatic backoff and recovery.
func (p *Publisher) backgroundPublisher() {
	defer close(p.shutdownDone)

	// Exponential backoff for attach failures
	backoffDuration := time.Second
	maxBackoff := time.Minute

	for {
		// Attach to the broker if not already attached
		if !p.attached.Load() {
			p.attach(&backoffDuration, maxBackoff)
		}

		// Try to drain one event from the buffer with a timeout
		// to periodically check if we should shut down.
		select {
		case <-p.doneCtx.Done():
			// Process shutdown initiated; drain remaining events
			// within bounded deadline (5 seconds)
			p.drainRemaining()
			return

		case req, ok := <-p.buffer:
			if !ok {
				// Buffer closed; graceful shutdown
				return
			}

			if p.attached.Load() {
				p.publishEvent(req)
			} else {
				// Not attached; drop and log
				p.droppedCounter.Add(1)
				p.logger.Warn("broker not attached, dropping publish",
					"promotion_id", req.payload.PromotionID,
					"event_kind", req.payload.EventKind)
				req.done <- fmt.Errorf("broker not attached")
			}
		}
	}
}

// attach attempts to connect to the broker and declare the exchange.
// On failure, it sleeps with exponential backoff before returning.
func (p *Publisher) attach(backoffDuration *time.Duration, maxBackoff time.Duration) {
	pub, err := p.newPublisherFn(p.conn, p.exchangeName)
	if err != nil {
		p.logger.Warn("failed to attach to broker, will retry",
			"error", err,
			"backoff", *backoffDuration)
		time.Sleep(*backoffDuration)
		// Increase backoff for next attempt (capped at maxBackoff)
		*backoffDuration *= 2
		if *backoffDuration > maxBackoff {
			*backoffDuration = maxBackoff
		}
		return
	}

	p.mu.Lock()
	p.publisher = pub
	p.mu.Unlock()
	p.attached.Store(true)
	p.logger.Info("attached to broker", "exchange", p.exchangeName)
	// Reset backoff on successful attach
	*backoffDuration = time.Second
}

// publishEvent publishes a single event to the broker.
func (p *Publisher) publishEvent(req *publishRequest) {
	payload, err := json.Marshal(req.payload)
	if err != nil {
		p.logger.Error("failed to marshal event payload",
			"error", err,
			"promotion_id", req.payload.PromotionID)
		req.done <- fmt.Errorf("marshal error: %w", err)
		return
	}

	routingKey := TopicForPromotion(req.payload.PromotionID)

	// Use a bounded context (5 seconds) for the broker publish.
	// This is the same timeout as rmq.Publisher.Publish itself.
	ctx, cancel := context.WithTimeout(p.doneCtx, 5*time.Second)
	defer cancel()

	p.mu.Lock()
	pub := p.publisher
	p.mu.Unlock()

	if pub == nil {
		// Should not happen if attached is true, but be defensive
		p.logger.Warn("publisher is nil despite attached=true")
		req.done <- fmt.Errorf("publisher not ready")
		return
	}

	err = pub.Publish(ctx, p.exchangeName, routingKey, payload)
	if err != nil {
		p.logger.Warn("failed to publish event to broker",
			"error", err,
			"promotion_id", req.payload.PromotionID,
			"exchange", p.exchangeName,
			"routing_key", routingKey)
		// Mark as detached; the background loop will retry attach
		p.attached.Store(false)
		req.done <- fmt.Errorf("publish error: %w", err)
		return
	}

	p.logger.Debug("published event to broker",
		"promotion_id", req.payload.PromotionID,
		"event_kind", req.payload.EventKind,
		"exchange", p.exchangeName,
		"routing_key", routingKey)
	req.done <- nil
}

// drainRemaining drains the buffer within a bounded deadline (5 seconds)
// on process shutdown.
func (p *Publisher) drainRemaining() {
	drainCtx, cancel := context.WithTimeout(p.doneCtx, 5*time.Second)
	defer cancel()

	drainedOnShutdown := 0
	droppedOnShutdown := 0

	for {
		select {
		case <-drainCtx.Done():
			p.logger.Info("shutdown drain deadline reached",
				"drained_on_shutdown", drainedOnShutdown,
				"dropped_on_shutdown", droppedOnShutdown)
			return

		case req, ok := <-p.buffer:
			if !ok {
				// Buffer already closed
				return
			}

			if p.attached.Load() {
				p.publishEvent(req)
				drainedOnShutdown++
			} else {
				droppedOnShutdown++
				p.logger.Warn("dropped event on shutdown, broker not attached",
					"promotion_id", req.payload.PromotionID)
				req.done <- fmt.Errorf("shutdown")
			}
		}
	}
}

// Close closes the publisher and waits for graceful shutdown.
// It closes the buffer and waits for the background goroutine to finish
// draining, up to a bounded deadline.
func (p *Publisher) Close(shutdownCtx context.Context) error {
	var err error
	p.shutdownOnce.Do(func() {
		// Signal shutdown to the background goroutine
		p.doneCancel()

		// Wait for background goroutine to finish draining
		select {
		case <-p.shutdownDone:
			// Graceful shutdown completed
		case <-shutdownCtx.Done():
			p.logger.Warn("shutdown context canceled before drain completed")
			err = shutdownCtx.Err()
		}

		// Close the underlying rmq.Publisher
		p.mu.Lock()
		if p.publisher != nil {
			closeErr := p.publisher.Close()
			if closeErr != nil {
				p.logger.Error("error closing rmq.Publisher", "error", closeErr)
				if err == nil {
					err = closeErr
				}
			}
		}
		p.mu.Unlock()

		// Log drain statistics
		drained := p.drainedCounter.Load()
		dropped := p.droppedCounter.Load()
		p.logger.Info("publisher closed",
			"total_drained", drained,
			"total_dropped", dropped)
	})
	return err
}
