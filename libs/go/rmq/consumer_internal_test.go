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
