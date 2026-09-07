package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards #1627's deployment-scoped Start/Stop/Restart action
// endpoints (handlers_deployment_actions.go): routing/method/verb validation,
// that Start never passes Force=true, that Stop resolves the live session
// via ListSessionsWithFilters(LiveOnly: true) rather than assuming one
// exists, that restart dispatches a single RestartDeployment RPC and never
// calls StopSession/StartSession directly from the UI (#1733 -- the
// stop-then-start orchestration now lives entirely server-side in
// control-api's consumer, #1730/#1731), and that every outcome re-renders
// the row from freshly observed state (FR7/FR8) rather than ever redirecting
// or assuming success on the HTMX path.
//
// fakeDeploymentAPIClient follows the same embed-nil-interface pattern as
// fakeManManAPIClient in handlers_sgc_test.go: only the RPCs
// handleDeploymentAction's call graph reaches are overridden, so an
// unexpected call panics loudly on the nil embedded interface instead of
// silently returning a zero value.
// mu guards calls/startCalls/stopCalls/restartCalls/liveSession below.
type fakeDeploymentAPIClient struct {
	manmanpb.ManManAPIClient

	mu sync.Mutex

	sgc    *manmanpb.ServerGameConfig
	sgcErr error

	gameConfig *manmanpb.GameConfig
	game       *manmanpb.Game

	// liveSession is what ListSessions(LiveOnly: true) returns; nil means
	// no live session for this deployment.
	liveSession *manmanpb.Session
	// allSessions is what ListSessions(LiveOnly: false) returns -- the
	// "all sessions for this SGC" listing buildDeploymentRowData derives
	// the row's LatestSession badge from.
	allSessions []*manmanpb.Session

	startResp *manmanpb.Session
	startErr  error
	stopErr   error

	// restartResp/restartErr control RestartDeployment's outcome. A nil
	// restartResp on a nil-error call falls back to an empty
	// *manmanpb.RestartDeploymentResponse{} (no already_in_flight, no
	// stopping/started session -- restartDeployment doesn't inspect those
	// fields at all, only the error).
	restartResp *manmanpb.RestartDeploymentResponse
	restartErr  error

	// stopBlocksUntilCtxDone simulates a hung/slow StopSession RPC (#1664's
	// FR8 defense-in-depth scenario): StopSession blocks until the passed
	// ctx is done (i.e. until deploymentActionBound's bounded timeout
	// fires) and returns ctx.Err() -- mirroring how a real gRPC call whose
	// context deadline expires returns a DeadlineExceeded-flavored error
	// that ControlClient.StopSession wraps with %w, so errors.Is still
	// sees through to context.DeadlineExceeded.
	stopBlocksUntilCtxDone bool
	// startBlocksUntilCtxDone is stopBlocksUntilCtxDone's StartSession
	// counterpart, covering #1668's extension of the bound to Start.
	startBlocksUntilCtxDone bool
	// restartBlocksUntilCtxDone is stopBlocksUntilCtxDone's
	// RestartDeployment counterpart, covering #1733's extension of the
	// bound to the single restart dispatch.
	restartBlocksUntilCtxDone bool

	// stopIgnoresCtx/startIgnoresCtx/restartIgnoresCtx simulate a
	// StopSession/StartSession/RestartDeployment RPC that never returns and
	// never even looks at ctx -- i.e. the production symptom #1667 actually
	// reported (the API's own handler blocked for its downstream's full
	// unbounded duration regardless of what context it was given). Unlike
	// stopBlocksUntilCtxDone above, these prove boundDeploymentRPC's
	// handler-side race against time.After(timeout) is what saves the
	// caller here, not the context.WithTimeout cancellation reaching the
	// fake at all -- the exact "necessary but not sufficient" gap #1668
	// calls out about a fake client that already respects context
	// cancellation instantly.
	stopIgnoresCtx    bool
	startIgnoresCtx   bool
	restartIgnoresCtx bool

	calls        []string // records call order: "stop", "start"
	startCalls   []*manmanpb.StartSessionRequest
	stopCalls    []*manmanpb.StopSessionRequest
	restartCalls []*manmanpb.RestartDeploymentRequest

	// listPendingRestartsStates/listPendingRestartsErr (#1735), when set,
	// drive ListPendingRestarts's response for tests that care about
	// RestartState propagation/degradation (this file's own scenarios don't
	// -- see the doc comment on ListPendingRestarts below); zero value keeps
	// the pre-existing "no restart record" behavior.
	listPendingRestartsStates map[int64]*manmanpb.PendingRestartState
	listPendingRestartsErr    error
	listPendingRestartsCalls  [][]int64
}

