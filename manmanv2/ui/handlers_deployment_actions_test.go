package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards #1627's deployment-scoped Start/Stop/Restart action
// endpoints (handlers_deployment_actions.go): routing/method/verb validation,
// that Start never passes Force=true, that Stop/Restart resolve the live
// session via ListSessionsWithFilters(LiveOnly: true) rather than assuming
// one exists, that restart is genuinely stop-then-start over the exact same
// helpers (not a distinct code path -- the plan's carried-forward note asks
// for the restart StartSession call to be compared against the plain-Start
// call, not just trusted "by convention"), and that every outcome re-renders
// the row from freshly observed state (FR7/FR8) rather than ever redirecting
// or assuming success on the HTMX path.
//
// fakeDeploymentAPIClient follows the same embed-nil-interface pattern as
// fakeManManAPIClient in handlers_sgc_test.go: only the RPCs
// handleDeploymentAction's call graph reaches are overridden, so an
// unexpected call panics loudly on the nil embedded interface instead of
// silently returning a zero value.
type fakeDeploymentAPIClient struct {
	manmanpb.ManManAPIClient

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

	// stopClearsLive simulates a stop that synchronously clears the live
	// session, so restart's waitForNoLiveSession sees "no live session"
	// on its very first (pre-sleep) check and proceeds straight to start
	// without actually waiting out an interval.
	stopClearsLive bool

	calls      []string // records call order: "stop", "start"
	startCalls []*manmanpb.StartSessionRequest
	stopCalls  []*manmanpb.StopSessionRequest
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
	if in.LiveOnly {
		if f.liveSession == nil {
			return &manmanpb.ListSessionsResponse{}, nil
		}
		return &manmanpb.ListSessionsResponse{Sessions: []*manmanpb.Session{f.liveSession}}, nil
	}
	return &manmanpb.ListSessionsResponse{Sessions: f.allSessions}, nil
}

func (f *fakeDeploymentAPIClient) StopSession(ctx context.Context, in *manmanpb.StopSessionRequest, opts ...grpc.CallOption) (*manmanpb.StopSessionResponse, error) {
	f.calls = append(f.calls, "stop")
	f.stopCalls = append(f.stopCalls, in)
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	if f.stopClearsLive {
		f.liveSession = nil
	}
	return &manmanpb.StopSessionResponse{}, nil
}

