package handlers

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func addServer(repo *mockServerRepository, s *manman.Server) {
	repo.servers[s.Name] = s
	if s.ServerID >= repo.nextID {
		repo.nextID = s.ServerID + 1
	}
}

func TestUpdateServer_SetHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	req := &pb.UpdateServerRequest{
		ServerId:          1,
		HostPublicAddress: "203.0.113.5",
		UpdatePaths:       []string{"host_public_address"},
	}

	resp, err := handler.UpdateServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.HostPublicAddress != "203.0.113.5" {
		t.Errorf("Expected host_public_address=203.0.113.5 on response, got: %q", resp.Server.HostPublicAddress)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.HostPublicAddress == nil || *stored.HostPublicAddress != "203.0.113.5" {
		t.Errorf("Expected stored host_public_address=203.0.113.5, got: %v", stored.HostPublicAddress)
	}
}

func TestUpdateServer_ClearHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addr := "203.0.113.5"
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, HostPublicAddress: &addr})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	req := &pb.UpdateServerRequest{
		ServerId:          1,
		HostPublicAddress: "",
		UpdatePaths:       []string{"host_public_address"},
	}

	resp, err := handler.UpdateServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.HostPublicAddress != "" {
		t.Errorf("Expected cleared host_public_address on response, got: %q", resp.Server.HostPublicAddress)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.HostPublicAddress != nil {
		t.Errorf("Expected stored host_public_address to be nil, got: %v", *stored.HostPublicAddress)
	}
}

func TestUpdateServer_FieldMaskIsolatesHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addr := "203.0.113.5"
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, HostPublicAddress: &addr})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	req := &pb.UpdateServerRequest{
		ServerId:          1,
		Name:              "srv-1-renamed",
		HostPublicAddress: "198.51.100.9", // populated, but not in update_paths
		UpdatePaths:       []string{"name"},
	}

	_, err := handler.UpdateServer(context.Background(), req)
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.Name != "srv-1-renamed" {
		t.Errorf("Expected name to be updated, got: %q", stored.Name)
	}
	if stored.HostPublicAddress == nil || *stored.HostPublicAddress != addr {
		t.Errorf("Expected host_public_address to be untouched (%q), got: %v", addr, stored.HostPublicAddress)
	}
}

func TestGetServer_NullHostPublicAddress(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.GetServer(context.Background(), &pb.GetServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.HostPublicAddress != "" {
		t.Errorf("Expected host_public_address=\"\" for a NULL address, got: %q", resp.Server.HostPublicAddress)
	}
}

func TestDrainServer_TransitionsToDrainingWithTimestamp(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected drain_state=draining on response, got: %q", resp.Server.DrainState)
	}
	if resp.Server.DrainRequestedAt == 0 {
		t.Error("Expected a non-zero drain_requested_at on response")
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected stored drain_state=draining, got: %q", stored.DrainState)
	}
	if stored.DrainRequestedAt == nil {
		t.Error("Expected stored drain_requested_at to be set")
	}
}

func TestUndrainServer_ReturnsToSchedulableAndClearsTimestamp(t *testing.T) {
	repo := newMockServerRepository()
	requestedAt := time.Now()
	addServer(repo, &manman.Server{
		ServerID:         1,
		Name:             "srv-1",
		Status:           manman.ServerStatusOnline,
		DrainState:       manman.ServerDrainStateDraining,
		DrainRequestedAt: &requestedAt,
	})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.UndrainServer(context.Background(), &pb.UndrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if resp.Server.DrainState != manman.ServerDrainStateSchedulable {
		t.Errorf("Expected drain_state=schedulable on response, got: %q", resp.Server.DrainState)
	}
	if resp.Server.DrainRequestedAt != 0 {
		t.Errorf("Expected drain_requested_at=0 on response, got: %d", resp.Server.DrainRequestedAt)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Failed to fetch stored server: %v", err)
	}
	if stored.DrainState != manman.ServerDrainStateSchedulable {
		t.Errorf("Expected stored drain_state=schedulable, got: %q", stored.DrainState)
	}
	if stored.DrainRequestedAt != nil {
		t.Errorf("Expected stored drain_requested_at to be nil, got: %v", *stored.DrainRequestedAt)
	}
}

func TestDrainServer_IdempotentOnAlreadyDrainingHost(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateDraining})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected draining an already-draining host to succeed, got error: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected drain_state=draining, got: %q", resp.Server.DrainState)
	}
}