func (f *fakeDeploymentAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	if f.sgcErr != nil {
		return nil, f.sgcErr
	}
	return &manmanpb.GetServerGameConfigResponse{Config: f.sgc}, nil
}

func (f *fakeDeploymentAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	gc := f.gameConfig
	if gc == nil {
		gc = &manmanpb.GameConfig{}
	}
	return &manmanpb.GetGameConfigResponse{Config: gc}, nil
}

func (f *fakeDeploymentAPIClient) GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	g := f.game
	if g == nil {
		g = &manmanpb.Game{}
	}
	return &manmanpb.GetGameResponse{Game: g}, nil
}

func (f *fakeDeploymentAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	f.mu.Lock()
	live := f.liveSession
	all := f.allSessions
	f.mu.Unlock()
	if in.LiveOnly {
		if live == nil {
			return &manmanpb.ListSessionsResponse{}, nil
		}
		return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{live}}, nil
	}
	return &manmanpb.ListSessionsResponse{Sessions: all}, nil
}

func (f *fakeDeploymentAPIClient) StopSession(ctx context.Context, in *manmanpb.StopSessionRequest, opts ...grpc.CallOption) (*manmanpb.StopSessionResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "stop")
	f.stopCalls = append(f.stopCalls, in)
	blocks := f.stopBlocksUntilCtxDone
	ignoresCtx := f.stopIgnoresCtx
	stopErr := f.stopErr
	f.mu.Unlock()

	if ignoresCtx {
		// Never returns and never looks at ctx -- see stopIgnoresCtx's doc
		// comment: this is what actually proves boundDeploymentRPC's
		// time.After race (not context cancellation reaching the fake)
		// bounds the caller.
		select {}
	}
	if blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if stopErr != nil {
		return nil, stopErr
	}
	return &manmanpb.StopSessionResponse{}, nil
}

func (f *fakeDeploymentAPIClient) StartSession(ctx context.Context, in *manmanpb.StartSessionRequest, opts ...grpc.CallOption) (*manmanpb.StartSessionResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "start")
	f.startCalls = append(f.startCalls, in)
	blocks := f.startBlocksUntilCtxDone
	ignoresCtx := f.startIgnoresCtx
	startErr := f.startErr
	resp := f.startResp
	f.mu.Unlock()

	if ignoresCtx {
		select {}
	}
	if blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if startErr != nil {
		return nil, startErr
	}
	if resp == nil {
		resp = &manmanpb.Session{SessionId: 999, Status: "pending"}
	}
	return &manmanpb.StartSessionResponse{Session: resp}, nil
}

// RestartDeployment is the fake's #1733 counterpart to StopSession/
// StartSession above: handleDeploymentAction's "restart" case now dispatches
// this single RPC directly (via ControlClient.RestartDeployment) rather than
// the UI orchestrating StopSession-then-StartSession itself, so this is the
// only call a restart click should ever produce against this fake.
func (f *fakeDeploymentAPIClient) RestartDeployment(ctx context.Context, in *manmanpb.RestartDeploymentRequest, opts ...grpc.CallOption) (*manmanpb.RestartDeploymentResponse, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "restart")
	f.restartCalls = append(f.restartCalls, in)
	blocks := f.restartBlocksUntilCtxDone
	ignoresCtx := f.restartIgnoresCtx
	restartErr := f.restartErr
	resp := f.restartResp
	f.mu.Unlock()

	if ignoresCtx {
		select {}
	}
	if blocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if restartErr != nil {
		return nil, restartErr
	}
	if resp == nil {
		resp = &manmanpb.RestartDeploymentResponse{}
	}
	return resp, nil
}

