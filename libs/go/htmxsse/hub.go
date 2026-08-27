package htmxsse

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
)

// Event represents a server-sent event.
type Event struct {
	// Topic is the topic this event was delivered on.
	Topic string
	// RoutingKey is the RabbitMQ routing key.
	RoutingKey string
	// Body is the event payload.
	Body []byte
}

// Transport is the interface for attaching to and managing
// the underlying message broker connection.
type Transport interface {
	// BindExchange binds this transport to an exchange with the given routing keys.
	BindExchange(exchange string, routingKeys []string) error
	// RegisterHandler registers a message handler for delivery events.
	RegisterHandler(queue string, handler rmq.MessageHandler)
	// Start starts consuming messages.
	Start(context.Context) error
	// Close closes the transport.
	Close() error
}

// AttachFunc is a function that creates and returns a Transport.
type AttachFunc func(context.Context) (Transport, error)

// Ticker provides an abstraction over time.Ticker for testing.
type Ticker interface {
	// C returns the channel that receives ticks.
	C() <-chan time.Time
	// Stop stops the ticker.
	Stop()
}

// defaultTicker wraps time.Ticker to implement the Ticker interface.
type defaultTicker struct {
	*time.Ticker
}

func (t *defaultTicker) C() <-chan time.Time {
	return t.Ticker.C
}

// Config holds configuration for the Hub.
type Config struct {
	// ExchangeName is the RabbitMQ exchange name for the Hub (required, caller-configured).
	ExchangeName string
	// HeartbeatInterval is the interval between heartbeats (default ~30s).
	HeartbeatInterval time.Duration
	// MaxStreamLifetime is the maximum lifetime of a stream (default ~1h).
	MaxStreamLifetime time.Duration
	// SubscriberBufferDepth is the depth of the per-subscriber buffer.
	SubscriberBufferDepth int
	// AdvertisedRetryInterval is the interval advertised to clients for retry.
	AdvertisedRetryInterval time.Duration
}

// DefaultConfig returns a Config with recommended defaults.
func DefaultConfig() Config {
	return Config{
		HeartbeatInterval:       30 * time.Second,
		MaxStreamLifetime:       1 * time.Hour,
		SubscriberBufferDepth:   100,
		AdvertisedRetryInterval: 5 * time.Second,
	}
}

// Validate checks the configuration for consistency.
// It returns an error if advertisedRetry >= 2 * heartbeat.
func (c *Config) Validate() error {
	if c.AdvertisedRetryInterval > 0 && c.HeartbeatInterval > 0 {
		if c.AdvertisedRetryInterval >= 2*c.HeartbeatInterval {
			return fmt.Errorf("advertisedRetryInterval (%v) must be less than 2*heartbeatInterval (%v): "+
				"larger values may prevent the not-live indicator from updating",
				c.AdvertisedRetryInterval, 2*c.HeartbeatInterval)
		}
	}
	return nil
}

// subscriber represents an active subscription to a topic.
type subscriber struct {
	topic   string
	eventCh chan Event
}

// Hub manages subscriptions to SSE topics over a message broker.
type Hub struct {
	mu sync.RWMutex

	// Configuration
	config Config
	// Transport attach function
	attachFunc AttachFunc
	// Clock for timeouts and retries
	clock Clock
	// Current transport
	transport Transport
	// Cancel context for the Hub
	cancel context.CancelFunc
	// Context for the Hub
	ctx context.Context

	// Active subscribers, keyed by topic
	subscribers map[string][]*subscriber
	// Subscriber ID to unsubscribe function (for cleanup)
	unsubscribeFuncs map[string]func()
	// Once to ensure attachment starts only once
	attachOnce sync.Once
}

// Clock provides an abstraction over time for testing.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// NewTicker returns a new Ticker.
	NewTicker(d time.Duration) Ticker
	// Sleep sleeps for the given duration, respecting context cancellation.
	Sleep(ctx context.Context, d time.Duration) error
}

// defaultClock implements Clock using the standard library.
type defaultClock struct{}

func (c *defaultClock) Now() time.Time {
	return time.Now()
}

func (c *defaultClock) NewTicker(d time.Duration) Ticker {
	return &defaultTicker{time.NewTicker(d)}
}

func (c *defaultClock) Sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// NewHub creates a new Hub with the given attach function and configuration.
func NewHub(attachFunc AttachFunc, config Config) *Hub {
	return NewHubWithClock(attachFunc, config, &defaultClock{})
}

// NewHubWithClock creates a new Hub with a custom clock (primarily for testing).
func NewHubWithClock(attachFunc AttachFunc, config Config, clock Clock) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		config:               config,
		attachFunc:           attachFunc,
		clock:                clock,
		cancel:               cancel,
		ctx:                  ctx,
		subscribers:          make(map[string][]*subscriber),
		unsubscribeFuncs:     make(map[string]func()),
	}
}

// Config returns the Hub's configuration, particularly useful for accessing
// the HeartbeatInterval for front-end consumption (e.g., to configure
// not-live detection thresholds in SSE client scripts).
func (h *Hub) Config() Config {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.config
}

