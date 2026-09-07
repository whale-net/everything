package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/events"
	"github.com/whale-net/everything/manmanv2/host/rmq"
	"github.com/whale-net/everything/manmanv2/models"
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

// --- Fakes for the manmanv2.htmxsse live-publish tests below ---

// fakeSessionRepository implements repository.SessionRepository in-memory,
// with an optional injected error for the update paths so DB-failure
// scenarios can be exercised without a real database.
type fakeSessionRepository struct {
	sessions      map[int64]*manman.Session
	staleSessions []*manman.Session
	updateErr     error
}

func newFakeSessionRepository() *fakeSessionRepository {
	return &fakeSessionRepository{sessions: make(map[int64]*manman.Session)}
}

func (f *fakeSessionRepository) Create(ctx context.Context, session *manman.Session) (*manman.Session, error) {
	f.sessions[session.SessionID] = session
	return session, nil
}

func (f *fakeSessionRepository) Get(ctx context.Context, sessionID int64) (*manman.Session, error) {
	s, ok := f.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	return s, nil
}

func (f *fakeSessionRepository) List(ctx context.Context, sgcID *int64, limit, offset int) ([]*manman.Session, error) {
	return nil, nil
}

func (f *fakeSessionRepository) ListWithFilters(ctx context.Context, filters *repository.SessionFilters, limit, offset int) ([]*manman.Session, error) {
	return nil, nil
}

func (f *fakeSessionRepository) Update(ctx context.Context, session *manman.Session) error {
	f.sessions[session.SessionID] = session
	return nil
}

func (f *fakeSessionRepository) UpdateStatus(ctx context.Context, sessionID int64, status string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	s, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}
	s.Status = status
	return nil
}

func (f *fakeSessionRepository) UpdateSessionStart(ctx context.Context, sessionID int64, startedAt time.Time) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	s, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}
	s.Status = manman.SessionStatusRunning
	s.StartedAt = &startedAt
	return nil
}

func (f *fakeSessionRepository) UpdateSessionEnd(ctx context.Context, sessionID int64, status string, endedAt time.Time, exitCode *int) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	s, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}
	s.Status = status
	s.EndedAt = &endedAt
	s.ExitCode = exitCode
	return nil
}

func (f *fakeSessionRepository) GetStaleSessions(ctx context.Context, threshold time.Duration) ([]*manman.Session, error) {
	return f.staleSessions, nil
}

func (f *fakeSessionRepository) StopOtherSessionsForSGC(ctx context.Context, sessionID int64, sgcID int64) error {
	return nil
}

// fakeServerPortRepository implements repository.ServerPortRepository,
// recording DeallocatePortsBySessionID calls (the only method Handle
// exercises for terminal transitions); everything else is a no-op stub.
type fakeServerPortRepository struct {
	deallocateCalls []int64
}

func (f *fakeServerPortRepository) AllocatePort(ctx context.Context, serverID int64, port int, protocol string, sessionID int64) error {
	return nil
}

func (f *fakeServerPortRepository) DeallocatePort(ctx context.Context, serverID int64, port int, protocol string) error {
	return nil
}

func (f *fakeServerPortRepository) IsPortAvailable(ctx context.Context, serverID int64, port int, protocol string) (bool, error) {
	return true, nil
}

func (f *fakeServerPortRepository) GetPortAllocation(ctx context.Context, serverID int64, port int, protocol string) (*manman.ServerPort, error) {
	return nil, nil
}

func (f *fakeServerPortRepository) ListAllocatedPorts(ctx context.Context, serverID int64) ([]*manman.ServerPort, error) {
	return nil, nil
}

func (f *fakeServerPortRepository) ListPortsBySessionID(ctx context.Context, sessionID int64) ([]*manman.ServerPort, error) {
	return nil, nil
}

func (f *fakeServerPortRepository) DeallocatePortsBySessionID(ctx context.Context, sessionID int64) error {
	f.deallocateCalls = append(f.deallocateCalls, sessionID)
	return nil
}

func (f *fakeServerPortRepository) AllocateMultiplePorts(ctx context.Context, serverID int64, portBindings []*manman.PortBinding, sessionID int64) error {
	return nil
}

func (f *fakeServerPortRepository) GetAvailablePortsInRange(ctx context.Context, serverID int64, protocol string, startPort, endPort, limit int) ([]int, error) {
	return nil, nil
}

// recordedPublish captures a single Publish{External,Live} call.
type recordedPublish struct {
	RoutingKey string
	Message    interface{}
}

// fakePublisher implements Publisher, recording calls to both exchanges
// separately so tests can assert on external and live publishes independently.
type fakePublisher struct {
	external []recordedPublish
	live     []recordedPublish
	liveErr  error
}

func (f *fakePublisher) PublishExternal(ctx context.Context, routingKey string, message interface{}) error {
	f.external = append(f.external, recordedPublish{RoutingKey: routingKey, Message: message})
	return nil
}