func TestDrainServer_IdempotentOnAlreadyDrainedHost(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateDrained})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected draining an already-drained host to succeed, got error: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateDraining {
		t.Errorf("Expected drain_state=draining, got: %q", resp.Server.DrainState)
	}
}

func TestUndrainServer_IdempotentOnAlreadySchedulableHost(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	resp, err := handler.UndrainServer(context.Background(), &pb.UndrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("Expected undraining an already-schedulable host to succeed, got error: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateSchedulable {
		t.Errorf("Expected drain_state=schedulable, got: %q", resp.Server.DrainState)
	}
}

func TestDrainServer_UnknownServerIDReturnsNotFound(t *testing.T) {
	repo := newMockServerRepository()
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	_, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 999})
	if err == nil {
		t.Fatal("Expected error for unknown server_id")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Errorf("Expected codes.NotFound, got: %v", st.Code())
	}
}

func TestUndrainServer_UnknownServerIDReturnsNotFound(t *testing.T) {
	repo := newMockServerRepository()
	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, nil, nil, nil)

	_, err := handler.UndrainServer(context.Background(), &pb.UndrainServerRequest{ServerId: 999})
	if err == nil {
		t.Fatal("Expected error for unknown server_id")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatal("Expected gRPC status error")
	}
	if st.Code() != codes.NotFound {
		t.Errorf("Expected codes.NotFound, got: %v", st.Code())
	}
}

// -- Drain eviction (#2366, FR3/FR18/FR2) test fixtures --

// drainEventLog is a shared, order-preserving call log used to prove FR18's
// ordering guarantee: cancellation must be recorded before any stop for the
// same eviction batch.
type drainEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *drainEventLog) record(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *drainEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.events))
	copy(out, l.events)
	return out
}

// fakeDrainSessionRepo is a minimal SessionRepository fake for DrainServer's
// eviction/settlement reads: it only implements ListWithFilters, embedding
// the real interface so any other method panics loudly if a test
// accidentally exercises it (same pattern as fakePendingRestartRepo in
// session_restart_consumer_test.go).
type fakeDrainSessionRepo struct {
	repository.SessionRepository

	mu    sync.Mutex
	live  []*manman.Session
	calls []*repository.SessionFilters
	err   error
}

func (f *fakeDrainSessionRepo) ListWithFilters(ctx context.Context, filters *repository.SessionFilters, limit, offset int) ([]*manman.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, filters)
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*manman.Session, len(f.live))
	copy(out, f.live)
	return out, nil
}

func (f *fakeDrainSessionRepo) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// removeLive models a session's stop reaching terminal status: it drops out
// of the live set a subsequent ListWithFilters(LiveOnly) read would see.
func (f *fakeDrainSessionRepo) removeLive(sessionID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.live {
		if s.SessionID == sessionID {
			f.live = append(f.live[:i], f.live[i+1:]...)
			return
		}
	}
}

// fakeDrainPendingRestartRepo is a minimal PendingRestartRepository fake
// implementing only CancelForSGCs.
type fakeDrainPendingRestartRepo struct {
	repository.PendingRestartRepository

	mu             sync.Mutex
	cancelCalls    [][]int64
	cancelReasons  []string
	cancelledCount int
	err            error
	log            *drainEventLog
}

