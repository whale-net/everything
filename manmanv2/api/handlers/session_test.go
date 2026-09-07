package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/models"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// liveSessionStatuses mirrors the LiveOnly predicate the postgres
// SessionRepository implementation applies (manmanv2/api/repository/postgres/session.go),
// so MockSessionRepo.ListWithFilters exercises the same live/terminal split
// RestartDeployment's getLiveSessionForSGC relies on.
var liveSessionStatuses = map[string]bool{
	manman.SessionStatusPending:  true,
	manman.SessionStatusStarting: true,
	manman.SessionStatusRunning:  true,
	manman.SessionStatusStopping: true,
}

// MockSessionRepo for handler tests
type MockSessionRepo struct {
	repository.SessionRepository
	sessions []*manman.Session
	created  []*manman.Session
	updated  []*manman.Session
	// updateErrForStatus, when non-nil, is returned by Update whenever the
	// session being updated has this status -- used to simulate StopSession's
	// dispatch failing partway through (the status-to-'stopping' Update).
	updateErrForStatus string
	updateErr          error
	// opLog, when non-nil, records this mock's tracked calls in invocation
	// order, shared with MockPendingRestartRepo so tests can assert
	// record-then-dispatch ordering explicitly rather than just presence.
	opLog *[]string
}

func (m *MockSessionRepo) ListWithFilters(ctx context.Context, filters *repository.SessionFilters, limit, offset int) ([]*manman.Session, error) {
	var result []*manman.Session
	for _, s := range m.sessions {
		if filters.SGCID != nil && s.SGCID != *filters.SGCID {
			continue
		}
		if filters.LiveOnly && !liveSessionStatuses[s.Status] {
			continue
		}
		if len(filters.StatusFilter) > 0 {
			match := false
			for _, st := range filters.StatusFilter {
				if s.Status == st {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		result = append(result, s)
	}
	return result, nil
}

func (m *MockSessionRepo) Get(ctx context.Context, sessionID int64) (*manman.Session, error) {
	for _, s := range m.sessions {
		if s.SessionID == sessionID {
			return s, nil
		}
	}
	return nil, errors.New("session not found")
}

func (m *MockSessionRepo) Create(ctx context.Context, s *manman.Session) (*manman.Session, error) {
	s.SessionID = int64(len(m.sessions) + 1)
	m.sessions = append(m.sessions, s)
	m.created = append(m.created, s)
	return s, nil
}

func (m *MockSessionRepo) Update(ctx context.Context, s *manman.Session) error {
	if m.updateErrForStatus != "" && s.Status == m.updateErrForStatus {
		return m.updateErr
	}
	m.updated = append(m.updated, s)
	if m.opLog != nil {
		*m.opLog = append(*m.opLog, "session.Update:"+s.Status)
	}
	for i, existing := range m.sessions {
		if existing.SessionID == s.SessionID {
			m.sessions[i] = s
		}
	}
	return nil
}

// opLogEntries returns a snapshot of the shared op log, or nil if this mock
// wasn't given one.
func (m *MockSessionRepo) opLogEntries() []string {
	if m.opLog == nil {
		return nil
	}
	return *m.opLog
}

func (m *MockSessionRepo) StopOtherSessionsForSGC(ctx context.Context, sessionID int64, sgcID int64) error {
	for _, s := range m.sessions {
		if s.SGCID == sgcID && s.SessionID != sessionID {
			s.Status = manman.SessionStatusStopped
		}
	}
	return nil
}

// MockSGCRepo
type MockSGCRepo struct {
	repository.ServerGameConfigRepository
	// getErr, when non-nil, is returned by Get for every id -- used to
	// simulate an unknown/invalid server_game_config_id.
	getErr error
}

func (m *MockSGCRepo) Get(ctx context.Context, id int64) (*manman.ServerGameConfig, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return &manman.ServerGameConfig{SGCID: id, ServerID: 1, GameConfigID: 1}, nil
}

// MockPendingRestartRepo for RestartDeployment tests.
type MockPendingRestartRepo struct {
	repository.PendingRestartRepository
	createErr       error
	createCalls     []pendingRestartCreateCall
	markFailedCalls []pendingRestartMarkFailedCall
	nextID          int64
	// opLog, when non-nil, records this mock's tracked calls in invocation
	// order, shared with MockSessionRepo (see above).
	opLog *[]string
}

type pendingRestartCreateCall struct {
	sgcID           int64
	gatingSessionID int64
	stallDeadline   time.Time
}

type pendingRestartMarkFailedCall struct {
	pendingRestartID int64
	reason           string
}

func (m *MockPendingRestartRepo) Create(ctx context.Context, sgcID, gatingSessionID int64, stallDeadline time.Time) (*manman.PendingRestart, error) {
	m.createCalls = append(m.createCalls, pendingRestartCreateCall{sgcID: sgcID, gatingSessionID: gatingSessionID, stallDeadline: stallDeadline})
	if m.opLog != nil {
		*m.opLog = append(*m.opLog, "pendingRestart.Create")
	}
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.nextID++
	return &manman.PendingRestart{
		PendingRestartID:   m.nextID,
		ServerGameConfigID: sgcID,
		GatingSessionID:    gatingSessionID,
		Status:             "pending",
		StallDeadline:      stallDeadline,
	}, nil
}

func (m *MockPendingRestartRepo) MarkFailed(ctx context.Context, pendingRestartID int64, reason string) error {
	m.markFailedCalls = append(m.markFailedCalls, pendingRestartMarkFailedCall{pendingRestartID: pendingRestartID, reason: reason})
	if m.opLog != nil {
		*m.opLog = append(*m.opLog, "pendingRestart.MarkFailed")
	}
	return nil
}

// MockGCRepo
type MockGCRepo struct {
	repository.GameConfigRepository
}

func (m *MockGCRepo) Get(ctx context.Context, id int64) (*manman.GameConfig, error) {
	return &manman.GameConfig{ConfigID: id, GameID: 1}, nil
}

// MockStrategyRepo
type MockStrategyRepo struct {
	repository.ConfigurationStrategyRepository
}

func (m *MockStrategyRepo) ListByGame(ctx context.Context, gameID int64) ([]*manman.ConfigurationStrategy, error) {
	return []*manman.ConfigurationStrategy{}, nil
}

// MockServerPortRepo
type MockServerPortRepo struct {
	repository.ServerPortRepository
}

func (m *MockServerPortRepo) DeallocatePortsBySessionID(ctx context.Context, sessionID int64) error {
	return nil
}

// MockGameConfigVolumeRepo
type MockGameConfigVolumeRepo struct {
	repository.GameConfigVolumeRepository
}

func (m *MockGameConfigVolumeRepo) ListByGameConfig(ctx context.Context, configID int64) ([]*manman.GameConfigVolume, error) {
	return []*manman.GameConfigVolume{}, nil
}

func TestStartSessionLifecycle(t *testing.T) {
	sessionRepo := &MockSessionRepo{}
	sgcRepo := &MockSGCRepo{}
	gcRepo := &MockGCRepo{}
	strategyRepo := &MockStrategyRepo{}
	serverPortRepo := &MockServerPortRepo{}
	volumeRepo := &MockGameConfigVolumeRepo{}

	repo := &repository.Repository{
		Sessions:                sessionRepo,
		ServerGameConfigs:       sgcRepo,
		GameConfigs:             gcRepo,
		ConfigurationStrategies: strategyRepo,
		ServerPorts:             serverPortRepo,
		GameConfigVolumes:       volumeRepo,
	}

	h := &SessionHandler{
		repo:        repo,
		sessionRepo: sessionRepo,
		sgcRepo:     sgcRepo,
		gcRepo:      gcRepo,
		publisher:   nil, // Publisher is optional in StartSession
	}

	sgcID := int64(100)

	t.Run("Happy path: no active sessions", func(t *testing.T) {
		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID}
		resp, err := h.StartSession(context.Background(), req)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if resp.Session.Status != manman.SessionStatusPending {
			t.Errorf("Expected status pending, got %s", resp.Session.Status)
		}
	})

	t.Run("Sad path: active session exists, force=false", func(t *testing.T) {
		// Existing running session
		sessionRepo.sessions = []*manman.Session{
			{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning},
		}

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID, Force: false}
		_, err := h.StartSession(context.Background(), req)
		if err == nil {
			t.Fatal("Expected error, got nil")
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.FailedPrecondition {
			t.Errorf("Expected FailedPrecondition error, got %v", err)
		}
	})

	t.Run("Happy path: active session exists, force=true", func(t *testing.T) {
		// Existing running session
		sessionRepo.sessions = []*manman.Session{
			{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning},
		}
		sessionRepo.created = nil

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID, Force: true}
		resp, err := h.StartSession(context.Background(), req)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if resp.Session.Status != manman.SessionStatusPending {
			t.Errorf("Expected status pending, got %s", resp.Session.Status)
		}
		if len(sessionRepo.created) != 1 {
			t.Fatal("Expected new session to be created")
		}
	})

	t.Run("Happy path: crashed session exists, force=false", func(t *testing.T) {
		// Existing crashed session
		sessionRepo.sessions = []*manman.Session{
			{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusCrashed},
		}
		sessionRepo.created = nil

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID, Force: false}
		resp, err := h.StartSession(context.Background(), req)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		if resp.Session.Status != manman.SessionStatusPending {
			t.Errorf("Expected status pending, got %s", resp.Session.Status)
		}
	})
}

