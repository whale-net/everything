package handlers

import (
	"errors"
	"fmt"
	"testing"
)

func TestPermanentError(t *testing.T) {
	baseErr := errors.New("entity not found")
	permErr := &PermanentError{Err: baseErr}

	// Test Error() method
	if permErr.Error() != "entity not found" {
		t.Errorf("PermanentError.Error() = %q, want %q", permErr.Error(), "entity not found")
	}

	// Test Unwrap() method
	unwrapped := permErr.Unwrap()
	if unwrapped != baseErr {
		t.Errorf("PermanentError.Unwrap() = %v, want %v", unwrapped, baseErr)
	}
}

func TestIsPermanentError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "permanent error",
			err:      &PermanentError{Err: errors.New("test")},
			expected: true,
		},
		{
			name:     "regular error",
			err:      errors.New("test"),
			expected: false,
		},
		{
			name:     "wrapped permanent error",
			err:      fmt.Errorf("wrapped: %w", &PermanentError{Err: errors.New("test")}),
			expected: false, // IsPermanentError only checks direct type, not unwrapped
		},
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPermanentError(tt.err)
			if result != tt.expected {
				t.Errorf("IsPermanentError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}