func (f *fakeDrainPendingRestartRepo) CancelForSGCs(ctx context.Context, sgcIDs []int64, reason string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := append([]int64(nil), sgcIDs...)
	f.cancelCalls = append(f.cancelCalls, cp)
	f.cancelReasons = append(f.cancelReasons, reason)
	if f.log != nil {
		f.log.record("cancel")
	}
	if f.err != nil {
		return 0, f.err
	}
	return f.cancelledCount, nil
}

// fakeDrainSessionStopper is a SessionStopper fake recording every
// StopSession call, in order, with a per-session error override and an
// optional onStop hook a test uses to simulate the stop reaching terminal
// status (e.g. dropping the session from a fakeDrainSessionRepo's live set).
type fakeDrainSessionStopper struct {
	mu     sync.Mutex
	calls  []int64
	errFor map[int64]error
	onStop func(sessionID int64)
	log    *drainEventLog
}

func (f *fakeDrainSessionStopper) StopSession(ctx context.Context, req *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req.SessionId)
	err := f.errFor[req.SessionId]
	onStop := f.onStop
	log := f.log
	f.mu.Unlock()

	if log != nil {
		log.record("stop")
	}
	if err != nil {
		return nil, err
	}
	if onStop != nil {
		onStop(req.SessionId)
	}
	return &pb.StopSessionResponse{}, nil
}

func (f *fakeDrainSessionStopper) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func liveSession(sessionID, sgcID int64) *manman.Session {
	return &manman.Session{SessionID: sessionID, SGCID: sgcID, Status: manman.SessionStatusRunning}
}

func TestDrainServer_EvictsEveryLiveSessionExactlyOnce(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})

	sessionRepo := &fakeDrainSessionRepo{live: []*manman.Session{
		liveSession(101, 1),
		liveSession(102, 2),
		liveSession(103, 3),
	}}
	pendingRepo := &fakeDrainPendingRestartRepo{}
	stopper := &fakeDrainSessionStopper{}

	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, sessionRepo, pendingRepo, stopper)

	_, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if got := stopper.callCount(); got != 3 {
		t.Fatalf("expected exactly 3 StopSession calls (one per live session), got %d", got)
	}
	seen := map[int64]int{}
	for _, id := range stopper.calls {
		seen[id]++
	}
	for _, id := range []int64{101, 102, 103} {
		if seen[id] != 1 {
			t.Errorf("expected session %d stopped exactly once, got %d calls", id, seen[id])
		}
	}
}

func TestDrainServer_NoLiveSessionsSettlesToDrainedImmediately(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})

	sessionRepo := &fakeDrainSessionRepo{live: nil}
	pendingRepo := &fakeDrainPendingRestartRepo{}
	stopper := &fakeDrainSessionStopper{}

	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, sessionRepo, pendingRepo, stopper)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateDrained {
		t.Fatalf("expected a host with no live sessions to settle straight to drained, got %q", resp.Server.DrainState)
	}
	if got := stopper.callCount(); got != 0 {
		t.Errorf("expected zero StopSession calls, got %d", got)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("failed to fetch stored server: %v", err)
	}
	if stored.DrainState != manman.ServerDrainStateDrained {
		t.Errorf("expected persisted drain_state=drained, got %q", stored.DrainState)
	}
}

func TestDrainServer_OneStopFailureDoesNotBlockOthers(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})

	sessionRepo := &fakeDrainSessionRepo{live: []*manman.Session{
		liveSession(101, 1),
		liveSession(102, 2),
		liveSession(103, 3),
	}}
	pendingRepo := &fakeDrainPendingRestartRepo{}
	stopper := &fakeDrainSessionStopper{errFor: map[int64]error{102: errors.New("stop failed")}}

	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, sessionRepo, pendingRepo, stopper)

	_, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("expected DrainServer itself to succeed despite one stop failing, got: %v", err)
	}

	if got := stopper.callCount(); got != 3 {
		t.Fatalf("expected all 3 sessions to have a StopSession attempt despite one failing, got %d calls", got)
	}
}

