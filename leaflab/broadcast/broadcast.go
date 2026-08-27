// Package broadcast provides the single cross-process signalling mechanism used
// by leaflab services: an AMQP fanout exchange that delivers every published
// message to every subscriber, never a competing-consumer work queue.
//
// leaflab/processor publishes signals through this package (cache invalidation,
// FR73/#1203, and config ack, FR47/#1216); leaflab/api subscribes through it,
// one Listener per API replica, so every replica observes every ack (NFR15).
// Both signal types ride the same exchange, distinguished only by routing key —
// see leaflab/ARCHITECTURE.md "Cross-process broadcast signalling" for the
// rationale and leaflab/processor/cache.go for the original (FR73) consumer of
// this mechanism.
//
// Adding a new broadcast signal type means adding a routing key here, not a
// new exchange.
package broadcast

import (
	"context"
	"fmt"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
)

// Exchange is the one fanout exchange leaflab uses for all cross-process
// broadcast signals. It was introduced in #1203 for cache invalidation
// (leaflab.cache-invalidations) and is reused, not duplicated, for every
// broadcast signal leaflab adds afterwards.
const Exchange = "leaflab.cache-invalidations"

const (
	// RoutingKeyCacheInvalidation carries CacheInvalidationSignal payloads (FR73).
	RoutingKeyCacheInvalidation = "leaflab.cache-invalidation"
	// RoutingKeyConfigAck carries ConfigAckSignal payloads (FR47/NFR15).
	RoutingKeyConfigAck = "leaflab.config-ack"
)

// ConfigAckSignal is published whenever the processor receives a device
// acknowledgement for a config push. Every API replica must receive the
// signal to enable bounded waits pinned to any replica (NFR15's broadcast
// constraint). Both leaflab/processor (publisher) and leaflab/api
// (subscriber) use this exact type so there is one canonical wire schema.
type ConfigAckSignal struct {
	// AckedAt is when the ack was received from the device.
	AckedAt time.Time `json:"acked_at"`

	// DeviceID of the board that acked.
	DeviceID string `json:"device_id"`

	// ConfigVersion of the config being acked.
	ConfigVersion int64 `json:"config_version"`

	// Accepted indicates whether the device accepted (true) or rejected (false) the config.
	Accepted bool `json:"accepted"`

	// RejectionReason is the verbatim reason from the device, if rejected.
	// Empty string if the config was accepted.
	RejectionReason string `json:"rejection_reason,omitempty"`
}

// DeclareExchange idempotently declares exchange as a durable fanout exchange.
// Safe to call from multiple processes and multiple times within one process —
// RabbitMQ treats a redeclaration with identical parameters as a no-op.
func DeclareExchange(conn *rmq.Connection, exchange string) error {
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("broadcast: open channel: %w", err)
	}
	defer ch.Close()

	if err := ch.ExchangeDeclare(
		exchange, // exchange name
		"fanout", // exchange type — delivers to every bound queue, not one arbitrary consumer
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
	); err != nil {
		return fmt.Errorf("broadcast: declare fanout exchange %s: %w", exchange, err)
	}
	return nil
}

// Publisher publishes broadcast signals onto a fanout exchange. Construct one
// per process (it wraps an existing *rmq.Publisher, so multiple signal types
// published from the same process can share one underlying connection and
// channel) and call Publish with the signal's routing key.
type Publisher struct {
	pub      *rmq.Publisher
	exchange string
}

// NewPublisher wraps pub for broadcast signalling on exchange, declaring the
// exchange first. It does not take ownership of pub — the caller remains
// responsible for closing it.
func NewPublisher(conn *rmq.Connection, pub *rmq.Publisher, exchange string) (*Publisher, error) {
	if err := DeclareExchange(conn, exchange); err != nil {
		return nil, err
	}
	return &Publisher{pub: pub, exchange: exchange}, nil
}

// Exchange returns the fanout exchange this publisher publishes to.
func (p *Publisher) Exchange() string {
	return p.exchange
}

// Publish sends payload to every subscriber bound to the broadcast exchange,
// tagged with routingKey so subscribers can distinguish signal types.
func (p *Publisher) Publish(ctx context.Context, routingKey string, payload []byte) error {
	return p.pub.Publish(ctx, p.exchange, routingKey, payload)
}

// NewListener creates a *rmq.Consumer subscribed to every message on exchange
// via a private, ephemeral queue exclusive to this call — a broker-assigned
// name, non-durable, auto-deleted when the connection closes. Unlike a durable
// named queue shared across replicas (a competing-consumer work queue), every
// call to NewListener — one per replica — receives every message published to
// exchange, which is what NFR15's "every API replica" constraint requires.
//
// Callers must call RegisterHandler for the routing keys they care about and
// then Start the returned consumer.
func NewListener(conn *rmq.Connection, exchange string) (*rmq.Consumer, error) {
	if err := DeclareExchange(conn, exchange); err != nil {
		return nil, err
	}

	// Empty name + non-durable + auto-delete: the broker assigns a unique name
	// and deletes the queue when this connection's channel closes, so no two
	// listeners can ever collide on the same queue.
	consumer, err := rmq.NewConsumerWithOpts(conn, "", false, true, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("broadcast: create listener queue: %w", err)
	}

	// Fanout exchanges ignore the binding key for routing purposes; "#" here is
	// just a placeholder to satisfy the AMQP bind call.
	if err := consumer.BindExchange(exchange, []string{"#"}); err != nil {
		consumer.Close() //nolint:errcheck
		return nil, fmt.Errorf("broadcast: bind listener to exchange %s: %w", exchange, err)
	}

	return consumer, nil
}
