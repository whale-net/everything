package events

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

// TestRoutingKey pins the "session.{session_id}.{event_type}" scheme
// (events.go's doc comment): every publisher and consumer must agree on
// this exact format, so the builder -- not ad hoc fmt.Sprintf call sites --
// is what this test guards.
func TestRoutingKey(t *testing.T) {
	tests := []struct {
		name      string
		sessionID uuid.UUID
		eventType string
		expected  string
	}{
		{
			name:      "simple type",
			sessionID: uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
			eventType: "turn.started",
			expected:  "session.550e8400-e29b-41d4-a716-446655440000.turn.started",
		},
		{
			name:      "dotted event type is preserved verbatim",
			sessionID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			eventType: "tool.call.completed",
			expected:  "session.00000000-0000-0000-0000-000000000001.tool.call.completed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RoutingKey(tt.sessionID, tt.eventType)
			if got != tt.expected {
				t.Errorf("RoutingKey(%s, %q) = %q, want %q", tt.sessionID, tt.eventType, got, tt.expected)
			}
		})
	}
}

// TestDeclareArgs pins DeclareArgs()'s return values. Every publisher and
// consumer must declare ExchangeName with these exact arguments --
// ExchangeDeclare is idempotent only for matching arguments, so drift here
// is a silent 406 PRECONDITION_FAILED waiting to happen the next time a
// second process declares the exchange differently.
func TestDeclareArgs(t *testing.T) {
	kind, durable, autoDelete, internal, noWait, args := DeclareArgs()

	if kind != "topic" {
		t.Errorf("DeclareArgs() kind = %q, want %q", kind, "topic")
	}
	if !durable {
		t.Errorf("DeclareArgs() durable = %v, want true", durable)
	}
	if autoDelete {
		t.Errorf("DeclareArgs() autoDelete = %v, want false", autoDelete)
	}
	if internal {
		t.Errorf("DeclareArgs() internal = %v, want false", internal)
	}
	if noWait {
		t.Errorf("DeclareArgs() noWait = %v, want false", noWait)
	}
	if args != nil {
		t.Errorf("DeclareArgs() args = %v, want nil", args)
	}

	var _ amqp.Table = args
}

func TestExchangeName(t *testing.T) {
	if ExchangeName != "whagent/events" {
		t.Errorf("ExchangeName = %q, want %q", ExchangeName, "whagent/events")
	}
}

// TestEvent_JSONRoundTrip proves every LB1 field on Event survives a
// marshal/unmarshal round trip byte-for-byte -- this is the contract the
// RabbitMQ message body, and later the S3 jsonl line, both depend on.
func TestEvent_JSONRoundTrip(t *testing.T) {
	original := Event{
		EventID:     uuid.New(),
		SessionID:   uuid.New(),
		Seq:         42,
		Turn:        3,
		Type:        "tool.called",
		Payload:     json.RawMessage(`{"tool":"search","args":{"q":"whales"}}`),
		CommittedAt: time.Date(2026, 9, 8, 12, 30, 0, 0, time.UTC),
	}

	body, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if decoded.EventID != original.EventID {
		t.Errorf("EventID = %v, want %v", decoded.EventID, original.EventID)
	}
	if decoded.SessionID != original.SessionID {
		t.Errorf("SessionID = %v, want %v", decoded.SessionID, original.SessionID)
	}
	if decoded.Seq != original.Seq {
		t.Errorf("Seq = %v, want %v", decoded.Seq, original.Seq)
	}
	if decoded.Turn != original.Turn {
		t.Errorf("Turn = %v, want %v", decoded.Turn, original.Turn)
	}
	if decoded.Type != original.Type {
		t.Errorf("Type = %v, want %v", decoded.Type, original.Type)
	}
	if string(decoded.Payload) != string(original.Payload) {
		t.Errorf("Payload = %s, want %s", decoded.Payload, original.Payload)
	}
	if !decoded.CommittedAt.Equal(original.CommittedAt) {
		t.Errorf("CommittedAt = %v, want %v", decoded.CommittedAt, original.CommittedAt)
	}
}

// TestEvent_JSONFieldNames guards the wire contract's field names: every
// consumer (archiver, embed host) depends on the fixed snake_case keys
// below, not Go's default PascalCase, and not any future field rename that
// forgets to keep the `json:` tag in sync.
func TestEvent_JSONFieldNames(t *testing.T) {
	ev := Event{
		EventID:     uuid.New(),
		SessionID:   uuid.New(),
		Seq:         1,
		Turn:        0,
		Type:        "turn.started",
		Payload:     json.RawMessage(`{}`),
		CommittedAt: time.Now().UTC(),
	}

	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("Unmarshal into map: %v", err)
	}

	for _, field := range []string{"event_id", "session_id", "seq", "turn", "type", "payload", "committed_at"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("marshaled Event missing expected field %q; got %d keys", field, len(raw))
		}
	}
	if len(raw) != 7 {
		t.Errorf("marshaled Event has %d fields, want exactly 7 (no unexpected extra fields)", len(raw))
	}
}