func (f *fakePublisher) PublishLive(ctx context.Context, routingKey string, message interface{}) error {
	f.live = append(f.live, recordedPublish{RoutingKey: routingKey, Message: message})
	return f.liveErr
}

func newTestHandler(sessionRepo repository.SessionRepository, portRepo repository.ServerPortRepository, pub Publisher) *SessionStatusHandler {
	repo := &repository.Repository{
		Sessions:    sessionRepo,
		ServerPorts: portRepo,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewSessionStatusHandler(repo, pub, logger)
}

// TestHandlePublishesLiveForNonTerminalTransitions covers the case the
// existing external-publish filter drops: pending/starting/stopping
// transitions must still reach the live exchange (FR2 full fidelity).
func TestHandlePublishesLiveForNonTerminalTransitions(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
	}{
		{"pending to starting", manman.SessionStatusPending, manman.SessionStatusStarting},
		{"starting to running", manman.SessionStatusStarting, manman.SessionStatusRunning},
		{"running to stopping", manman.SessionStatusRunning, manman.SessionStatusStopping},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionRepo := newFakeSessionRepository()
			sessionRepo.sessions[1] = &manman.Session{SessionID: 1, SGCID: 42, Status: tc.from}
			pub := &fakePublisher{}
			h := newTestHandler(sessionRepo, &fakeServerPortRepository{}, pub)

			msg := rmq.SessionStatusUpdate{SessionID: 1, SGCID: 42, Status: tc.to}
			body, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("failed to marshal message: %v", err)
			}

			if err := h.Handle(context.Background(), "status.session."+tc.to, body); err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}

			if len(pub.live) != 1 {
				t.Fatalf("expected exactly 1 PublishLive call, got %d", len(pub.live))
			}
			if want := "deployment.42"; pub.live[0].RoutingKey != want {
				t.Errorf("expected live routing key %q, got %q", want, pub.live[0].RoutingKey)
			}
		})
	}
}

// TestHandlePublishesLiveAndExternalForTerminalTransitions verifies terminal
// transitions still get both the existing external publish (routing key and
// behavior unchanged) and the new live publish.
func TestHandlePublishesLiveAndExternalForTerminalTransitions(t *testing.T) {
	cases := []struct {
		name string
		from string
		to   string
	}{
		{"stopping to stopped", manman.SessionStatusStopping, manman.SessionStatusStopped},
		{"running to crashed", manman.SessionStatusRunning, manman.SessionStatusCrashed},
		{"starting to lost", manman.SessionStatusStarting, manman.SessionStatusLost},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessionRepo := newFakeSessionRepository()
			sessionRepo.sessions[1] = &manman.Session{SessionID: 1, SGCID: 7, Status: tc.from}
			pub := &fakePublisher{}
			h := newTestHandler(sessionRepo, &fakeServerPortRepository{}, pub)

			msg := rmq.SessionStatusUpdate{SessionID: 1, SGCID: 7, Status: tc.to}
			body, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("failed to marshal message: %v", err)
			}

			if err := h.Handle(context.Background(), "status.session."+tc.to, body); err != nil {
				t.Fatalf("Handle returned error: %v", err)
			}

			if len(pub.external) != 1 {
				t.Fatalf("expected exactly 1 PublishExternal call, got %d", len(pub.external))
			}
			wantExternalKey := fmt.Sprintf("manman.session.%s", tc.to)
			if pub.external[0].RoutingKey != wantExternalKey {
				t.Errorf("expected unchanged external routing key %q, got %q", wantExternalKey, pub.external[0].RoutingKey)
			}

			if len(pub.live) != 1 {
				t.Fatalf("expected exactly 1 PublishLive call, got %d", len(pub.live))
			}
			if want := "deployment.7"; pub.live[0].RoutingKey != want {
				t.Errorf("expected live routing key %q, got %q", want, pub.live[0].RoutingKey)
			}
		})
	}
}

// TestHandleLiveRoutingKeyIsSGCKeyed verifies the live routing key is built
// from the message's SGCID, not its SessionID.
func TestHandleLiveRoutingKeyIsSGCKeyed(t *testing.T) {
	sessionRepo := newFakeSessionRepository()
	sessionRepo.sessions[999] = &manman.Session{SessionID: 999, SGCID: 42, Status: manman.SessionStatusPending}
	pub := &fakePublisher{}
	h := newTestHandler(sessionRepo, &fakeServerPortRepository{}, pub)

	msg := rmq.SessionStatusUpdate{SessionID: 999, SGCID: 42, Status: manman.SessionStatusStarting}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	if err := h.Handle(context.Background(), "status.session.starting", body); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}

	if len(pub.live) != 1 {
		t.Fatalf("expected exactly 1 PublishLive call, got %d", len(pub.live))
	}
	if want := events.TopicForDeployment(42); pub.live[0].RoutingKey != want {
		t.Errorf("expected routing key %q, got %q", want, pub.live[0].RoutingKey)
	}
	if pub.live[0].RoutingKey != "deployment.42" {
		t.Errorf("routing key is not SGC-keyed: got %q", pub.live[0].RoutingKey)
	}
}

