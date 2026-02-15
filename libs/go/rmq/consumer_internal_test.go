package rmq

import "testing"

// TestMatchesRoutingKey tests the internal matchesRoutingKey function
func TestMatchesRoutingKey(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		pattern  string
		expected bool
	}{
		{"exact match", "test.key", "test.key", true},
		{"wildcard # matches all", "test.key", "#", true},
		{"wildcard # matches prefix", "test.key.value", "test.#", true},
		{"no match", "test.key", "other.key", false},
		{"empty pattern", "test.key", "", false},
		{"empty key", "", "test.key", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchesRoutingKey(tt.key, tt.pattern)
			if result != tt.expected {
				t.Errorf("matchesRoutingKey(%q, %q) = %v, want %v", tt.key, tt.pattern, result, tt.expected)
			}
		})
	}
}

// TestBuildQueueArguments_DurableQueueNoExpires verifies that durable queues
// do not get x-expires. This was the root cause of a production failure where
// re-declaring an existing durable queue with x-expires caused a
// PRECONDITION_FAILED error from RabbitMQ.
func TestBuildQueueArguments_DurableQueueNoExpires(t *testing.T) {
	args := buildQueueArguments("processor-events", true, false, 0, 0)

	if args == nil {
		t.Fatal("expected non-nil arguments for durable queue (should have DLQ config)")
	}

	if _, ok := args["x-expires"]; ok {
		t.Error("durable queue must not have x-expires; this causes PRECONDITION_FAILED when the queue already exists without it")
	}

	// Verify DLQ routing is still present
	if v, ok := args["x-dead-letter-routing-key"]; !ok || v != "processor-events-dlq" {
		t.Errorf("expected x-dead-letter-routing-key = 'processor-events-dlq', got %v", v)
	}
}

// TestBuildQueueArguments_NonDurableNonAutoDeleteGetsExpires verifies that
// non-durable, non-auto-delete queues still get x-expires as a safety net.
func TestBuildQueueArguments_NonDurableNonAutoDeleteGetsExpires(t *testing.T) {
	args := buildQueueArguments("temp-queue", false, false, 0, 0)

	if args == nil {
		t.Fatal("expected non-nil arguments for non-durable non-auto-delete queue")
	}

	expires, ok := args["x-expires"]
	if !ok {
		t.Fatal("non-durable, non-auto-delete queue should have x-expires")
	}
	if expires != 300000 {
		t.Errorf("expected x-expires = 300000, got %v", expires)
	}
}

// TestBuildQueueArguments_AutoDeleteNoExpires verifies that auto-delete queues
// do not get x-expires (RabbitMQ handles cleanup automatically).
func TestBuildQueueArguments_AutoDeleteNoExpires(t *testing.T) {
	args := buildQueueArguments("auto-queue", false, true, 0, 0)

	if args != nil {
		if _, ok := args["x-expires"]; ok {
			t.Error("auto-delete queue should not have x-expires")
		}
	}
}

// TestBuildQueueArguments_MessageTTLAndMaxMessages verifies optional limits.
func TestBuildQueueArguments_MessageTTLAndMaxMessages(t *testing.T) {
	args := buildQueueArguments("limited-queue", true, false, 60000, 1000)

	if args == nil {
		t.Fatal("expected non-nil arguments")
	}

	if v, ok := args["x-message-ttl"]; !ok || v != 60000 {
		t.Errorf("expected x-message-ttl = 60000, got %v", v)
	}
	if v, ok := args["x-max-length"]; !ok || v != 1000 {
		t.Errorf("expected x-max-length = 1000, got %v", v)
	}
	if v, ok := args["x-overflow"]; !ok || v != "drop-head" {
		t.Errorf("expected x-overflow = 'drop-head', got %v", v)
	}
	// Still no x-expires on durable queue
	if _, ok := args["x-expires"]; ok {
		t.Error("durable queue with TTL/max-length must not have x-expires")
	}
}
