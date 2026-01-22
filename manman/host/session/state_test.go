package session_test

import (
	"testing"

	"github.com/whale-net/everything/manman"
	"github.com/whale-net/everything/manman/host/session"
)

func TestManager_AddGetRemoveSession(t *testing.T) {
	manager := session.NewManager()

	state := &session.State{
		SessionID: 1,
		SGCID:     100,
		Status:    manman.SessionStatusPending,
	}

	// Add session
	manager.AddSession(state)

	// Get session
	retrieved, ok := manager.GetSession(1)
	if !ok {
		t.Fatal("Session not found after adding")
	}
	if retrieved.SessionID != 1 {
		t.Errorf("Expected session ID 1, got %d", retrieved.SessionID)
	}

	// Remove session
	manager.RemoveSession(1)

	// Verify removed
	_, ok = manager.GetSession(1)
	if ok {
		t.Error("Session still exists after removal")
	}
}

func TestManager_ListSessions(t *testing.T) {
	manager := session.NewManager()

	// Add multiple sessions
	for i := int64(1); i <= 5; i++ {
		state := &session.State{
			SessionID: i,
			SGCID:     100 + i,
			Status:    manman.SessionStatusPending,
		}
		manager.AddSession(state)
	}

	// List all sessions
	sessions := manager.ListSessions()
	if len(sessions) != 5 {
		t.Errorf("Expected 5 sessions, got %d", len(sessions))
	}

	// Verify all sessions are present
	sessionMap := make(map[int64]bool)
	for _, s := range sessions {
		sessionMap[s.SessionID] = true
	}

	for i := int64(1); i <= 5; i++ {
		if !sessionMap[i] {
			t.Errorf("Session %d not found in list", i)
		}
	}
}

func TestManager_ConcurrentAccess(t *testing.T) {
	manager := session.NewManager()

	// Test concurrent access
	done := make(chan bool, 10)
	for i := int64(1); i <= 10; i++ {
		go func(id int64) {
			state := &session.State{
				SessionID: id,
				SGCID:     100 + id,
				Status:    manman.SessionStatusPending,
			}
			manager.AddSession(state)
			_, ok := manager.GetSession(id)
			if !ok {
				t.Errorf("Session %d not found after concurrent add", id)
			}
			manager.RemoveSession(id)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify all sessions are removed
	sessions := manager.ListSessions()
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions after concurrent removal, got %d", len(sessions))
	}
}

func TestState_UpdateStatus(t *testing.T) {
	state := &session.State{
		SessionID: 1,
		Status:    manman.SessionStatusPending,
	}

	state.UpdateStatus(manman.SessionStatusRunning)
	if state.GetStatus() != manman.SessionStatusRunning {
		t.Errorf("Expected status %s, got %s", manman.SessionStatusRunning, state.GetStatus())
	}

	state.UpdateStatus(manman.SessionStatusStopped)
	if state.GetStatus() != manman.SessionStatusStopped {
		t.Errorf("Expected status %s, got %s", manman.SessionStatusStopped, state.GetStatus())
	}
}

func TestState_ConcurrentStatusUpdate(t *testing.T) {
	state := &session.State{
		SessionID: 1,
		Status:    manman.SessionStatusPending,
	}

	// Test concurrent status updates
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			state.UpdateStatus(manman.SessionStatusRunning)
			_ = state.GetStatus()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Final status should be one of the valid statuses
	finalStatus := state.GetStatus()
	validStatuses := map[string]bool{
		manman.SessionStatusPending:  true,
		manman.SessionStatusStarting: true,
		manman.SessionStatusRunning:  true,
		manman.SessionStatusStopping: true,
		manman.SessionStatusStopped:  true,
		manman.SessionStatusCrashed:  true,
	}

	if !validStatuses[finalStatus] {
		t.Errorf("Final status %s is not a valid status", finalStatus)
	}
}
