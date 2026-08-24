package htmxsse

import (
	"context"
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

// Config holds configuration for the Hub.
type Config struct {
	// HeartbeatInterval is the interval between heartbeats (default ~30s).
	HeartbeatInterval time.Duration
	// MaxStreamLifetime is the maximum lifetime of a stream (default ~1h).
	MaxStreamLifetime time.Duration
	// SubscriberBufferDepth is the depth of the per-subscriber buffer.
	SubscriberBufferDepth int
	// AdvertisedRetryInterval is the interval advertised to clients for retry.
	AdvertisedRetryInterval time.Duration
}

// Hub manages subscriptions to SSE topics over a message broker.
type Hub struct {
	// Configuration
	config Config
	// Transport attach function
	attachFunc AttachFunc
	// Current transport
	transport Transport
}

// NewHub creates a new Hub with the given attach function and configuration.
func NewHub(attachFunc AttachFunc, config Config) *Hub {
	return &Hub{
		config:     config,
		attachFunc: attachFunc,
	}
}

// Subscribe subscribes to a topic and returns a channel for receiving events
// and an unsubscribe function.
func (h *Hub) Subscribe(topic string) (<-chan Event, func()) {
	// TODO: Implement subscription logic
	return nil, func() {}
}