// ListPendingRestarts is the fake's #1735 counterpart: buildDeploymentRowData
// (handlers_deployment_actions.go) calls this for every row it builds,
// including through the action endpoints this file exercises. This suite's
// own scenarios are not about restart-state badges (see session_test.go/
// components/restart_state_test.go for that), so the zero value reports "no
// restart record" for every sgc id; listPendingRestartsStates/Err let the
// dedicated buildDeploymentRowData tests below opt into a populated or
// failing response without touching every other scenario in this file.
func (f *fakeDeploymentAPIClient) ListPendingRestarts(ctx context.Context, in *manmanpb.ListPendingRestartsRequest, opts ...grpc.CallOption) (*manmanpb.ListPendingRestartsResponse, error) {
	f.mu.Lock()
	f.listPendingRestartsCalls = append(f.listPendingRestartsCalls, in.ServerGameConfigIds)
	f.mu.Unlock()
	if f.listPendingRestartsErr != nil {
		return nil, f.listPendingRestartsErr
	}
	resp := &manmanpb.ListPendingRestartsResponse{}
	for _, id := range in.ServerGameConfigIds {
		if state, ok := f.listPendingRestartsStates[id]; ok {
			resp.States = append(resp.States, state)
		}
	}
	return resp, nil
}

func newDeploymentTestApp(api *fakeDeploymentAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

// doDeploymentAction invokes handleDeploymentAction directly (bypassing the
// mux/auth wrapping main.go's setupRoutes applies) since this file's
// scenarios are about the handler's own routing/dispatch logic, not
// mux-vs-auth wiring -- that boundary is covered separately by
// TestSessionDetailRoutingUnaffected below.
func doDeploymentAction(app *App, method, path string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	app.handleDeploymentAction(w, req)
	return w
}

func stoppedSGC(id int64) *manmanpb.ServerGameConfig {
	return &manmanpb.ServerGameConfig{ServerGameConfigId: id, Status: "active"}
}

// TestDeploymentAction_Start_CallsStartSessionWithoutForce covers FR2: the
// plain Start path must call StartSession with Force=false and the right
// ServerGameConfigId -- no ConfigurationPatch/env layering, no
// force-killing an active session out from under FR1's non-blocking
// crashed/lost handling.
func TestDeploymentAction_Start_CallsStartSessionWithoutForce(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "pending"}},
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/start", true)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.startCalls) != 1 {
		t.Fatalf("StartSession call count = %d, want 1", len(api.startCalls))
	}
	got := api.startCalls[0]
	if got.Force {
		t.Errorf("StartSessionRequest.Force = true, want false (plain Start must not force-kill an active session)")
	}
	if got.ServerGameConfigId != 42 {
		t.Errorf("StartSessionRequest.ServerGameConfigId = %d, want 42", got.ServerGameConfigId)
	}
}

// TestDeploymentAction_Start_RendersObservedPendingNotRunning covers FR8:
// immediately after Start the freshly observed session is pending/starting,
// not running, and the row must render what was actually observed rather
// than assuming success.
func TestDeploymentAction_Start_RendersObservedPendingNotRunning(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "pending"}},
		// No live session yet -- a pending session isn't live.
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/start", true)

	body := w.Body.String()
	if !strings.Contains(body, "pending") {
		t.Errorf("expected the observed pending status in the rendered row, got: %s", body)
	}
	if strings.Contains(body, "running") {
		t.Errorf("expected no assumed-running status in the rendered row (FR8: not an assumed success), got: %s", body)
	}
}

// TestDeploymentAction_Start_Failure_RendersInlineError covers FR8's failure
// path: a StartSession error still responds 200 with the row fragment,
// ActionError populated inline, and never an HX-Redirect.
func TestDeploymentAction_Start_Failure_RendersInlineError(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:      stoppedSGC(42),
		startErr: errors.New("failed to start session: rpc error: internal"),
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/start", true)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failure still re-renders the row); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected an inline alert-error on failure, got: %s", body)
	}
	if !strings.Contains(body, "deployment-row-42") {
		t.Errorf("expected the row fragment for SGC 42, got: %s", body)
	}
	if w.Header().Get("HX-Redirect") != "" {
		t.Errorf("expected no HX-Redirect header on failure, got %q", w.Header().Get("HX-Redirect"))
	}
}

