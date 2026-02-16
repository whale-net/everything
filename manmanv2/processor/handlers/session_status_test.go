package handlers

import (
	"testing"

	"github.com/whale-net/everything/manman"
)

func TestIsValidTransition(t *testing.T) {
	tests := []struct {
		name     string
		from     string
		to       string
		expected bool
	}{
		// Valid transitions
		{"pending to starting", manman.SessionStatusPending, manman.SessionStatusStarting, true},
		{"starting to running", manman.SessionStatusStarting, manman.SessionStatusRunning, true},
		{"running to stopping", manman.SessionStatusRunning, manman.SessionStatusStopping, true},
		{"stopping to stopped", manman.SessionStatusStopping, manman.SessionStatusStopped, true},

		// Lost from any non-terminal state
		{"pending to lost", manman.SessionStatusPending, manman.SessionStatusLost, true},
		{"starting to lost", manman.SessionStatusStarting, manman.SessionStatusLost, true},
		{"running to lost", manman.SessionStatusRunning, manman.SessionStatusLost, true},
		{"stopping to lost", manman.SessionStatusStopping, manman.SessionStatusLost, true},

		// Crash from any non-terminal state
		{"pending to crashed", manman.SessionStatusPending, manman.SessionStatusCrashed, true},
		{"starting to crashed", manman.SessionStatusStarting, manman.SessionStatusCrashed, true},
		{"running to crashed", manman.SessionStatusRunning, manman.SessionStatusCrashed, true},
		{"stopping to crashed", manman.SessionStatusStopping, manman.SessionStatusCrashed, true},

		// Idempotent (same state)
		{"pending to pending", manman.SessionStatusPending, manman.SessionStatusPending, true},
		{"running to running", manman.SessionStatusRunning, manman.SessionStatusRunning, true},
		{"stopped to stopped", manman.SessionStatusStopped, manman.SessionStatusStopped, true},

		// Invalid transitions
		{"pending to running", manman.SessionStatusPending, manman.SessionStatusRunning, false},
		{"pending to stopped", manman.SessionStatusPending, manman.SessionStatusStopped, false},
		{"starting to stopping", manman.SessionStatusStarting, manman.SessionStatusStopping, false},
		{"running to starting", manman.SessionStatusRunning, manman.SessionStatusStarting, false},
		{"stopped to running", manman.SessionStatusStopped, manman.SessionStatusRunning, false},
		{"stopped to starting", manman.SessionStatusStopped, manman.SessionStatusStarting, false},
		{"crashed to running", manman.SessionStatusCrashed, manman.SessionStatusRunning, false},
		{"crashed to starting", manman.SessionStatusCrashed, manman.SessionStatusStarting, false},

		// Invalid from unknown state
		{"unknown to running", "unknown", manman.SessionStatusRunning, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidTransition(tt.from, tt.to)
			if result != tt.expected {
				t.Errorf("isValidTransition(%q, %q) = %v, want %v",
					tt.from, tt.to, result, tt.expected)
			}
		})
	}
}

func TestSessionStateTransitionPaths(t *testing.T) {
	// Test complete valid paths
	validPaths := [][]string{
		// Normal lifecycle
		{
			manman.SessionStatusPending,
			manman.SessionStatusStarting,
			manman.SessionStatusRunning,
			manman.SessionStatusStopping,
			manman.SessionStatusStopped,
		},
		// Crash during starting
		{
			manman.SessionStatusPending,
			manman.SessionStatusStarting,
			manman.SessionStatusCrashed,
		},
		// Crash while running
		{
			manman.SessionStatusPending,
			manman.SessionStatusStarting,
			manman.SessionStatusRunning,
			manman.SessionStatusCrashed,
		},
	}

	for i, path := range validPaths {
		for j := 0; j < len(path)-1; j++ {
			from := path[j]
			to := path[j+1]
			if !isValidTransition(from, to) {
				t.Errorf("Path %d: transition %q -> %q should be valid", i, from, to)
			}
		}
	}
}
