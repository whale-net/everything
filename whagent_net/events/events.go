// Package events owns the whagent-net event-bus contract (LB1/LB7): the
// exchange identity, routing-key scheme, and the record type every
// publisher and consumer agrees on. This is a dedicated, tiny package
// (modelled on tools/app_registry/events) rather than living inside a
// binary's implementation, because every process that touches the bus --
// worker (publish), archiver, ui, and any embed host (consume) -- must
// agree on the exact exchange name, routing-key shape, and declare
// arguments; centralizing them here prevents 406 PRECONDITION_FAILED from
// argument drift and keeps the routing-key format out of call sites.
package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// ExchangeName is the RabbitMQ topic exchange every whagent-net process
// publishes committed transcript events to and consumes them from.
const ExchangeName = "whagent/events"

// DeclareArgs returns the AMQP ExchangeDeclare arguments for ExchangeName.
// Every publisher and consumer must use this when declaring the exchange:
// ExchangeDeclare is idempotent only for matching arguments, so drift
// between processes causes a 406 PRECONDITION_FAILED that closes the
// channel.
func DeclareArgs() (kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) {
	return "topic", true, false, false, false, nil
}

// RoutingKey builds the routing key for a session's event, in the fixed
// scheme "session.{session_id}.{event_type}". Callers must always go
// through this builder rather than formatting the string themselves, so
// the scheme has exactly one definition.
func RoutingKey(sessionID uuid.UUID, eventType string) string {
	return fmt.Sprintf("session.%s.%s", sessionID, eventType)
}

// Transcript event type constants (LB1): the `type` column value every
// producer/consumer of a `transcript_event` row agrees on -- centralized
// here for the same reason ExchangeName/RoutingKey are (a free-text
// column with no DB-level enum, so drift between the worker that writes it
// and any later reader, e.g. archiver/ui/mcp, must be prevented by sharing
// one Go definition rather than by convention alone). whagent_net/worker
// is the first writer (issue #2114): EventTypeUserMessage/
// EventTypeAssistantMessage carry a message-shaped payload (see
// whagent_net/worker/context.go's transcriptMessagePayload, the schema
// every producer/consumer of these two types agrees on). Tool-call/
// tool-result event types are added by the follow-up task that first
// writes them (tool dispatch -- ARCHITECTURE.md "Session workflow").
//
// EventTypeCapped/EventTypeFailure (issue #2119) are the two terminal
// transcript events FR6/FR7/FR2 describe: committed once, immediately
// before the session's status write goes terminal, so a consumer can
// react to "ran out"/"failed" differently from "finished" without
// inspecting the session row. Both go through the same
// TranscriptStore.AppendIfAbsent path every other event does (no new
// store method) -- retry-safe the same way a re-invoked CommitTurn
// activity is (session/turn_commit.go).
const (
	EventTypeUserMessage      = "user_message"
	EventTypeAssistantMessage = "assistant_message"

	// EventTypeCapped is committed when a session trips its turn cap
	// (FR6) or cost cap (FR7). Payload carries which cap tripped
	// (session.CapKind's wire value, "turns" or "cost") -- see
	// whagent_net/worker/caps.go.
	EventTypeCapped = "capped"

	// EventTypeFailure is committed when a session ends `failed` (FR2).
	// Payload carries the same error_category/error_detail GetSession
	// reports (FR3) -- one classification, two surfaces, never two
	// independently-derived answers. Never originated for a domain
	// server's own `isError` tool result -- that stays an ordinary
	// tool-result event (FR2's boundary; see
	// whagent_net/worker/tools/dispatch.go's package doc comment,
	// "isError is not a whagent-net failure"). See
	// whagent_net/worker/classify.go.
	EventTypeFailure = "failure"
)

// Event is the whagent-net LB1 record: the single definition of a
// committed transcript event. The `transcript_event` Postgres row, the
// RabbitMQ message body, and -- later -- the S3 jsonl line are all this
// same record, not three independent projections of it. EventID is
// globally unique and time-ordered (UUIDv7 or equivalent) and stable
// across re-publish onto the bus; Seq is a per-session monotonic sequence
// assigned at commit time.
type Event struct {
	EventID     uuid.UUID       `json:"event_id"`
	SessionID   uuid.UUID       `json:"session_id"`
	Seq         int64           `json:"seq"`
	Turn        int             `json:"turn"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	CommittedAt time.Time       `json:"committed_at"`
}
