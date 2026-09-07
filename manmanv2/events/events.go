// Package events defines the shared RabbitMQ exchange identity and helpers
// for manmanv2's live-status htmxsse events. This is the single source of
// truth for exchange identity, routing-key shape, and declare arguments.
//
// Both event-processor (the publisher) and manmanv2/ui (the consumer, via
// libs/go/htmxsse.DefaultAttachFunc) declare this exchange and must agree
// byte-for-byte on its name and declare arguments; a mismatch causes a 406
// PRECONDITION_FAILED error when the UI attaches.
package events

import (
	"fmt"
	"strconv"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ExchangeName is the RabbitMQ topic exchange name for manmanv2's live
// session-status htmxsse events. This is a dedicated exchange, independent
// of the shared "manman" exchange and the "external" exchange used for
// status.session.* republishing.
const ExchangeName = "manmanv2.htmxsse"

// TopicForDeployment returns the routing key for a live session-status event,
// keyed by ServerGameConfig (SGC) id rather than session id. The routing key
// format is "deployment.<sgcID>", allowing subscribers to bind with topic
// wildcards (e.g., "deployment.#").
func TopicForDeployment(sgcID int64) string {
	return fmt.Sprintf("deployment.%d", sgcID)
}

// deploymentTopicPrefix is the routing-key prefix TopicForDeployment always
// produces; ParseDeploymentTopic strips it back off.
const deploymentTopicPrefix = "deployment."

// ParseDeploymentTopic is the inverse of TopicForDeployment: it recovers the
// ServerGameConfig id from a routing key of the form "deployment.<sgcID>".
// ok is false for anything else -- a malformed routing key, a topic from a
// different (future) event kind, or a non-numeric suffix -- callers (e.g.
// manmanv2/ui's live SSE fragment, #1724) must treat that as an
// unknown/unparseable topic rather than guess at a value.
func ParseDeploymentTopic(topic string) (sgcID int64, ok bool) {
	if !strings.HasPrefix(topic, deploymentTopicPrefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(topic, deploymentTopicPrefix), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// DeclareArgs returns the AMQP ExchangeDeclare arguments for the manmanv2
// htmxsse exchange. This must be used by both event-processor and
// manmanv2/ui when declaring the exchange.
//
//   - type: "topic" (for routing key-based matching)
//   - durable: true (survive broker restart)
//   - autoDelete: false (persist even when no queues are bound)
//   - internal: false (allow external clients to publish/consume)
//   - noWait: false (wait for broker acknowledgement)
//   - args: nil (no additional arguments)
//
// ExchangeDeclare is idempotent only for matching arguments; a mismatch
// returns 406 PRECONDITION_FAILED and closes the channel. This must match
// htmxsse.DefaultAttachFunc's declare call exactly.
func DeclareArgs() (kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) {
	return "topic", true, false, false, false, nil
}