// Subscribe subscribes to a topic and returns a channel for receiving events
// and an unsubscribe function.
func (h *Hub) Subscribe(topic string) (<-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Ensure the Hub is started (only once)
	h.attachOnce.Do(func() {
		go h.attachWithRetry()
	})

	// Create a subscriber
	sub := &subscriber{
		topic:   topic,
		eventCh: make(chan Event, h.config.SubscriberBufferDepth),
	}

	h.subscribers[topic] = append(h.subscribers[topic], sub)

	// Generate a unique ID for this subscription
	subID := fmt.Sprintf("%s-%p", topic, sub)

	// Create unsubscribe function
	unsubscribe := func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		// Close the channel
		close(sub.eventCh)

		// Remove from subscribers list
		subs := h.subscribers[topic]
		for i, s := range subs {
			if s == sub {
				h.subscribers[topic] = append(subs[:i], subs[i+1:]...)
				break
			}
		}

		// Clean up empty topic lists
		if len(h.subscribers[topic]) == 0 {
			delete(h.subscribers, topic)
		}

		// Clean up the unsubscribe function reference
		delete(h.unsubscribeFuncs, subID)
	}

	h.unsubscribeFuncs[subID] = unsubscribe

	return sub.eventCh, unsubscribe
}

// attachWithRetry attaches to the transport with exponential backoff.
// This runs in a goroutine and doesn't block the calling goroutine.
func (h *Hub) attachWithRetry() {
	delay := 100 * time.Millisecond
	maxDelay := 30 * time.Second
	transportSuccessful := false

	for {
		select {
		case <-h.ctx.Done():
			return
		default:
		}

		transport, err := h.attachFunc(h.ctx)
		if err != nil {
			log.Printf("htmxsse: failed to attach transport: %v, retrying in %v", err, delay)
			if err := h.clock.Sleep(h.ctx, delay); err != nil {
				return
			}
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
			continue
		}

		// Successfully attached
		h.mu.Lock()
		h.transport = transport
		h.mu.Unlock()

		log.Printf("htmxsse: transport attached successfully")

		// Register the catch-all handler
		h.registerHandler()

		// Start the transport - this should block until the context is cancelled
		// or an error occurs
		err = transport.Start(h.ctx)

		// Transport has stopped; prepare to retry
		h.mu.Lock()
		h.transport = nil
		h.mu.Unlock()

		if err != nil {
			log.Printf("htmxsse: transport failed: %v, will retry", err)
		} else {
			log.Printf("htmxsse: transport context cancelled, will retry if subscriptions exist")
		}

		// Check if there are still subscribers; if not, exit
		h.mu.RLock()
		hasSubscribers := len(h.subscribers) > 0
		h.mu.RUnlock()

		if !hasSubscribers {
			log.Printf("htmxsse: no subscribers, exiting attach loop")
			return
		}

		// Apply exponential backoff before retry, resetting delay only if transport was successful
		if transportSuccessful {
			delay = 100 * time.Millisecond
		}
		transportSuccessful = false

		if err := h.clock.Sleep(h.ctx, delay); err != nil {
			return
		}
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}

// registerHandler registers the catch-all message handler.
func (h *Hub) registerHandler() {
	h.mu.RLock()
	transport := h.transport
	exchangeName := h.config.ExchangeName
	h.mu.RUnlock()

	if transport == nil {
		return
	}

	// Register a catch-all handler that dispatches based on routing key
	transport.RegisterHandler("*", h.handleMessage)

	// Bind to the exchange with a wildcard routing key
	err := transport.BindExchange(exchangeName, []string{"#"})
	if err != nil {
		log.Printf("htmxsse: failed to bind exchange: %v", err)
	}
}

// handleMessage handles incoming messages from RabbitMQ.
// This is called by the rmq Consumer's delivery handler.
func (h *Hub) handleMessage(ctx context.Context, msg rmq.Message) error {
	// Extract topic from routing key (topic is the full routing key)
	topic := msg.RoutingKey

	event := Event{
		Topic:      topic,
		RoutingKey: msg.RoutingKey,
		Body:       msg.Body,
	}

	h.mu.RLock()
	subs := h.subscribers[topic]
	// Make a copy to avoid holding the lock while sending
	subsCopy := make([]*subscriber, len(subs))
	copy(subsCopy, subs)
	h.mu.RUnlock()

	// Send to all subscribers on this topic
	for _, sub := range subsCopy {
		select {
		case sub.eventCh <- event:
			// Successfully sent
		default:
			// Channel is full - apply drop-oldest policy
			// The buffered channel will naturally drop the oldest message
			// when we try to send the next one
			select {
			case <-sub.eventCh: // Drain one message
				sub.eventCh <- event // Send the new one
			default:
				// If somehow the channel is still full, just skip this event
			}
		}
	}

	// Always ack unconditionally - we handle the message synchronously
	return nil
}

// Close closes the Hub and cleans up resources.
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Signal context cancellation
	h.cancel()

	// Close all subscriber channels
	for _, subs := range h.subscribers {
		for _, sub := range subs {
			select {
			case <-sub.eventCh:
			default:
				close(sub.eventCh)
			}
		}
	}
	h.subscribers = make(map[string][]*subscriber)
	h.unsubscribeFuncs = make(map[string]func())

	// Close transport if present
	if h.transport != nil {
		if err := h.transport.Close(); err != nil {
			log.Printf("htmxsse: error closing transport: %v", err)
		}
	}

	return nil
}
