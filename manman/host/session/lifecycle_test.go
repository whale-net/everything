package session_test

import (
	"testing"

	"github.com/whale-net/everything/manman/host/session"
)

// Test: GetActiveSGCIDs returns unique SGC IDs
func TestGetActiveSGCIDs(t *testing.T) {
	manager := session.NewManager()

	// Add sessions with different SGC IDs
	manager.AddSession(&session.State{SessionID: 1, SGCID: 100})
	manager.AddSession(&session.State{SessionID: 2, SGCID: 101})
	manager.AddSession(&session.State{SessionID: 3, SGCID: 100}) // Duplicate SGC

	activeSGCs := manager.GetActiveSGCIDs()

	if len(activeSGCs) != 2 {
		t.Errorf("Expected 2 unique SGC IDs, got %d", len(activeSGCs))
	}

	if !activeSGCs[100] {
		t.Error("Expected SGC ID 100 to be active")
	}

	if !activeSGCs[101] {
		t.Error("Expected SGC ID 101 to be active")
	}
}

// Test: GetActiveSessionIDs returns all session IDs
func TestGetActiveSessionIDs(t *testing.T) {
	manager := session.NewManager()

	manager.AddSession(&session.State{SessionID: 1001})
	manager.AddSession(&session.State{SessionID: 1002})
	manager.AddSession(&session.State{SessionID: 1003})

	activeIDs := manager.GetActiveSessionIDs()

	if len(activeIDs) != 3 {
		t.Errorf("Expected 3 active session IDs, got %d", len(activeIDs))
	}

	for _, id := range []int64{1001, 1002, 1003} {
		if !activeIDs[id] {
			t.Errorf("Expected session ID %d to be active", id)
		}
	}
}

// Test: GetSessionStats aggregates correctly
func TestGetSessionStats(t *testing.T) {
	manager := session.NewManager()

	// Add sessions with different statuses
	manager.AddSession(&session.State{SessionID: 1, Status: "running"})
	manager.AddSession(&session.State{SessionID: 2, Status: "pending"})
	manager.AddSession(&session.State{SessionID: 3, Status: "crashed"})
	manager.AddSession(&session.State{SessionID: 4, Status: "running"})

	stats := manager.GetSessionStats()

	if stats.Total != 4 {
		t.Errorf("Expected 4 total sessions, got %d", stats.Total)
	}

	if stats.Running != 2 {
		t.Errorf("Expected 2 running sessions, got %d", stats.Running)
	}

	if stats.Pending != 1 {
		t.Errorf("Expected 1 pending session, got %d", stats.Pending)
	}

	if stats.Crashed != 1 {
		t.Errorf("Expected 1 crashed session, got %d", stats.Crashed)
	}
}

// Note: extractIDsFromLabels is not exported, so we can't test it directly from external test package
// These tests would need to be internal tests or we'd need to export the function for testing