func (f *fakeDeploymentAPIClient) StartSession(ctx context.Context, in *manmanpb.StartSessionRequest, opts ...grpc.CallOption) (*manmanpb.StartSessionResponse, error) {
	f.calls = append(f.calls, "start")
	f.startCalls = append(f.startCalls, in)
	if f.startErr != nil {
		return nil, f.startErr
	}
	resp := f.startResp
	if resp == nil {
		resp = &manmanpb.Session{SessionId: 999, Status: "pending"}
	}
	return &manmanpb.StartSessionResponse{Session: resp}, nil
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

// TestDeploymentAction_Restart_StopsThenStarts covers FR6: restart is
// literally stop-then-start over the exact same helpers as the plain
// Stop/Start paths -- not a distinct code path merely believed to behave
// the same way. Asserts the call order (stop strictly before start) and
// compares the restart path's StartSession call directly against what the
// plain-Start path sends (Force=false, right ServerGameConfigId) rather
// than trusting the "restart = stop+start" convention by inspection alone.
func TestDeploymentAction_Restart_StopsThenStarts(t *testing.T) {
	restartAPI := &fakeDeploymentAPIClient{
		sgc:            stoppedSGC(42),
		liveSession:    &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions:    []*manmanpb.Session{{SessionId: 999, Status: "pending"}},
		stopClearsLive: true,
	}
	restartApp := newDeploymentTestApp(restartAPI)

	w := doDeploymentAction(restartApp, http.MethodPost, "/sessions/deployments/42/restart", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	if got := restartAPI.calls; len(got) != 2 || got[0] != "stop" || got[1] != "start" {
		t.Fatalf("call order = %v, want [stop start]", got)
	}
	if len(restartAPI.stopCalls) != 1 || restartAPI.stopCalls[0].SessionId != 777 {
		t.Fatalf("expected StopSession called once with the live session's id 777, got %+v", restartAPI.stopCalls)
	}
	if len(restartAPI.startCalls) != 1 {
		t.Fatalf("expected StartSession called once, got %d calls", len(restartAPI.startCalls))
	}
	restartStart := restartAPI.startCalls[0]

	// Compare against the plain-Start path's own recorded call, rather than
	// just asserting restartStart's fields in isolation, per the plan's
	// carried-forward note: prove restart genuinely reuses the Start
	// helper's exact request shape.
	plainAPI := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "pending"}},
	}
	plainApp := newDeploymentTestApp(plainAPI)
	doDeploymentAction(plainApp, http.MethodPost, "/sessions/deployments/42/start", true)
	if len(plainAPI.startCalls) != 1 {
		t.Fatalf("expected the plain-Start comparison call to record exactly 1 StartSession call, got %d", len(plainAPI.startCalls))
	}
	plainStart := plainAPI.startCalls[0]

	if restartStart.Force != plainStart.Force {
		t.Errorf("restart's StartSession.Force = %v, plain Start's = %v; restart must send the identical Force value", restartStart.Force, plainStart.Force)
	}
	if restartStart.ServerGameConfigId != plainStart.ServerGameConfigId {
		t.Errorf("restart's StartSession.ServerGameConfigId = %d, plain Start's = %d; restart must target the same SGC", restartStart.ServerGameConfigId, plainStart.ServerGameConfigId)
	}
	if restartStart.Force {
		t.Errorf("restart's StartSession.Force = true, want false (FR6: no force=true on restart's start step)")
	}
}

// TestDeploymentAction_Restart_CrashedNoLiveSession_StartsOnly covers FR6's
// degenerate case: a crashed/lost deployment has no live session to stop,
// so restart degenerates to the start step alone -- StopSession must never
// be called.
func TestDeploymentAction_Restart_CrashedNoLiveSession_StartsOnly(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 5, Status: "crashed"}},
	}
	app := newDeploymentTestApp(api)

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.stopCalls) != 0 {
		t.Errorf("StopSession call count = %d, want 0 (no live session to stop)", len(api.stopCalls))
	}
	if len(api.startCalls) != 1 {
		t.Errorf("StartSession call count = %d, want 1", len(api.startCalls))
	}
}

// TestDeploymentAction_Restart_StopNeverCompletes_DoesNotStart covers FR6's
// timeout guard: if the stop never actually clears the live session within
// the (injected, short) deadline, restart must not force-kill and must not
// start a second container -- StartSession is never called, and the row
// carries an inline timeout error.
func TestDeploymentAction_Restart_StopNeverCompletes_DoesNotStart(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		liveSession: &manmanpb.Session{SessionId: 777, Status: "running"},
		allSessions: []*manmanpb.Session{{SessionId: 777, Status: "running"}},
		// stopClearsLive left false: the fake keeps reporting the session
		// as live no matter how many times StopSession is called, so
		// waitForNoLiveSession's poll loop never converges.
	}
	app := newDeploymentTestApp(api)
	app.deploymentStopPollInterval = time.Millisecond
	app.deploymentStopTimeout = 5 * time.Millisecond

	w := doDeploymentAction(app, http.MethodPost, "/sessions/deployments/42/restart", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.stopCalls) != 1 {
		t.Errorf("StopSession call count = %d, want 1", len(api.stopCalls))
	}
	if len(api.startCalls) != 0 {
		t.Errorf("StartSession call count = %d, want 0 (must not start a second container after a stop timeout)", len(api.startCalls))
	}
	body := w.Body.String()
	if !strings.Contains(body, "did not complete in time") {
		t.Errorf("expected an inline stop-timeout error, got: %s", body)
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