// TestDeploymentAction_Stop_StopsLiveSession covers FR4: Stop resolves the
// deployment's live session via ListSessionsWithFilters(LiveOnly: true) and
// stops that session id.
func TestDeploymentAction_Stop_StopsLiveSession(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		liveSession: &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions: []*manmanpb.Session{{SessionId: 777, Status: "running"}},
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/stop", true)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.stopCalls) != 1 {
		t.Fatalf("StopSession call count = %d, want 1", len(api.stopCalls))
	}
	if api.stopCalls[0].SessionId != 777 {
		t.Errorf("StopSession called with SessionId = %d, want 777 (the live session's id)", api.stopCalls[0].SessionId)
	}
}

// TestDeploymentAction_Stop_NoLiveSession_RendersInlineNotice covers FR8's
// "raced with a crash/stop" case: no live session any more is not an error
// page -- it's an inline notice, and StopSession is never called.
func TestDeploymentAction_Stop_NoLiveSession_RendersInlineNotice(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 1, Status: "stopped"}},
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/stop", true)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.stopCalls) != 0 {
		t.Errorf("StopSession call count = %d, want 0 (no live session to stop)", len(api.stopCalls))
	}
	body := w.Body.String()
	if !strings.Contains(body, "No running session to stop") {
		t.Errorf("expected an inline no-running-session notice, got: %s", body)
	}
}

// TestDeploymentAction_Restart_CallsRestartDeploymentOnly is the core
// assertion of the #1733 cutover: a restart click issues exactly one
// RestartDeployment RPC and zero StopSession/StartSession RPCs from the UI
// -- the stop-then-start orchestration now happens entirely server-side
// (control-api's consumer, #1731), so the UI must never call StopSession or
// StartSession itself for a restart. Also proves the response returns
// promptly (well under a generous bound) rather than waiting on any
// convergence, since restartDeployment does nothing but await the single
// bounded RPC.
func TestDeploymentAction_Restart_CallsRestartDeploymentOnly(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		liveSession: &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions: []*manmanpb.Session{{SessionId: 777, Status: "running"}},
	}
	app := newDeploymentTestApp(api)

	start := time.Now()
	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("handler took %s to return, want well under 1s (restart no longer waits on any convergence)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "deployment-row-42") {
		t.Errorf("expected the row fragment for SGC 42, got: %s", body)
	}
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected no inline error on a successful restart dispatch, got: %s", body)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.restartCalls) != 1 {
		t.Fatalf("RestartDeployment call count = %d, want 1", len(api.restartCalls))
	}
	if got := api.restartCalls[0].ServerGameConfigId; got != 42 {
		t.Errorf("RestartDeploymentRequest.ServerGameConfigId = %d, want 42", got)
	}
	if len(api.stopCalls) != 0 {
		t.Errorf("StopSession call count = %d, want 0 (restart must not dispatch stop from the UI, #1733)", len(api.stopCalls))
	}
	if len(api.startCalls) != 0 {
		t.Errorf("StartSession call count = %d, want 0 (restart must not dispatch start from the UI, #1733)", len(api.startCalls))
	}
}

// TestDeploymentAction_Restart_AlreadyInFlight_RendersTransitionalNoError
// covers the response's already_in_flight: true case: it is a success
// outcome (a double click, or the operator retrying after a pod restart),
// not an error, so it must render the same transitional row as a fresh
// dispatch with no inline error.
func TestDeploymentAction_Restart_AlreadyInFlight_RendersTransitionalNoError(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		liveSession: &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions: []*manmanpb.Session{{SessionId: 777, Status: "running"}},
		restartResp: &manmanpb.RestartDeploymentResponse{AlreadyInFlight: true},
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "alert-error") {
		t.Errorf("expected no inline error for already_in_flight: true, got: %s", body)
	}
	if !strings.Contains(body, "deployment-row-42") {
		t.Errorf("expected the row fragment for SGC 42, got: %s", body)
	}
}