const restartTestStallTimeout = 30 * time.Second

// newRestartDeploymentHandler builds a SessionHandler wired the same way
// NewSessionHandler does, with all of RestartDeployment's dependencies as
// fakes, sharing a single opLog across the session and pending-restart mocks
// so tests can assert call order (#1730 "Ordering is load-bearing").
func newRestartDeploymentHandler(sessions []*manman.Session, sgcRepo *MockSGCRepo, pendingRepo *MockPendingRestartRepo) (*SessionHandler, *MockSessionRepo) {
	opLog := &[]string{}
	sessionRepo := &MockSessionRepo{sessions: sessions, opLog: opLog}
	if sgcRepo == nil {
		sgcRepo = &MockSGCRepo{}
	}
	if pendingRepo == nil {
		pendingRepo = &MockPendingRestartRepo{}
	}
	pendingRepo.opLog = opLog

	gcRepo := &MockGCRepo{}
	serverPortRepo := &MockServerPortRepo{}
	volumeRepo := &MockGameConfigVolumeRepo{}

	repo := &repository.Repository{
		Sessions:          sessionRepo,
		ServerGameConfigs: sgcRepo,
		GameConfigs:       gcRepo,
		ServerPorts:       serverPortRepo,
		GameConfigVolumes: volumeRepo,
		PendingRestarts:   pendingRepo,
	}

	h := &SessionHandler{
		repo:                repo,
		sessionRepo:         sessionRepo,
		sgcRepo:             sgcRepo,
		gcRepo:              gcRepo,
		pendingRestartsRepo: pendingRepo,
		publisher:           nil, // Publisher is optional; StopSession/StartSession skip the RMQ publish and still run their DB side effects.
		restartStallTimeout: restartTestStallTimeout,
	}
	return h, sessionRepo
}

