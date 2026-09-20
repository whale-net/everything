package rmq

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

// fakeAcknowledger records which acknowledgement method handleMessage called,
// so tests can distinguish "handler was invoked and message acked" from
// "no handler matched and the message was Nacked".
type fakeAcknowledger struct {
	acked  bool
	nacked bool
}

func (f *fakeAcknowledger) Ack(tag uint64, multiple bool) error {
	f.acked = true
	return nil
}

func (f *fakeAcknowledger) Nack(tag uint64, multiple bool, requeue bool) error {
	f.nacked = true
	return nil
}

func (f *fakeAcknowledger) Reject(tag uint64, requeue bool) error {
	f.nacked = true
	return nil
}

// TestHandleMessage_CatchAllPatternDispatchesMultiSegmentRoutingKey is a
// regression test for the htmxsse Hub's catch-all registration: it exercises
// the real (*Consumer).handleMessage dispatch path (matchesRoutingKey, not a
// fake transport double) with a handler registered under "#" — the pattern
// rmq treats as match-everything — and a realistic multi-segment routing key.
// It fails if a handler is instead registered under a pattern like "*", which
// matchesRoutingKey does not treat as a wildcard and would cause the message
// to be Nacked and dropped instead of dispatched.
func TestHandleMessage_CatchAllPatternDispatchesMultiSegmentRoutingKey(t *testing.T) {
	var invoked bool
	var gotRoutingKey string

	c := &Consumer{
		handlers: map[string]MessageHandler{
			"#": func(ctx context.Context, msg Message) error {
				invoked = true
				gotRoutingKey = msg.RoutingKey
				return nil
			},
		},
	}

	ack := &fakeAcknowledger{}
	delivery := amqp.Delivery{
		Acknowledger: ack,
		RoutingKey:   "session.abc123.status_change:running",
	}

	c.handleMessage(context.Background(), delivery)

	if !invoked {
		t.Fatal("expected handler registered under \"#\" to be invoked for a multi-segment routing key, but it was not")
	}
	if gotRoutingKey != "session.abc123.status_change:running" {
		t.Errorf("handler received routing key %q, want %q", gotRoutingKey, "session.abc123.status_change:running")
	}
	if !ack.acked {
		t.Error("expected message to be Acked after successful dispatch")
	}
	if ack.nacked {
		t.Error("expected message not to be Nacked; a Nack means dispatch failed to find the handler")
	}
}