// TestDeploymentAction_Restart_Failure_RendersInlineError covers FR8's
// failure path for restart: an RPC error still responds 200 with the row
// fragment, ActionError populated inline, and never a 500.
func TestDeploymentAction_Restart_Failure_RendersInlineError(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		liveSession: &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions: []*manmanpb.Session{{SessionId: 777, Status: "running"}},
		restartErr:  errors.New("failed to restart deployment: rpc error: internal"),
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failure still re-renders the row); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected an inline alert-error on the failed restart dispatch, got: %s", body)
	}
	if !strings.Contains(body, "Failed to restart the deployment") {
		t.Errorf("expected the generic restart-failure message, got: %s", body)
	}
	if !strings.Contains(body, "deployment-row-42") {
		t.Errorf("expected the row fragment for SGC 42, got: %s", body)
	}
}

// TestDeploymentAction_Restart_SlowRestartDeployment_ReturnsInlineErrorNotDroppedConnection
// covers #1733's extension of the #1664/#1668 outbound-RPC bound to the
// single RestartDeployment dispatch: a call that hangs past
// App.deploymentActionTimeout must not block the handler indefinitely -- it
// must return promptly with a distinct timeout-flavored inline error.
func TestDeploymentAction_Restart_SlowRestartDeployment_ReturnsInlineErrorNotDroppedConnection(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:                       stoppedSGC(42),
		liveSession:               &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions:               []*manmanpb.Session{{SessionId: 777, Status: "running"}},
		restartBlocksUntilCtxDone: true,
	}
	app := newDeploymentTestApp(api)
	app.deploymentActionTimeout = 5 * time.Millisecond

	start := time.Now()
	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("handler took %s to return, want well under 1s (must not block past the bounded timeout)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failure still re-renders the row); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "taking longer than expected") {
		t.Errorf("expected a distinct timeout-flavored inline error, got: %s", body)
	}
	if !strings.Contains(body, "deployment-row-42") {
		t.Errorf("expected the row fragment for SGC 42, got: %s", body)
	}
}

// TestDeploymentAction_Restart_HungRestartDeployment_IgnoresCtx_StillReturnsWithinBound
// is #1668's belt-and-suspenders proof (see
// TestDeploymentAction_Stop_HungStopSession_IgnoresCtx_StillReturnsWithinBound's
// doc comment), extended to restart's single RestartDeployment dispatch:
// restartIgnoresCtx's RestartDeployment never returns and never looks at
// ctx at all, so this only passes because boundDeploymentRPC races the call
// against its own independent time.After(timeout) in the calling goroutine.
func TestDeploymentAction_Restart_HungRestartDeployment_IgnoresCtx_StillReturnsWithinBound(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:               stoppedSGC(42),
		liveSession:       &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions:       []*manmanpb.Session{{SessionId: 777, Status: "running"}},
		restartIgnoresCtx: true,
	}
	app := newDeploymentTestApp(api)
	app.deploymentActionTimeout = 10 * time.Millisecond

	start := time.Now()
	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("handler took %s to return, want well under 1s (a hung callee that ignores ctx must not block the handler)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "taking longer than expected") {
		t.Errorf("expected a distinct timeout-flavored inline error, got: %s", w.Body.String())
	}
}

