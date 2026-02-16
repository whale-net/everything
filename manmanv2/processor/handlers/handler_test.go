package handlers

import (
	"testing"
)

func TestMatchRoutingKey(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		routingKey string
		expected   bool
	}{
		// Exact matches
		{"exact match", "status.host.online", "status.host.online", true},
		{"exact match different", "health.heartbeat", "health.heartbeat", true},

		// Single word wildcard (*)
		{"star matches one word", "status.*.online", "status.host.online", true},
		{"star no match too many words", "status.*.online", "status.host.server.online", false},
		{"star no match too few words", "status.*", "status", false},

		// Multi-word wildcard (#)
		{"hash matches zero words", "status.#", "status", true},
		{"hash matches one word", "status.#", "status.host", true},
		{"hash matches many words", "status.#", "status.host.server.online", true},
		{"hash at end", "status.host.#", "status.host.online", true},
		{"hash at end multiple", "status.host.#", "status.host.online.extra", true},
		{"hash only", "#", "anything.goes.here", true},

		// Complex patterns
		{"star and hash", "status.*.#", "status.host.online.extra", true},
		{"multiple stars", "*.host.*", "status.host.online", true},

		// No matches
		{"different prefix", "status.host.online", "health.host.online", false},
		{"different suffix", "status.host.online", "status.host.offline", false},
		{"too short for pattern", "status.host.online", "status.host", false},
		{"too long for pattern", "status.host", "status.host.online", false},

		// Edge cases
		{"empty pattern", "", "", true},
		{"empty key with pattern", "status", "", false},
		{"empty pattern with key", "", "status", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchRoutingKey(tt.pattern, tt.routingKey)
			if result != tt.expected {
				t.Errorf("matchRoutingKey(%q, %q) = %v, want %v",
					tt.pattern, tt.routingKey, result, tt.expected)
			}
		})
	}
}

func TestMatchRoutingKeyPatterns(t *testing.T) {
	// Test common ManMan patterns
	patterns := map[string][]string{
		"status.host.#": {
			"status.host.online",
			"status.host.offline",
			"status.host.extra.data",
		},
		"status.session.#": {
			"status.session.pending",
			"status.session.running",
			"status.session.stopped",
		},
		"health.#": {
			"health",
			"health.heartbeat",
			"health.metrics.cpu",
		},
	}

	for pattern, keys := range patterns {
		for _, key := range keys {
			if !matchRoutingKey(pattern, key) {
				t.Errorf("Pattern %q should match key %q", pattern, key)
			}
		}
	}

	// Test non-matches
	nonMatches := map[string][]string{
		"status.host.#": {
			"status.session.online",
			"health.host.online",
			"host.online",
		},
		"status.session.#": {
			"status.host.running",
			"session.running",
		},
	}

	for pattern, keys := range nonMatches {
		for _, key := range keys {
			if matchRoutingKey(pattern, key) {
				t.Errorf("Pattern %q should NOT match key %q", pattern, key)
			}
		}
	}
}
