package handlers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
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

	// fleetStatus/fleetStatusErr back CountRunningDeploymentsByGame for
	// TestGetFleetStatusSummary (#2371).
	fleetStatus    []*manman.FleetGameStatus
	fleetStatusErr error
}

func (m *MockSessionRepo) CountRunningDeploymentsByGame(ctx context.Context) ([]*manman.FleetGameStatus, error) {
	if m.fleetStatusErr != nil {
		return nil, m.fleetStatusErr
	}
	return m.fleetStatus, nil
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

	// byLatestSGCID and getLatestErr drive GetLatestBySGCIDs for
	// ListPendingRestarts tests (#1735). The mock does not apply the
	// visibility-window trim itself -- that's exercised against the real
	// postgres repository, not this fake -- it just returns exactly what the
	// test populates, keyed by sgc id, echoing repository.go's contract that
	// an id with no record is simply absent from the map.
	byLatestSGCID map[int64]*manman.PendingRestart
	getLatestErr  error
	// getLatestCalls records each GetLatestBySGCIDs call's id slice, so tests
	// can assert an empty id list never reaches the repository.
	getLatestCalls [][]int64
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

func (m *MockPendingRestartRepo) GetLatestBySGCIDs(ctx context.Context, sgcIDs []int64) (map[int64]*manman.PendingRestart, error) {
	m.getLatestCalls = append(m.getLatestCalls, sgcIDs)
	if m.getLatestErr != nil {
		return nil, m.getLatestErr
	}
	result := make(map[int64]*manman.PendingRestart)
	for _, id := range sgcIDs {
		if pr, ok := m.byLatestSGCID[id]; ok {
			result[id] = pr
		}
	}
	return result, nil
}

// MockGCRepo
type MockGCRepo struct {
	repository.GameConfigRepository
	// gc, when non-nil, is returned by Get instead of the default stub.
	gc *manman.GameConfig
}

func (m *MockGCRepo) Get(ctx context.Context, id int64) (*manman.GameConfig, error) {
	if m.gc != nil {
		return m.gc, nil
	}
	return &manman.GameConfig{ConfigID: id, GameID: 1}, nil
}

// MockStrategyRepo
type MockStrategyRepo struct {
	repository.ConfigurationStrategyRepository
	// strategies, when non-nil, is returned by ListByGame (default: empty).
	strategies []*manman.ConfigurationStrategy
}

func (m *MockStrategyRepo) ListByGame(ctx context.Context, gameID int64) ([]*manman.ConfigurationStrategy, error) {
	if m.strategies != nil {
		return m.strategies, nil
	}
	return []*manman.ConfigurationStrategy{}, nil
}

// MockPatchRepo
type MockPatchRepo struct {
	repository.ConfigurationPatchRepository
	// patches is returned (filtered by the requested level/entity) by List.
	patches []*manman.ConfigurationPatch
	listErr error
	// listCalls records each List call's (level, entityID) filter so tests
	// can assert both deployment levels were consulted.
	listCalls []string
}

func (m *MockPatchRepo) List(ctx context.Context, strategyID *int64, patchLevel *string, entityID *int64) ([]*manman.ConfigurationPatch, error) {
	m.listCalls = append(m.listCalls, fmt.Sprintf("level=%v entity=%v", derefStr(patchLevel), derefI64(entityID)))
	if m.listErr != nil {
		return nil, m.listErr
	}
	var result []*manman.ConfigurationPatch
	for _, p := range m.patches {
		if patchLevel != nil && p.PatchLevel != *patchLevel {
			continue
		}
		if entityID != nil && p.EntityID != *entityID {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func derefI64(i *int64) int64 {
	if i == nil {
		return -1
	}
	return *i
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
	patchRepo := &MockPatchRepo{}
	serverPortRepo := &MockServerPortRepo{}
	volumeRepo := &MockGameConfigVolumeRepo{}
	// MockSGCRepo.Get always pins ServerID 1 (#2364 cordon guard).
	serverRepo := newMockCordonServerRepo(1)

	repo := &repository.Repository{
		Sessions:                sessionRepo,
		ServerGameConfigs:       sgcRepo,
		GameConfigs:             gcRepo,
		ConfigurationStrategies: strategyRepo,
		ConfigurationPatches:    patchRepo,
		ServerPorts:             serverPortRepo,
		GameConfigVolumes:       volumeRepo,
		Servers:                 serverRepo,
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

	// Cordon (#2364, FR3): a deployment pinned to a draining/drained host
	// cannot be started, and a blocked start must leave no orphaned pending
	// session behind.
	for _, state := range []string{manman.ServerDrainStateDraining, manman.ServerDrainStateDrained} {
		t.Run("Sad path: "+state+" host rejects start, no session created", func(t *testing.T) {
			sessionRepo.sessions = nil
			sessionRepo.created = nil
			serverRepo.servers[1].DrainState = state

			req := &pb.StartSessionRequest{ServerGameConfigId: sgcID}
			resp, err := h.StartSession(context.Background(), req)
			if err == nil {
				t.Fatalf("expected error, got nil (resp=%+v)", resp)
			}
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.FailedPrecondition {
				t.Errorf("expected FailedPrecondition, got %v", err)
			}
			if len(sessionRepo.created) != 0 {
				t.Fatalf("expected no session to be created, got %d", len(sessionRepo.created))
			}
		})
	}

	t.Run("Happy path: start succeeds again after undrain", func(t *testing.T) {
		sessionRepo.sessions = nil
		sessionRepo.created = nil
		serverRepo.servers[1].DrainState = manman.ServerDrainStateSchedulable

		req := &pb.StartSessionRequest{ServerGameConfigId: sgcID}
		resp, err := h.StartSession(context.Background(), req)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if resp.Session.Status != manman.SessionStatusPending {
			t.Errorf("expected status pending, got %s", resp.Session.Status)
		}
		if len(sessionRepo.created) != 1 {
			t.Fatalf("expected new session to be created, got %d", len(sessionRepo.created))
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
		Sessions:                sessionRepo,
		ServerGameConfigs:       sgcRepo,
		GameConfigs:             gcRepo,
		ConfigurationStrategies: &MockStrategyRepo{},
		ConfigurationPatches:    &MockPatchRepo{},
		ServerPorts:             serverPortRepo,
		GameConfigVolumes:       volumeRepo,
		PendingRestarts:         pendingRepo,
		// MockSGCRepo.Get always pins ServerID 1 (#2364 cordon guard).
		Servers: newMockCordonServerRepo(1),
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

	// Cordon (#2364, FR3): a deployment pinned to a draining/drained host
	// cannot be restarted back onto it -- covers both the live-session
	// (deferred Stop-then-Start) and no-live-session (inline Start) paths.
	for _, state := range []string{manman.ServerDrainStateDraining, manman.ServerDrainStateDrained} {
		t.Run(state+" host, live session: rejected, no stop dispatched, no pending_restarts row", func(t *testing.T) {
			liveSession := &manman.Session{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning}
			pendingRepo := &MockPendingRestartRepo{}
			h, sessionRepo := newRestartDeploymentHandler([]*manman.Session{liveSession}, nil, pendingRepo)
			h.repo.Servers.(*MockCordonServerRepo).servers[1].DrainState = state

			resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
			if err == nil {
				t.Fatalf("expected error, got nil (resp=%+v)", resp)
			}
			if resp != nil {
				t.Fatalf("expected nil response on error, got %+v", resp)
			}
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.FailedPrecondition {
				t.Errorf("expected FailedPrecondition, got %v", err)
			}
			if len(pendingRepo.createCalls) != 0 {
				t.Fatalf("expected zero PendingRestarts.Create calls, got %d", len(pendingRepo.createCalls))
			}
			if len(sessionRepo.updated) != 0 {
				t.Fatalf("expected zero Stop dispatches, got %d session updates", len(sessionRepo.updated))
			}
		})

		t.Run(state+" host, no live session: rejected, no inline start", func(t *testing.T) {
			pendingRepo := &MockPendingRestartRepo{}
			h, sessionRepo := newRestartDeploymentHandler(nil, nil, pendingRepo)
			h.repo.Servers.(*MockCordonServerRepo).servers[1].DrainState = state

			resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
			if err == nil {
				t.Fatalf("expected error, got nil (resp=%+v)", resp)
			}
			st, ok := status.FromError(err)
			if !ok || st.Code() != codes.FailedPrecondition {
				t.Errorf("expected FailedPrecondition, got %v", err)
			}
			if len(sessionRepo.created) != 0 {
				t.Fatalf("expected zero sessions created, got %d", len(sessionRepo.created))
			}
			if len(pendingRepo.createCalls) != 0 {
				t.Fatalf("expected zero PendingRestarts.Create calls, got %d", len(pendingRepo.createCalls))
			}
		})
	}

	t.Run("restart succeeds again after undrain", func(t *testing.T) {
		liveSession := &manman.Session{SessionID: 1, SGCID: sgcID, Status: manman.SessionStatusRunning}
		pendingRepo := &MockPendingRestartRepo{}
		h, _ := newRestartDeploymentHandler([]*manman.Session{liveSession}, nil, pendingRepo)
		h.repo.Servers.(*MockCordonServerRepo).servers[1].DrainState = manman.ServerDrainStateSchedulable

		resp, err := h.RestartDeployment(context.Background(), &pb.RestartDeploymentRequest{ServerGameConfigId: sgcID})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if resp.StoppingSession == nil {
			t.Fatal("expected stopping_session to be populated")
		}
		if len(pendingRepo.createCalls) != 1 {
			t.Fatalf("expected exactly one PendingRestarts.Create call, got %d", len(pendingRepo.createCalls))
		}
	})
}

// newListPendingRestartsHandler builds a SessionHandler with only the
// pending-restarts repo populated -- ListPendingRestarts (#1735) doesn't
// touch the session, sgc, or gc repos at all, so those stay nil to keep an
// accidental dependency on them a hard nil-pointer failure rather than a
// silent pass.
func newListPendingRestartsHandler(pendingRepo *MockPendingRestartRepo) *SessionHandler {
	repo := &repository.Repository{PendingRestarts: pendingRepo}
	return &SessionHandler{
		repo:                repo,
		pendingRestartsRepo: pendingRepo,
	}
}

func newFleetStatusHandler(sessionRepo *MockSessionRepo) *SessionHandler {
	repo := &repository.Repository{Sessions: sessionRepo}
	return &SessionHandler{
		repo:        repo,
		sessionRepo: sessionRepo,
	}
}

// TestGetFleetStatusSummary covers the RPC handler (#2371, manmanv2 M6,
// FR5/NFR5): it must pass the aggregate's rows through as
// FleetGameStatus messages field-for-field, and turn an aggregate query
// failure into an Internal error rather than a partial/zero-value
// response.
func TestGetFleetStatusSummary(t *testing.T) {
	t.Run("passes aggregate rows through unchanged", func(t *testing.T) {
		sessionRepo := &MockSessionRepo{
			fleetStatus: []*manman.FleetGameStatus{
				{GameID: 1, GameName: "Valheim", TotalCount: 3, RunningCount: 1},
				{GameID: 2, GameName: "Minecraft", TotalCount: 0, RunningCount: 0},
			},
		}
		h := newFleetStatusHandler(sessionRepo)

		resp, err := h.GetFleetStatusSummary(context.Background(), &pb.GetFleetStatusSummaryRequest{})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(resp.Games) != 2 {
			t.Fatalf("expected 2 games, got %d: %+v", len(resp.Games), resp.Games)
		}

		byID := map[int64]*pb.FleetGameStatus{}
		for _, g := range resp.Games {
			byID[g.GameId] = g
		}
		valheim := byID[1]
		if valheim == nil || valheim.GameName != "Valheim" || valheim.TotalCount != 3 || valheim.RunningCount != 1 {
			t.Errorf("unexpected Valheim row: %+v", valheim)
		}
		minecraft := byID[2]
		if minecraft == nil || minecraft.GameName != "Minecraft" || minecraft.TotalCount != 0 || minecraft.RunningCount != 0 {
			t.Errorf("expected zero-deployment game to be included as 0/0, got: %+v", minecraft)
		}
	})

	t.Run("aggregate query failure returns Internal error", func(t *testing.T) {
		sessionRepo := &MockSessionRepo{fleetStatusErr: errors.New("db unavailable")}
		h := newFleetStatusHandler(sessionRepo)

		_, err := h.GetFleetStatusSummary(context.Background(), &pb.GetFleetStatusSummaryRequest{})
		if err == nil {
			t.Fatal("expected an error when the aggregate query fails")
		}
		if status.Code(err) != codes.Internal {
			t.Fatalf("expected codes.Internal, got %v", status.Code(err))
		}
	})
}

func TestListPendingRestarts(t *testing.T) {
	t.Run("empty id list: empty response, no query", func(t *testing.T) {
		pendingRepo := &MockPendingRestartRepo{}
		h := newListPendingRestartsHandler(pendingRepo)

		resp, err := h.ListPendingRestarts(context.Background(), &pb.ListPendingRestartsRequest{})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(resp.States) != 0 {
			t.Fatalf("expected zero states, got %d", len(resp.States))
		}
		if len(pendingRepo.getLatestCalls) != 0 {
			t.Fatalf("expected zero GetLatestBySGCIDs calls for an empty id list, got %d", len(pendingRepo.getLatestCalls))
		}
	})

	t.Run("one state per SGC with a record, SGCs without one omitted", func(t *testing.T) {
		reason := "stop dispatch failed"
		pendingRepo := &MockPendingRestartRepo{
			byLatestSGCID: map[int64]*manman.PendingRestart{
				100: {PendingRestartID: 1, ServerGameConfigID: 100, GatingSessionID: 5, Status: "pending", CreatedAt: time.Unix(1000, 0)},
				102: {PendingRestartID: 2, ServerGameConfigID: 102, GatingSessionID: 6, Status: "failed", FailureReason: &reason, CreatedAt: time.Unix(2000, 0), ResolvedAt: timePtr(time.Unix(2100, 0))},
			},
		}
		h := newListPendingRestartsHandler(pendingRepo)

		resp, err := h.ListPendingRestarts(context.Background(), &pb.ListPendingRestartsRequest{ServerGameConfigIds: []int64{100, 101, 102}})
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if len(resp.States) != 2 {
			t.Fatalf("expected 2 states (101 has no record), got %d: %+v", len(resp.States), resp.States)
		}

		bySGC := make(map[int64]*pb.PendingRestartState)
		for _, s := range resp.States {
			bySGC[s.ServerGameConfigId] = s
		}
		if _, ok := bySGC[101]; ok {
			t.Fatalf("expected SGC 101 (no record) to be omitted, got %+v", bySGC[101])
		}

		got100 := bySGC[100]
		if got100 == nil || got100.Status != "pending" || got100.PendingRestartId != 1 || got100.GatingSessionId != 5 || got100.CreatedAtUnix != 1000 {
			t.Errorf("unexpected state for SGC 100: %+v", got100)
		}
		got102 := bySGC[102]
		if got102 == nil || got102.Status != "failed" || got102.FailureReason != reason || got102.ResolvedAtUnix != 2100 {
			t.Errorf("unexpected state for SGC 102: %+v", got102)
		}
	})

	t.Run("GetLatestBySGCIDs failure: internal error", func(t *testing.T) {
		pendingRepo := &MockPendingRestartRepo{getLatestErr: errors.New("db unavailable")}
		h := newListPendingRestartsHandler(pendingRepo)

		resp, err := h.ListPendingRestarts(context.Background(), &pb.ListPendingRestartsRequest{ServerGameConfigIds: []int64{100}})
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
	})
}

func timePtr(t time.Time) *time.Time { return &t }