// TestDeploymentAction_Restart_NoGoroutineSpawned proves the restart path
// spawns no background goroutine of its own (beyond whatever
// boundDeploymentRPC itself races the call in, which exits promptly once
// the bounded RestartDeployment call returns): #1733 deletes
// finishRestartInBackground entirely, so nothing should hold restart intent
// on the stack past the request/response boundary. Asserted via
// runtime.NumGoroutine() settling back to (approximately) its pre-request
// count shortly after the handler returns, rather than growing and staying
// grown the way the old background-goroutine design would have.
func TestDeploymentAction_Restart_NoGoroutineSpawned(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		liveSession: &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions: []*manmanpb.Session{{SessionId: 777, Status: "running"}},
	}
	app := newDeploymentTestApp(api)

	before := runtime.NumGoroutine()

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	// Give boundDeploymentRPC's own short-lived racing goroutine (which
	// exits as soon as the bounded call returns, well before this point)
	// room to unwind before comparing counts.
	deadline := time.Now().Add(time.Second)
	var after int
	for time.Now().Before(deadline) {
		after = runtime.NumGoroutine()
		if after <= before {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if after > before {
		t.Errorf("runtime.NumGoroutine() = %d after the restart request, want <= %d (pre-request) -- no goroutine should outlive the response (finishRestartInBackground was deleted by #1733)", after, before)
	}
}

// TestDeploymentAction_Stop_SlowStopSession_ReturnsInlineErrorNotDroppedConnection
// covers #1664's FR8 defense-in-depth on the plain Stop path: a StopSession
// call that hangs past App.deploymentActionTimeout (boundDeploymentRPC's
// bound, App.deploymentActionBound) must not be allowed to block the
// handler indefinitely -- it must return promptly with a distinct
// timeout-flavored inline error (deploymentStopErrorMessage's
// isDeploymentActionTimeout branch), not a hang that would risk main.go's
// 15s http.Server.WriteTimeout dropping the connection.
func TestDeploymentAction_Stop_SlowStopSession_ReturnsInlineErrorNotDroppedConnection(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:                    stoppedSGC(42),
		liveSession:            &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions:            []*manmanpb.Session{{SessionId: 777, Status: "running"}},
		stopBlocksUntilCtxDone: true,
	}
	app := newDeploymentTestApp(api)
	app.deploymentActionTimeout = 5 * time.Millisecond

	start := time.Now()
	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/stop", true)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("handler took %s to return, want well under 1s (must not block past the bounded timeout)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failure still re-renders the row); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "taking longer than expected") {
		t.Errorf("expected a distinct timeout-flavored inline error, got: %s", body)
	}
	if !strings.Contains(body, "deployment-row-42") {
		t.Errorf("expected the row fragment for SGC 42, got: %s", body)
	}
}

// TestDeploymentAction_Start_SlowStartSession_ReturnsInlineErrorNotDroppedConnection
// covers #1668's extension of #1664's FR8 defense-in-depth to the plain
// Start path: prior to this fix, handleDeploymentAction's "start" case
// called StartSession on the raw, unbounded request context, so a hung
// Start had no bound at all.
func TestDeploymentAction_Start_SlowStartSession_ReturnsInlineErrorNotDroppedConnection(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:                     stoppedSGC(42),
		startBlocksUntilCtxDone: true,
	}
	app := newDeploymentTestApp(api)
	app.deploymentActionTimeout = 5 * time.Millisecond

	start := time.Now()
	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/start", true)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("handler took %s to return, want well under 1s (must not block past the bounded timeout)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failure still re-renders the row); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "taking longer than expected") {
		t.Errorf("expected a distinct timeout-flavored inline error, got: %s", body)
	}
	if !strings.Contains(body, "deployment-row-42") {
		t.Errorf("expected the row fragment for SGC 42, got: %s", body)
	}
}

// TestDeploymentAction_Stop_HungStopSession_IgnoresCtx_StillReturnsWithinBound
// is #1668's belt-and-suspenders proof: stopIgnoresCtx's StopSession never
// returns and never looks at ctx at all, so if boundDeploymentRPC relied
// solely on context cancellation reaching the RPC (the way #1664's plain
// context.WithTimeout did, and the way stopBlocksUntilCtxDone's "necessary
// but not sufficient" fake-client test above already covers), this test
// would hang forever. It only passes because boundDeploymentRPC races the
// call against its own independent time.After(timeout) in the calling
// goroutine, so the handler returns on the bound regardless of whether the
// callee ever cooperates -- the closest a fake client can get to
// reproducing #1667's live-Tilt symptom (a downstream that never answers
// and a handler that never checked its own context either) without an
// actual network hang.
func TestDeploymentAction_Stop_HungStopSession_IgnoresCtx_StillReturnsWithinBound(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:            stoppedSGC(42),
		liveSession:    &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions:    []*manmanpb.Session{{SessionId: 777, Status: "running"}},
		stopIgnoresCtx: true,
	}
	app := newDeploymentTestApp(api)
	app.deploymentActionTimeout = 10 * time.Millisecond

	start := time.Now()
	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/stop", true)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("handler took %s to return, want well under 1s (a hung callee that ignores ctx must not block the handler)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "taking longer than expected") {
		t.Errorf("expected a distinct timeout-flavored inline error, got: %s", w.Body.String())
	}
}

