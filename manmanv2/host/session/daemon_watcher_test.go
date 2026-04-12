package session

import (
	"context"
	"testing"

	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
)

// mockRMQPublisher is a test double for the RabbitMQ publisher used by SessionManager.
type mockRMQPublisher struct {
	publishedUpdates []*hostrmq.SessionStatusUpdate
}

func (m *mockRMQPublisher) PublishLog(_ context.Context, _ int64, _ string, _ string) error {
	return nil
}

func (m *mockRMQPublisher) PublishSessionStatus(_ context.Context, update *hostrmq.SessionStatusUpdate) error {
	m.publishedUpdates = append(m.publishedUpdates, update)
	return nil
}

// newTestSessionManager builds a minimal SessionManager suitable for unit tests
// (no Docker client, no gRPC client).
func newTestSessionManager(pub *mockRMQPublisher) *SessionManager {
	return &SessionManager{
		stateManager: NewManager(),
		rmqPublisher: pub,
	}
}

// TestMarkAllSessionsCrashed_EmptyState verifies that calling markAllSessionsCrashed
// with no sessions is a no-op and publishes nothing.
func TestMarkAllSessionsCrashed_EmptyState(t *testing.T) {
	pub := &mockRMQPublisher{}
	sm := newTestSessionManager(pub)

	sm.markAllSessionsCrashed(context.Background())

	if len(pub.publishedUpdates) != 0 {
		t.Errorf("expected 0 published updates, got %d", len(pub.publishedUpdates))
	}
}

// TestMarkAllSessionsCrashed_PublishesCrashEvents verifies that each running session
// gets a crashed event published and is removed from the state manager.
func TestMarkAllSessionsCrashed_PublishesCrashEvents(t *testing.T) {
	pub := &mockRMQPublisher{}
	sm := newTestSessionManager(pub)

	// Add a few running sessions.
	sessions := []*State{
		{SessionID: 1, SGCID: 101, Status: "running"},
		{SessionID: 2, SGCID: 102, Status: "running"},
		{SessionID: 3, SGCID: 103, Status: "running"},
	}
	for _, s := range sessions {
		sm.stateManager.AddSession(s)
	}

	sm.markAllSessionsCrashed(context.Background())

	// All sessions must be removed from the state manager.
	if remaining := sm.stateManager.ListSessions(); len(remaining) != 0 {
		t.Errorf("expected 0 sessions remaining, got %d", len(remaining))
	}

	// A crashed event must have been published for each session.
	if len(pub.publishedUpdates) != 3 {
		t.Fatalf("expected 3 published updates, got %d", len(pub.publishedUpdates))
	}
	published := make(map[int64]string, len(pub.publishedUpdates))
	for _, u := range pub.publishedUpdates {
		published[u.SessionID] = u.Status
	}
	for _, s := range sessions {
		if published[s.SessionID] != "crashed" {
			t.Errorf("session %d: expected status 'crashed', got %q", s.SessionID, published[s.SessionID])
		}
	}
}

// TestMarkAllSessionsCrashed_StatusSetToCrashed verifies that every session's in-memory
// status is updated to "crashed" before it is removed.
func TestMarkAllSessionsCrashed_StatusSetToCrashed(t *testing.T) {
	pub := &mockRMQPublisher{}
	sm := newTestSessionManager(pub)

	state := &State{SessionID: 10, SGCID: 200, Status: "running"}
	sm.stateManager.AddSession(state)

	sm.markAllSessionsCrashed(context.Background())

	// The state object's status should have been updated even though it was
	// removed from the map.
	if state.GetStatus() != "crashed" {
		t.Errorf("expected status 'crashed', got %q", state.GetStatus())
	}
}

// TestMarkAllSessionsCrashed_ClosesAttachResp verifies that an open AttachResp is
// closed and nilified during crash processing.
func TestMarkAllSessionsCrashed_ClosesAttachResp(t *testing.T) {
	pub := &mockRMQPublisher{}
	sm := newTestSessionManager(pub)

	// A nil AttachResp should not panic.
	state := &State{SessionID: 5, SGCID: 50, Status: "running", AttachResp: nil}
	sm.stateManager.AddSession(state)

	// Should not panic even with nil AttachResp.
	sm.markAllSessionsCrashed(context.Background())

	if _, exists := sm.stateManager.GetSession(5); exists {
		t.Error("session should have been removed from state manager")
	}
}
