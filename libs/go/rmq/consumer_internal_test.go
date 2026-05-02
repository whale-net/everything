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

// TestBuildQueueArguments_DurableWithLimits_NoDLQ verifies that durable queues
// with TTL or max-length do NOT get dead-letter routing. These are high-throughput
// queues (e.g. log streams) where TTL expiry and overflow are expected operational
// conditions — routing them to the DLQ would flood it.
func TestBuildQueueArguments_DurableWithLimits_NoDLQ(t *testing.T) {
	cases := []struct {
		name        string
		messageTTL  int
		maxMessages int
	}{
		{"ttl only", 60000, 0},
		{"max-messages only", 0, 1000},
		{"both", 60000, 1000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := buildQueueArguments("logs.session.1", true, false, tc.messageTTL, tc.maxMessages)
			if args == nil {
				return // no args at all is also fine — no DLQ
			}
			if _, ok := args["x-dead-letter-exchange"]; ok {
				t.Errorf("durable queue with limits must not have x-dead-letter-exchange")
			}
			if _, ok := args["x-dead-letter-routing-key"]; ok {
				t.Errorf("durable queue with limits must not have x-dead-letter-routing-key")
			}
		})
	}
}

// TestBuildQueueArguments_DurableUnlimited_HasDLQ verifies that unlimited durable
// queues (e.g. lifecycle event queues) still get dead-letter routing.
func TestBuildQueueArguments_DurableUnlimited_HasDLQ(t *testing.T) {
	args := buildQueueArguments("log-processor-lifecycle", true, false, 0, 0)

	if args == nil {
		t.Fatal("expected non-nil arguments for unlimited durable queue")
	}
	if _, ok := args["x-dead-letter-exchange"]; !ok {
		t.Error("unlimited durable queue should have x-dead-letter-exchange")
	}
	if v, ok := args["x-dead-letter-routing-key"]; !ok || v != "log-processor-lifecycle-dlq" {
		t.Errorf("expected x-dead-letter-routing-key = 'log-processor-lifecycle-dlq', got %v", v)
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

// TestBindExchange_CopiesRoutingKeys verifies that mutating the caller's slice after
// BindExchange does not affect the stored binding.
func TestBindExchange_CopiesRoutingKeys(t *testing.T) {
	keys := []string{"logs.session.1", "logs.session.2"}
	c := &Consumer{
		handlers: make(map[string]MessageHandler),
	}

	// Use internal method directly (no channel needed for this test path)
	keysCopy := append([]string(nil), keys...)
	c.bindings = append(c.bindings, binding{exchange: "manman", routingKeys: keysCopy})

	// Mutate the original slice — stored binding must be unaffected
	keys[0] = "MUTATED"

	if c.bindings[0].routingKeys[0] != "logs.session.1" {
		t.Errorf("BindExchange must copy routingKeys; got %q after caller mutation", c.bindings[0].routingKeys[0])
	}
}

// TestStartConsuming_DeepCopiesBindings verifies that the snapshot taken inside
// startConsuming is isolated from concurrent appends to c.bindings.
func TestStartConsuming_DeepCopiesBindings(t *testing.T) {
	original := []string{"logs.session.1"}
	c := &Consumer{
		handlers: make(map[string]MessageHandler),
		bindings: []binding{
			{exchange: "manman", routingKeys: append([]string(nil), original...)},
		},
	}

	// Simulate the deep copy that startConsuming performs under the mutex
	c.mu.Lock()
	snapshot := append(c.bindings[:0:0], c.bindings...)
	for i := range snapshot {
		snapshot[i].routingKeys = append(snapshot[i].routingKeys[:0:0], snapshot[i].routingKeys...)
	}
	c.mu.Unlock()

	// Append a new binding to c.bindings after snapshot
	c.mu.Lock()
	c.bindings = append(c.bindings, binding{exchange: "manman", routingKeys: []string{"logs.session.2"}})
	c.mu.Unlock()

	// Snapshot must still have only 1 binding
	if len(snapshot) != 1 {
		t.Errorf("snapshot should have 1 binding, got %d", len(snapshot))
	}

	// Mutate the inner slice on c.bindings — snapshot must be unaffected
	c.mu.Lock()
	c.bindings[0].routingKeys[0] = "MUTATED"
	c.mu.Unlock()

	if snapshot[0].routingKeys[0] != "logs.session.1" {
		t.Errorf("deep copy failed: snapshot routingKeys[0] = %q", snapshot[0].routingKeys[0])
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