func TestRestartDeploymentDispatchHalf(t *testing.T) {
	const sgcID = int64(100)

	t.Run("live session present: record then dispatch, in that order", func(t *testing.T) {
		liveSession := &manman.Session{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning}
		pendingRepo := &MockPendingRestartRepo{}
		h, sessionRepo := newRestartDeploymentHandler([]*manman.Session{liveSession}, nil, pendingRepo)

		before := time.Now()
		resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if resp.AlreadyInFlight {
			t.Fatal("expected already_in_flight to be false")
		}
		if resp.StoppingSession == nil || resp.StoppingSession.SessionId != liveSession.SessionID {
			t.Fatalf("expected stopping_session to be the live session, got %+v", resp.StoppingSession)
		}

		if len(pendingRepo.createCalls) != 1 {
			t.Fatalf("expected exactly one PendingRestarts.Create call, got %d", len(pendingRepo.createCalls))
		}
		call := pendingRepo.createCalls[0]
		if call.sgcID != sgcID {
			t.Errorf("expected create sgcID %d, got %d", sgcID, call.sgcID)
		}
		if call.gatingSessionID != liveSession.SessionID {
			t.Errorf("expected gating_session_id %d, got %d", liveSession.SessionID, call.gatingSessionID)
		}
		wantDeadline := before.Add(restartTestStallTimeout)
		if call.stallDeadline.Before(wantDeadline.Add(-time.Second)) || call.stallDeadline.After(time.Now().Add(restartTestStallTimeout+time.Second)) {
			t.Errorf("expected stall_deadline ~= now+%s, got %s", restartTestStallTimeout, call.stallDeadline)
		}

		stopCount := 0
		for _, u := range sessionRepo.updated {
			if u.Status == manman.SessionStatusStopping {
				stopCount++
			}
		}
		if stopCount != 1 {
			t.Fatalf("expected exactly one Stop dispatch (session updated to stopping), got %d", stopCount)
		}

		// Ordering is load-bearing (#1730): Create must precede the Stop
		// dispatch's session.Update, not just both occur.
		createIdx, updateIdx := -1, -1
		for i, entry := range sessionRepo.opLogEntries() {
			if entry == "pendingRestart.Create" && createIdx == -1 {
				createIdx = i
			}
			if entry == "session.Update:"+manman.SessionStatusStopping && updateIdx == -1 {
				updateIdx = i
			}
		}
		if createIdx == -1 || updateIdx == -1 {
			t.Fatalf("expected both pendingRestart.Create and session.Update:stopping in op log, got %v", sessionRepo.opLogEntries())
		}
		if createIdx > updateIdx {
			t.Fatalf("expected pendingRestart.Create (idx %d) before Stop dispatch (idx %d), got op log %v", createIdx, updateIdx, sessionRepo.opLogEntries())
		}
	})

	t.Run("already in flight: no-op, zero Stop dispatches", func(t *testing.T) {
		liveSession := &manman.Session{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning}
		pendingRepo := &MockPendingRestartRepo{createErr: repository.ErrPendingRestartExists}
		h, sessionRepo := newRestartDeploymentHandler([]*manman.Session{liveSession}, nil, pendingRepo)

		resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if !resp.AlreadyInFlight {
			t.Fatal("expected already_in_flight to be true")
		}
		if resp.StoppingSession != nil || resp.StartedSession != nil {
			t.Fatalf("expected no session in response, got %+v", resp)
		}
		if len(sessionRepo.updated) != 0 {
			t.Fatalf("expected zero Stop dispatches, got %d session updates", len(sessionRepo.updated))
		}
	})

	t.Run("unexpected Create error: error returned, zero Stop dispatches, zero records", func(t *testing.T) {
		liveSession := &manman.Session{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning}
		wantErr := errors.New("db unavailable")
		pendingRepo := &MockPendingRestartRepo{createErr: wantErr}
		h, sessionRepo := newRestartDeploymentHandler([]*manman.Session{liveSession}, nil, pendingRepo)

		resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if resp != nil {
			t.Fatalf("expected nil response on error, got %+v", resp)
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.Internal {
			t.Errorf("expected Internal error, got %v", err)
		}
		if len(sessionRepo.updated) != 0 {
			t.Fatalf("expected zero Stop dispatches, got %d session updates", len(sessionRepo.updated))
		}
		// createCalls records every attempt, but the mock only "persists" a
		// record on success -- since Create errored, nothing was committed.
		if len(pendingRepo.markFailedCalls) != 0 {
			t.Fatalf("expected zero MarkFailed calls, got %d", len(pendingRepo.markFailedCalls))
		}
	})

	t.Run("Stop dispatch failing: record marked failed, error returned", func(t *testing.T) {
		liveSession := &manman.Session{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning}
		pendingRepo := &MockPendingRestartRepo{}
		h, sessionRepo := newRestartDeploymentHandler([]*manman.Session{liveSession}, nil, pendingRepo)
		sessionRepo.updateErrForStatus = manman.SessionStatusStopping
		sessionRepo.updateErr = errors.New("stop dispatch failed")

		resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if resp != nil {
			t.Fatalf("expected nil response on error, got %+v", resp)
		}
		if len(pendingRepo.markFailedCalls) != 1 {
			t.Fatalf("expected exactly one MarkFailed call, got %d", len(pendingRepo.markFailedCalls))
		}
		if len(pendingRepo.createCalls) != 1 {
			t.Fatalf("expected exactly one Create call, got %d", len(pendingRepo.createCalls))
		}
		if pendingRepo.markFailedCalls[0].pendingRestartID != pendingRepo.nextID {
			t.Errorf("expected MarkFailed on the created record %d, got %d", pendingRepo.nextID, pendingRepo.markFailedCalls[0].pendingRestartID)
		}
	})

	t.Run("no live session: no record created, Start dispatched inline", func(t *testing.T) {
		pendingRepo := &MockPendingRestartRepo{}
		h, sessionRepo := newRestartDeploymentHandler(nil, nil, pendingRepo)

		resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(pendingRepo.createCalls) != 0 {
			t.Fatalf("expected zero PendingRestarts.Create calls, got %d", len(pendingRepo.createCalls))
		}
		if resp.StartedSession == nil {
			t.Fatal("expected started_session to be populated")
		}
		if resp.StoppingSession != nil {
			t.Fatalf("expected no stopping_session, got %+v", resp.StoppingSession)
		}
		if len(sessionRepo.created) != 1 {
			t.Fatalf("expected exactly one session created inline, got %d", len(sessionRepo.created))
		}
	})

	t.Run("unknown server_game_config_id: gRPC error, no record", func(t *testing.T) {
		sgcRepo := &MockSGCRepo{getErr: errors.New("not found")}
		pendingRepo := &MockPendingRestartRepo{}
		h, _ := newRestartDeploymentHandler(nil, sgcRepo, pendingRepo)

		resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if resp != nil {
			t.Fatalf("expected nil response on error, got %+v", resp)
		}
		st, ok := status.FromError(err)
		if !ok || st.Code() != codes.NotFound {
			t.Errorf("expected NotFound error, got %v", err)
		}
		if len(pendingRepo.createCalls) != 0 {
			t.Fatalf("expected zero PendingRestarts.Create calls, got %d", len(pendingRepo.createCalls))
		}
	})
}
