package pagetoken

import (
	"strings"
	"testing"
)

// Test that tokens encode to opaque base64 strings that don't leak information
func TestEncode_IsOpaque(t *testing.T) {
	token := &Token{
		LastRecordedAt: 1234567890,
		LastBoardID:    42,
	}

	encoded, err := Encode(token)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// Token should be base64 encoded, not readable
	if strings.Contains(encoded, "1234567890") || strings.Contains(encoded, "42") {
		t.Errorf("token leaks plain values: %q", encoded)
	}

	// Should be valid base64
	if !isValidBase64(encoded) {
		t.Errorf("token is not valid base64: %q", encoded)
	}
}

// Test that nil tokens encode to empty string
func TestEncode_NilToken(t *testing.T) {
	encoded, err := Encode(nil)
	if err != nil {
		t.Fatalf("Encode(nil) failed: %v", err)
	}
	if encoded != "" {
		t.Errorf("expected empty string for nil token, got %q", encoded)
	}
}

// Test that empty tokens encode to base64 of empty JSON object
func TestEncode_EmptyToken(t *testing.T) {
	encoded, err := Encode(&Token{})
	if err != nil {
		t.Fatalf("Encode empty token failed: %v", err)
	}
	if encoded == "" {
		t.Errorf("expected non-empty string for empty token")
	}
}

// Test that valid tokens round-trip correctly
func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		token *Token
	}{
		{
			name:  "nil token",
			token: nil,
		},
		{
			name:  "empty token",
			token: &Token{},
		},
		{
			name: "token with values",
			token: &Token{
				LastRecordedAt: 1234567890,
				LastBoardID:    42,
			},
		},
		{
			name: "token with large values",
			token: &Token{
				LastRecordedAt: 9223372036854775807, // max int64
				LastBoardID:    9223372036854775806,
			},
		},
		{
			name: "token with zero values",
			token: &Token{
				LastRecordedAt: 0,
				LastBoardID:    0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Encode
			encoded, err := Encode(tt.token)
			if err != nil {
				t.Fatalf("Encode failed: %v", err)
			}

			// Decode
			decoded, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode failed: %v", err)
			}

			// Compare
			if tt.token == nil && decoded != nil {
				t.Fatalf("expected nil, got %v", decoded)
			}
			if tt.token != nil && decoded == nil {
				t.Fatalf("expected non-nil, got nil")
			}
			if tt.token != nil && decoded != nil {
				if tt.token.LastRecordedAt != decoded.LastRecordedAt {
					t.Errorf("LastRecordedAt mismatch: %d != %d", tt.token.LastRecordedAt, decoded.LastRecordedAt)
				}
				if tt.token.LastBoardID != decoded.LastBoardID {
					t.Errorf("LastBoardID mismatch: %d != %d", tt.token.LastBoardID, decoded.LastBoardID)
				}
			}
		})
	}
}

// Test that mutated tokens are rejected
func TestDecode_MutatedTokenIsRejected(t *testing.T) {
	token := &Token{
		LastRecordedAt: 1234567890,
		LastBoardID:    42,
	}

	// Encode the token
	encoded, err := Encode(token)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// Mutate the token by changing a character in the base64
	if len(encoded) > 0 {
		chars := []byte(encoded)
		if chars[0] == 'A' {
			chars[0] = 'B'
		} else {
			chars[0] = 'A'
		}
		mutated := string(chars)

		// Try to decode the mutated token
		_, err := Decode(mutated)
		if err == nil {
			t.Errorf("expected error decoding mutated token, got nil")
		}

		// Verify error message doesn't leak token contents
		if strings.Contains(err.Error(), "1234567890") || strings.Contains(err.Error(), "42") {
			t.Errorf("error message leaked token contents: %q", err.Error())
		}
	}
}

// Test that invalid base64 is rejected
func TestDecode_InvalidBase64(t *testing.T) {
	tests := []string{
		"not-base64!@#$",
		"!!!invalid!!!",
		"../../etc/passwd",
	}

	for _, encoded := range tests {
		t.Run(encoded, func(t *testing.T) {
			_, err := Decode(encoded)
			if err == nil {
				t.Errorf("expected error for invalid base64 %q, got nil", encoded)
			}

			// Error should be generic and not leak the input
			if strings.Contains(err.Error(), encoded) {
				t.Errorf("error message leaked input: %q", err.Error())
			}
		})
	}
}

// Test that empty string decodes to nil
func TestDecode_EmptyString(t *testing.T) {
	decoded, err := Decode("")
	if err != nil {
		t.Fatalf("Decode empty string failed: %v", err)
	}
	if decoded != nil {
		t.Errorf("expected nil for empty string, got %v", decoded)
	}
}

// Test that a token with only JSON-like structure but missing required fields still works
func TestDecode_MissingFields(t *testing.T) {
	// Create a token with default values (omitted fields)
	token := &Token{
		LastRecordedAt: 0,
		LastBoardID:    0,
	}

	// Encode and decode
	encoded, err := Encode(token)
	if err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if decoded == nil {
		t.Fatalf("expected non-nil token")
	}
	if decoded.LastRecordedAt != 0 || decoded.LastBoardID != 0 {
		t.Errorf("expected zero values, got %v", decoded)
	}
}

// Helper to check if a string is valid base64
func isValidBase64(s string) bool {
	if s == "" {
		return true
	}
	// Very basic check — proper validation would use base64.StdEncoding.DecodeString
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=') {
			return false
		}
	}
	return true
}