// TestDeploymentAction_Start_HungStartSession_IgnoresCtx_StillReturnsWithinBound
// is TestDeploymentAction_Stop_HungStopSession_IgnoresCtx_StillReturnsWithinBound's
// Start counterpart.
func TestDeploymentAction_Start_HungStartSession_IgnoresCtx_StillReturnsWithinBound(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:             stoppedSGC(42),
		startIgnoresCtx: true,
	}
	app := newDeploymentTestApp(api)
	app.deploymentActionTimeout = 10 * time.Millisecond

	start := time.Now()
	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/start", true)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("handler took %s to return, want well under 1s (a hung callee that ignores ctx must not block the handler)", elapsed)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "taking longer than expected") {
		t.Errorf("expected a distinct timeout-flavored inline error, got: %s", w.Body.String())
	}
}

// TestDeploymentAction_MethodNotAllowed covers the 405 guard: only POST is
// accepted on the deployment action routes.
func TestDeploymentAction_MethodNotAllowed(t *testing.T) {
	app := newDeploymentTestApp(&fakeDeploymentAPIClient{})

	w := doDeploymentAction(app, http.MethodGet, "/sessions/deployments/42/start", true)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405; body: %s", w.Code, w.Body.String())
	}
}

// TestDeploymentAction_BadSGCID covers the 400 guard: an unparseable
// ServerGameConfig id in the path is rejected before any RPC is attempted.
func TestDeploymentAction_BadSGCID(t *testing.T) {
	app := newDeploymentTestApp(&fakeDeploymentAPIClient{})

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/not-a-number/start", true)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

// TestDeploymentAction_UnknownVerb covers the 404 guard: only start/stop/
// restart are recognized verbs.
func TestDeploymentAction_UnknownVerb(t *testing.T) {
	app := newDeploymentTestApp(&fakeDeploymentAPIClient{})

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/frobnicate", true)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestDeploymentAction_SGCNotFound covers the case where the path's SGC id
// doesn't resolve to an actual ServerGameConfig: buildDeploymentRowData's
// failure to fetch it must surface as 404, not a panic on a nil Config or a
// silently-empty row.
func TestDeploymentAction_SGCNotFound(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgcErr: errors.New("rpc error: code = NotFound desc = server_game_config not found"),
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/9999/start", true)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestDeploymentAction_NonHTMXRequest_Redirects covers the no-JS form
// fallback: a request without the HX-Request header redirects back to
// /sessions instead of returning a bare fragment.
func TestDeploymentAction_NonHTMXRequest_Redirects(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "pending"}},
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/start", false)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/sessions" {
		t.Errorf("Location = %q, want /sessions", loc)
	}
}

// TestSessionDetailRoutingUnaffected guards the new "/sessions/deployments/"
// prefix route against hijacking the pre-existing "/sessions/" catch-all:
// Go's ServeMux longest-pattern-wins means "/sessions/deployments/" must
// win for its own paths, but "/sessions/{id}/stop" must still reach
// handleSessionDetail -> handleSessionStop exactly as before #1627.
func TestSessionDetailRoutingUnaffected(t *testing.T) {
	api := &fakeDeploymentAPIClient{}
	app := newDeploymentTestApp(api)

	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/", app.handleSessionDetail)
	mux.HandleFunc("/sessions/deployments/", app.handleDeploymentAction)

	req := httptest.NewRequest(http.MethodPost, "/sessions/123/stop", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (handleSessionStop's redirect); body: %s", w.Code, w.Body.String())
	}
	if len(api.stopCalls) != 1 || api.stopCalls[0].SessionId != 123 {
		t.Fatalf("expected StopSession called once with SessionId 123 (handleSessionStop's own path), got %+v", api.stopCalls)
	}
}
