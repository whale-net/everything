// Package events defines the shared RabbitMQ exchange configuration and
// helpers for app-registry's htmxsse events. This is the single source of
// truth for exchange identity and declare arguments across all three
// app-registry processes (UI, writeback worker, app-registry-server).
//
// This is deliberately its own tiny package rather than living in a binary's
// implementation: all three binaries must agree on the exact exchange name,
// routing-key shape, and declare arguments. Centralizing these definitions
// here ensures byte-identical ExchangeDeclare calls in all processes,
// preventing 406 PRECONDITION_FAILED errors from argument drift.
package events

import (
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ExchangeName is the RabbitMQ topic exchange name for app-registry's htmxsse
// events. This is a dedicated exchange shared by UI, writeback worker, and
// app-registry-server. All three processes must declare this exchange with
// identical arguments (see DeclareArgs).
const ExchangeName = "app-registry.htmxsse"

// TopicForPromotion returns the routing key for a promotion event, derived
// from the promotion id. The routing key format is "promotion.<id>", allowing
// subscribers to bind with topic wildcards (e.g., "promotion.#").
func TopicForPromotion(id string) string {
	return fmt.Sprintf("promotion.%s", id)
}

// DeclareArgs returns the AMQP ExchangeDeclare arguments for the app-registry
// htmxsse exchange. This must be used by all three processes (UI, worker,
// server) when declaring the exchange.
//
// The arguments match FR0.7 requirements:
//   - type: "topic" (for routing key-based matching)
//   - durable: true (survive broker restart)
//   - autoDelete: false (persist even when no queues are bound)
//   - internal: false (allow external clients to publish/consume)
//   - noWait: false (wait for broker acknowledgement)
//   - args: nil (no additional arguments)
//
// ExchangeDeclare is idempotent only for matching arguments; a mismatch
// returns 406 PRECONDITION_FAILED and closes the channel. Byte-identical
// calls in all three binaries prevent this silent failure mode.
func DeclareArgs() (kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) {
	return "topic", true, false, false, false, nil
}