func TestDrainServer_CancelsPendingRestartsBeforeDispatchingStops(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})

	sessionRepo := &fakeDrainSessionRepo{live: []*manman.Session{
		liveSession(101, 1),
		liveSession(102, 2),
	}}
	log := &drainEventLog{}
	pendingRepo := &fakeDrainPendingRestartRepo{log: log}
	stopper := &fakeDrainSessionStopper{log: log}

	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, sessionRepo, pendingRepo, stopper)

	_, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if len(pendingRepo.cancelCalls) != 1 {
		t.Fatalf("expected exactly 1 CancelForSGCs call for the whole eviction batch, got %d", len(pendingRepo.cancelCalls))
	}
	gotSGCs := map[int64]bool{}
	for _, id := range pendingRepo.cancelCalls[0] {
		gotSGCs[id] = true
	}
	if !gotSGCs[1] || !gotSGCs[2] {
		t.Fatalf("expected CancelForSGCs called with sgc_ids [1, 2], got %v", pendingRepo.cancelCalls[0])
	}

	events := log.snapshot()
	if len(events) == 0 || events[0] != "cancel" {
		t.Fatalf("expected cancellation to be the first recorded event, got %v", events)
	}
	for _, e := range events[1:] {
		if e != "stop" {
			t.Fatalf("expected every event after the cancellation to be a stop, got %v", events)
		}
	}
}

func TestDrainServer_ReadsDrainedOnceAllEvictedSessionsAreTerminal(t *testing.T) {
	repo := newMockServerRepository()
	addServer(repo, &manman.Server{ServerID: 1, Name: "srv-1", Status: manman.ServerStatusOnline, DrainState: manman.ServerDrainStateSchedulable})

	sessionRepo := &fakeDrainSessionRepo{live: []*manman.Session{
		liveSession(101, 1),
	}}
	pendingRepo := &fakeDrainPendingRestartRepo{}
	// The real host stop is async: StopSession dispatches it but the
	// session doesn't reach terminal status synchronously within the RPC.
	// Model that by *not* wiring onStop here, so DrainServer's own
	// settlement attempt still sees the session live and stays "draining".
	stopper := &fakeDrainSessionStopper{}

	handler := NewServerHandler(repo, newMockServerPortRangeRepository(), nil, sessionRepo, pendingRepo, stopper)

	resp, err := handler.DrainServer(context.Background(), &pb.DrainServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if resp.Server.DrainState != manman.ServerDrainStateDraining {
		t.Fatalf("expected the host to still be draining while its session is live, got %q", resp.Server.DrainState)
	}

	// The gating session now reaches terminal status (host reports it
	// stopped) -- no separate reconciliation loop runs this; it's the next
	// read (GetServer here) that must observe and persist drained.
	sessionRepo.removeLive(101)

	getResp, err := handler.GetServer(context.Background(), &pb.GetServerRequest{ServerId: 1})
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if getResp.Server.DrainState != manman.ServerDrainStateDrained {
		t.Fatalf("expected GetServer to observe and settle drained once the session is gone, got %q", getResp.Server.DrainState)
	}

	stored, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("failed to fetch stored server: %v", err)
	}
	if stored.DrainState != manman.ServerDrainStateDrained {
		t.Errorf("expected the settled drained state to be persisted, got %q", stored.DrainState)
	}
}

func TestServerToProto_HostPublicAddressRoundTrip(t *testing.T) {
	nilServer := &manman.Server{ServerID: 1, Name: "srv-1"}
	pbNil := serverToProto(nilServer)
	if pbNil.HostPublicAddress != "" {
		t.Errorf("Expected empty string for nil HostPublicAddress, got: %q", pbNil.HostPublicAddress)
	}

	addr := "203.0.113.5"
	populatedServer := &manman.Server{ServerID: 1, Name: "srv-1", HostPublicAddress: &addr}
	pbPopulated := serverToProto(populatedServer)
	if pbPopulated.HostPublicAddress != addr {
		t.Errorf("Expected host_public_address=%q, got: %q", addr, pbPopulated.HostPublicAddress)
	}
}