// TestHandleNoLivePublishOnInvalidTransition verifies a transition rejected
// by isValidTransition produces zero PublishLive (and zero PublishExternal)
// calls.
func TestHandleNoLivePublishOnInvalidTransition(t *testing.T) {
	sessionRepo := newFakeSessionRepository()
	sessionRepo.sessions[1] = &manman.Session{SessionID: 1, SGCID: 5, Status: manman.SessionStatusPending}
	pub := &fakePublisher{}
	h := newTestHandler(sessionRepo, &fakeServerPortRepository{}, pub)

	// pending -> running is not a valid direct transition.
	msg := rmq.SessionStatusUpdate{SessionID: 1, SGCID: 5, Status: manman.SessionStatusRunning}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	if err := h.Handle(context.Background(), "status.session.running", body); err == nil {
		t.Fatal("expected an error for an invalid transition, got nil")
	}

	if len(pub.live) != 0 {
		t.Errorf("expected 0 PublishLive calls on invalid transition, got %d", len(pub.live))
	}
	if len(pub.external) != 0 {
		t.Errorf("expected 0 PublishExternal calls on invalid transition, got %d", len(pub.external))
	}
}

// TestHandleNoLivePublishOnDBUpdateFailure verifies a failed persistence
// call short-circuits before either publish fires.
func TestHandleNoLivePublishOnDBUpdateFailure(t *testing.T) {
	sessionRepo := newFakeSessionRepository()
	sessionRepo.sessions[1] = &manman.Session{SessionID: 1, SGCID: 5, Status: manman.SessionStatusPending}
	sessionRepo.updateErr = errors.New("db unavailable")
	pub := &fakePublisher{}
	h := newTestHandler(sessionRepo, &fakeServerPortRepository{}, pub)

	msg := rmq.SessionStatusUpdate{SessionID: 1, SGCID: 5, Status: manman.SessionStatusStarting}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	if err := h.Handle(context.Background(), "status.session.starting", body); err == nil {
		t.Fatal("expected an error from the DB update failure, got nil")
	}

	if len(pub.live) != 0 {
		t.Errorf("expected 0 PublishLive calls on DB update failure, got %d", len(pub.live))
	}
	if len(pub.external) != 0 {
		t.Errorf("expected 0 PublishExternal calls on DB update failure, got %d", len(pub.external))
	}
}

// TestHandleSurvivesPublishLiveError verifies a PublishLive error is logged
// and swallowed: Handle still returns nil, and the DB update it followed was
// not skipped or rolled back.
func TestHandleSurvivesPublishLiveError(t *testing.T) {
	sessionRepo := newFakeSessionRepository()
	sessionRepo.sessions[1] = &manman.Session{SessionID: 1, SGCID: 5, Status: manman.SessionStatusPending}
	pub := &fakePublisher{liveErr: errors.New("amqp channel closed")}
	h := newTestHandler(sessionRepo, &fakeServerPortRepository{}, pub)

	msg := rmq.SessionStatusUpdate{SessionID: 1, SGCID: 5, Status: manman.SessionStatusStarting}
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	if err := h.Handle(context.Background(), "status.session.starting", body); err != nil {
		t.Fatalf("expected Handle to succeed despite PublishLive error, got %v", err)
	}

	if len(pub.live) != 1 {
		t.Fatalf("expected PublishLive to have been attempted once, got %d", len(pub.live))
	}

	updated, err := sessionRepo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("failed to fetch session: %v", err)
	}
	if updated.Status != manman.SessionStatusStarting {
		t.Errorf("expected DB update to have applied despite publish error, got status %q", updated.Status)
	}
}

// TestCheckStaleSessionsPublishesLive covers the stale-session (server-side
// crash detection) call site: a Lost transition detected by the background
// checker must reach the live exchange, not just the external one.
func TestCheckStaleSessionsPublishesLive(t *testing.T) {
	sessionRepo := newFakeSessionRepository()
	staleSession := &manman.Session{
		SessionID: 3,
		SGCID:     9,
		Status:    manman.SessionStatusStarting,
		UpdatedAt: time.Now().Add(-time.Hour),
	}
	sessionRepo.sessions[3] = staleSession
	sessionRepo.staleSessions = []*manman.Session{staleSession}
	pub := &fakePublisher{}
	h := newTestHandler(sessionRepo, &fakeServerPortRepository{}, pub)

	if err := h.checkStaleSessions(context.Background(), 5*time.Minute); err != nil {
		t.Fatalf("checkStaleSessions returned error: %v", err)
	}

	if len(pub.live) != 1 {
		t.Fatalf("expected exactly 1 PublishLive call for the stale session, got %d", len(pub.live))
	}
	if want := "deployment.9"; pub.live[0].RoutingKey != want {
		t.Errorf("expected live routing key %q, got %q", want, pub.live[0].RoutingKey)
	}

	if len(pub.external) != 1 {
		t.Errorf("expected exactly 1 PublishExternal call for the stale session, got %d", len(pub.external))
	}
}
