package events

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestTopicForPromotion(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		expected string
	}{
		{
			name:     "simple id",
			id:       "abc123",
			expected: "promotion.abc123",
		},
		{
			name:     "id with hyphens",
			id:       "promo-001",
			expected: "promotion.promo-001",
		},
		{
			name:     "uuid-like id",
			id:       "550e8400-e29b-41d4-a716-446655440000",
			expected: "promotion.550e8400-e29b-41d4-a716-446655440000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TopicForPromotion(tt.id)
			if got != tt.expected {
				t.Errorf("TopicForPromotion(%q) = %q, want %q", tt.id, got, tt.expected)
			}
		})
	}
}

func TestDeclareArgs(t *testing.T) {
	kind, durable, autoDelete, internal, noWait, args := DeclareArgs()

	// Verify FR0.7 requirements: type "topic", durable=true, autoDelete=false,
	// internal=false, noWait=false, args=nil
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

	// Ensure the return types are the expected AMQP types
	var _ string = kind
	var _ bool = durable
	var _ bool = autoDelete
	var _ bool = internal
	var _ bool = noWait
	var _ amqp.Table = args
}

func TestExchangeName(t *testing.T) {
	expected := "app-registry.htmxsse"
	if ExchangeName != expected {
		t.Errorf("ExchangeName = %q, want %q", ExchangeName, expected)
	}
}
