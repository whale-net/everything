package handlers

import (
	"encoding/json"
	"testing"

	"github.com/whale-net/everything/manmanv2"
)

func TestJsonbToStringArray(t *testing.T) {
	tests := []struct {
		name     string
		input    manman.JSONB
		expected []string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty items",
			input:    manman.JSONB{"items": []interface{}{}},
			expected: []string{},
		},
		{
			name: "valid string array",
			input: manman.JSONB{"items": []interface{}{
				"arg1", "arg2", "arg3",
			}},
			expected: []string{"arg1", "arg2", "arg3"},
		},
		{
			name: "from database JSON unmarshal",
			input: func() manman.JSONB {
				// Simulate what happens when JSONB is unmarshaled from database
				jsonStr := `{"items": ["+net_public_adr", "135.148.136.84", "+hostport", "38515"]}`
				var result manman.JSONB
				json.Unmarshal([]byte(jsonStr), &result)
				return result
			}(),
			expected: []string{"+net_public_adr", "135.148.136.84", "+hostport", "38515"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := jsonbToStringArray(tt.input)
			
			if len(result) != len(tt.expected) {
				t.Errorf("length mismatch: got %d, want %d", len(result), len(tt.expected))
				return
			}
			
			for i := range result {
				if result[i] != tt.expected[i] {
					t.Errorf("element %d: got %q, want %q", i, result[i], tt.expected[i])
				}
			}
		})
	}
}
