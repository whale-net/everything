package manman

import (
	"testing"
)

func TestSession_IsActive(t *testing.T) {
	tests := []struct {
		status   string
		expected bool
	}{
		{SessionStatusPending, true},
		{SessionStatusStarting, true},
		{SessionStatusRunning, true},
		{SessionStatusStopping, true},
		{SessionStatusCrashed, true},
		{SessionStatusLost, true},
		{SessionStatusStopped, false},
		{SessionStatusCompleted, false},
		{"unknown", false},
	}

	for _, tt := range tests {
		s := Session{Status: tt.status}
		if s.IsActive() != tt.expected {
			t.Errorf("IsActive() for status %s = %v, want %v", tt.status, s.IsActive(), tt.expected)
		}
	}
}

func TestSession_IsAvailable(t *testing.T) {
	tests := []struct {
		status   string
		expected bool
	}{
		{SessionStatusRunning, true},
		{SessionStatusPending, false},
		{SessionStatusStarting, false},
		{SessionStatusStopping, false},
		{SessionStatusCrashed, false},
		{SessionStatusLost, false},
		{SessionStatusStopped, false},
	}

	for _, tt := range tests {
		s := Session{Status: tt.status}
		if s.IsAvailable() != tt.expected {
			t.Errorf("IsAvailable() for status %s = %v, want %v", tt.status, s.IsAvailable(), tt.expected)
		}
	}
}
