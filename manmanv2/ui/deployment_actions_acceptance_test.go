package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file is #1630's end-to-end acceptance suite for milestone M2
// (capability C21, one-click deployment start/stop/restart -- #1620). Unlike
// the per-task suites in #1625-#1628 (which each call one handler directly,
// bypassing the mux/auth stack, and use a fake scoped to that one handler's
// call graph), this file drives a real http.ServeMux built from
// (*App).setupRoutes -- the same construction main_test.go uses -- so every
// test here exercises the actual route registration, auth wrapping, and
// handler wiring a browser would hit against /sessions. The per-task suites
// stay as regression guards for their own layer; this suite guards the
// composed behavior a Server Manager actually gets, so a future refactor
// that splits the layers differently still has one place proving C21 works
// end to end.
//
// fakeAcceptanceAPIClient is stateful, not fixed-response: StartSession
// creates a session in "pending" (StopSession moves the live session to
// "stopping"), and the fake's own storage is the single source of truth
// every ListSessions call reads from -- so a poll of either the
// unfiltered/PageSize-200 shape (buildDeploymentRowData's LatestSession/
// Actions derivation) or the LiveOnly shape (the Live Session cell, and
// Stop's live-session resolution) observes the same underlying state.
// Tick(), called explicitly by a test between two HTTP round-trips,
// converges every currently-transient session one step (pending/starting ->
// running, or -> crashed if the SGC was configured via setLaunchFails;
// stopping -> stopped). This is what FR7/FR8 use to model "poll the row
// fragment endpoint through the fake's lifecycle ticks" -- the test
// controls exactly when a tick happens, so a poll before the tick observes
// the pre-tick state and a poll after observes the post-tick state,
// deterministically.
//
// Since #1733, restart no longer orchestrates stop-then-start from the UI
// at all -- handleDeploymentAction's "restart" case dispatches a single
// RestartDeployment RPC (fakeAcceptanceAPIClient.RestartDeployment below)
// and returns as soon as it acks, so there is no wait-for-no-live-session
// convergence left in this file to model: RestartDeployment simply records
// the call and returns a canned response.
//
// Red/green discipline (verified by hand, then reverted -- see individual
// notes at each check):
//   - Inverting components.ComputeDeploymentActions' crashed/lost case to
//     not set CanStart made TestFR1_StartOfferedOnlyOnStoppedCrashedLost
//     fail (crashed/lost rows lost their Start button) while compiling
//     cleanly; reverting restored green.
//   - Changing handleDeploymentAction's "start" case to call
//     app.grpc.StartSession(ctx, sgcID, true) (force=true) made
//     TestFR2_StartFromListStartsSessionWithoutNavigation fail (Force ==
//     true, want false); reverting restored green.
//   - Changing restartDeployment to call app.grpc.StopSession/StartSession
//     directly instead of app.grpc.RestartDeployment made
//     TestFR6_RestartDispatchesRestartDeploymentOnly fail (RestartDeployment
//     call count == 0, want 1; StopSession/StartSession call counts != 0);
//     reverting restored green.
//   - Removing "pending" from components.IsTransientStatus' switch made
//     TestFR7_RowUpdatesInPlaceWithoutFullReload fail (the immediately-
//     rendered pending row carried no hx-trigger poll attribute); reverting
//     restored green.
//   - Changing the Stop form's hx-post target in
//     pages/sessions.templ (DeploymentRowInner) from
//     fmt.Sprintf("/sessions/deployments/%d/stop", ...) to
//     fmt.Sprintf("/sgc/%d/stop", ...) made
//     TestNFR_AllActionsReachableFromListWithoutDetailPage fail (an hx-post
//     target outside /sessions/deployments/...); reverting (and
//     regenerating templ output) restored green.

// fakeSession is one deployment's current session record in
// fakeAcceptanceAPIClient's storage.
type fakeSession struct {
	id             int64
	sgcID          int64
	status         string
	startedAt      int64
	stopChecksLeft int // see fakeAcceptanceAPIClient's header comment
}

func (s *fakeSession) toProto() *manmanpb.Session {
	if s == nil {
		return nil
	}
	return &manmanpb.Session{
		SessionId:          s.id,
		ServerGameConfigId: s.sgcID,
		Status:             s.status,
		StartedAt:          s.startedAt,
	}
}

// isLiveAcceptanceStatus mirrors what this fake treats as "has a live
// container" for its LiveOnly=true query path -- pending/starting/running/
// stopping all count as live (a container is provisioning, up, or tearing
// down); stopped/crashed/lost/unknown do not.
func isLiveAcceptanceStatus(status string) bool {
	switch status {
	case "pending", "starting", "running", "stopping":
		return true
	default:
		return false
	}
}

type fakeAcceptanceAPIClient struct {
	manmanpb.ManManAPIClient

	mu sync.Mutex

	serverID      int64
	sgcs          map[int64]*manmanpb.ServerGameConfig
	sessions      map[int64]*fakeSession // keyed by ServerGameConfigId
	nextSessionID int64
	launchFails   map[int64]bool

	startCalls   []*manmanpb.StartSessionRequest
	stopCalls    []*manmanpb.StopSessionRequest
	restartCalls []*manmanpb.RestartDeploymentRequest
	callOrder    []string // "start" / "stop" / "restart", in call order
}

func newFakeAcceptanceClient(serverID int64) *fakeAcceptanceAPIClient {
	return &fakeAcceptanceAPIClient{
		serverID:    serverID,
		sgcs:        make(map[int64]*manmanpb.ServerGameConfig),
		sessions:    make(map[int64]*fakeSession),
		launchFails: make(map[int64]bool),
	}
}

// addSGC registers a deployment with no session yet ("never started").
func (f *fakeAcceptanceAPIClient) addSGC(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sgcs[id] = &manmanpb.ServerGameConfig{ServerGameConfigId: id, ServerId: f.serverID, Status: "active"}
}

// seedSession gives a registered SGC an initial session at the given
// status, standing in for a deployment's state before the test's action
// under test runs.
func (f *fakeAcceptanceAPIClient) seedSession(sgcID int64, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSessionID++
	f.sessions[sgcID] = &fakeSession{id: f.nextSessionID, sgcID: sgcID, status: status, startedAt: f.nextSessionID}
}

// setLaunchFails configures an SGC so that its next Start's pending session
// converges (via Tick) to "crashed" instead of "running" -- FR8's "the fake
// fails the container launch" scenario.
func (f *fakeAcceptanceAPIClient) setLaunchFails(sgcID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launchFails[sgcID] = true
}

// Tick advances every currently-transient session one lifecycle step. See
// the file header comment for how this differs from the LiveOnly-path
// auto-converge used by restartDeployment's own internal wait loop.
func (f *fakeAcceptanceAPIClient) Tick() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for sgcID, sess := range f.sessions {
		if sess == nil {
			continue
		}
		switch sess.status {
		case "pending", "starting":
			if f.launchFails[sgcID] {
				sess.status = "crashed"
			} else {
				sess.status = "running"
			}
		case "stopping":
			sess.status = "stopped"
			sess.stopChecksLeft = 0
		}
	}
}

func (f *fakeAcceptanceAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &manmanpb.ListServersResponse{Servers: []*manmanpb.Server{
		{ServerId: f.serverID, Name: "Acceptance Server", Status: "online"},
	}}, nil
}

func (f *fakeAcceptanceAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*manmanpb.ServerGameConfig
	for _, sgc := range f.sgcs {
		if in.ServerId != 0 && sgc.ServerId != in.ServerId {
			continue
		}
		out = append(out, sgc)
	}
	return &manmanpb.ListServerGameConfigsResponse{Configs: out}, nil
}

func (f *fakeAcceptanceAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sgc, ok := f.sgcs[in.ServerGameConfigId]
	if !ok {
		return nil, fmt.Errorf("rpc error: code = NotFound desc = server_game_config %d not found", in.ServerGameConfigId)
	}
	return &manmanpb.GetServerGameConfigResponse{Config: sgc}, nil
}

// GetGameConfig always errors: display-name resolution isn't what this
// suite guards (handlers_sessions_deployment_row_test.go's
// fakeSessionsAPIClient does the same), so every row falls back to the
// existing "SGC %d" display name.
func (f *fakeAcceptanceAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return nil, fmt.Errorf("no game config configured for id %d", in.ConfigId)
}

// ListSessions serves all three request shapes this app makes
// (ServerId-scoped "all sessions" for deployment rows, LiveOnly-scoped live
// session lookups, and the page's own filtered session list) generically,
// by filtering the same underlying per-SGC session storage rather than
// dispatching on magic PageSize values -- so every query shape a real
// server would answer consistently, since they all read the same ground
// truth.
func (f *fakeAcceptanceAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []*manmanpb.Session
	for sgcID, sess := range f.sessions {
		if sess == nil {
			continue
		}
		if in.ServerGameConfigId != 0 && sgcID != in.ServerGameConfigId {
			continue
		}
		if in.ServerId != 0 {
			sgc, ok := f.sgcs[sgcID]
			if !ok || sgc.ServerId != in.ServerId {
				continue
			}
		}
		if len(in.StatusFilter) > 0 {
			matched := false
			for _, s := range in.StatusFilter {
				if s == sess.status {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if !in.LiveOnly {
			out = append(out, sess.toProto())
			continue
		}

		// LiveOnly path: a "stopping" session auto-converges to "stopped"
		// after one more observation, modeling a container that takes a
		// moment to actually tear down rather than disappearing from the
		// live listing the instant StopSession acks.
		if sess.status == "stopping" {
			if sess.stopChecksLeft > 0 {
				sess.stopChecksLeft--
			} else {
				sess.status = "stopped"
			}
		}
		if isLiveAcceptanceStatus(sess.status) {
			out = append(out, sess.toProto())
		}
	}
	return &manmanpb.ListSessionsResponse{Sessions: out}, nil
}

func (f *fakeAcceptanceAPIClient) StartSession(ctx context.Context, in *manmanpb.StartSessionRequest, opts ...grpc.CallOption) (*manmanpb.StartSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.startCalls = append(f.startCalls, in)
	f.callOrder = append(f.callOrder, "start")

	f.nextSessionID++
	sess := &fakeSession{id: f.nextSessionID, sgcID: in.ServerGameConfigId, status: "pending", startedAt: f.nextSessionID}
	f.sessions[in.ServerGameConfigId] = sess

	if f.launchFails[in.ServerGameConfigId] {
		// The launch was actually attempted (the pending session above is
		// real, observable state -- FR8's point is that the row must show
		// what was actually observed, not an assumed outcome either way),
		// but the RPC itself comes back ambiguous/failed, exercising
		// deploymentStartErrorMessage's inline-error path immediately.
		return nil, errors.New("failed to start session: rpc error: container launch did not confirm")
	}
	return &manmanpb.StartSessionResponse{Session: sess.toProto()}, nil
}

func (f *fakeAcceptanceAPIClient) StopSession(ctx context.Context, in *manmanpb.StopSessionRequest, opts ...grpc.CallOption) (*manmanpb.StopSessionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.stopCalls = append(f.stopCalls, in)
	f.callOrder = append(f.callOrder, "stop")

	for _, sess := range f.sessions {
		if sess.id == in.SessionId {
			sess.status = "stopping"
			sess.stopChecksLeft = 1
			return &manmanpb.StopSessionResponse{Session: sess.toProto()}, nil
		}
	}
	return &manmanpb.StopSessionResponse{}, nil
}

// RestartDeployment is the fake's #1733 counterpart to StopSession/
// StartSession above: since restart no longer orchestrates stop-then-start
// from the UI at all (that now lives entirely in control-api's own
// consumer, #1730/#1731, which this suite does not model), this simply
// records the dispatch and acks -- there is nothing left for this fake to
// converge, since the UI holds no restart state across the request/response
// boundary any more.
func (f *fakeAcceptanceAPIClient) RestartDeployment(ctx context.Context, in *manmanpb.RestartDeploymentRequest, opts ...grpc.CallOption) (*manmanpb.RestartDeploymentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.restartCalls = append(f.restartCalls, in)
	f.callOrder = append(f.callOrder, "restart")

	return &manmanpb.RestartDeploymentResponse{}, nil
}

// ListPendingRestarts is the fake's #1735 counterpart: this suite does not
// model in-flight/failed/expired pending_restarts rows (that is
// session_test.go's/restart_state_test.go's job), so it always reports "no
// restart record" for every requested SGC -- an empty response, matching
// control-api's own contract for a caller that queries any SGC set.
func (f *fakeAcceptanceAPIClient) ListPendingRestarts(ctx context.Context, in *manmanpb.ListPendingRestartsRequest, opts ...grpc.CallOption) (*manmanpb.ListPendingRestartsResponse, error) {
	return &manmanpb.ListPendingRestartsResponse{}, nil
}

// newAcceptanceFixture builds an App wired to a fresh fakeAcceptanceAPIClient
// and a real *http.ServeMux from (*App).setupRoutes, with a real
// htmxauth.Authenticator in AuthModeNone (auto-authenticates every request,
// mirroring local dev) so every test here can hit routes through the actual
// mux/auth stack without constructing a session cookie.
func newAcceptanceFixture(t *testing.T) (*App, *fakeAcceptanceAPIClient, *http.ServeMux) {
	t.Helper()

	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "acceptance-test-secret-at-least-32-bytes-long",
		SessionName:   "manmanv2_ui_acceptance_test_session",
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}

	api := newFakeAcceptanceClient(1)
	app := &App{
		auth: auth,
		grpc: &ControlClient{api: api},
	}

	mux := http.NewServeMux()
	app.setupRoutes(mux)

	return app, api, mux
}

func doGet(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func doPost(mux *http.ServeMux, path string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// ── FR1 ──────────────────────────────────────────────────────────────────

func TestFR1_StartOfferedOnlyOnStoppedCrashedLost(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const (
		sgcStopped = 101
		sgcCrashed = 102
		sgcLost    = 103
		sgcNever   = 104
	)
	for _, id := range []int64{sgcStopped, sgcCrashed, sgcLost, sgcNever} {
		api.addSGC(id)
	}
	api.seedSession(sgcStopped, "stopped")
	api.seedSession(sgcCrashed, "crashed")
	api.seedSession(sgcLost, "lost")
	// sgcNever: never started, no session seeded.

	w := doGet(mux, "/sessions?server_id=1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, id := range []int64{sgcStopped, sgcCrashed, sgcLost} {
		section := deploymentRowSection(t, body, id)
		if !strings.Contains(section, ">Start<") {
			t.Errorf("SGC %d: expected Start offered, got %q", id, section)
		}
	}

	neverSection := deploymentRowSection(t, body, sgcNever)
	if strings.Contains(neverSection, ">Start<") {
		t.Errorf("never-started SGC %d: expected no Start action (deploy-and-start is out of scope for M2), got %q", sgcNever, neverSection)
	}
}

// ── FR2 ──────────────────────────────────────────────────────────────────

func TestFR2_StartFromListStartsSessionWithoutNavigation(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const sgcID = 201
	api.addSGC(sgcID)
	api.seedSession(sgcID, "stopped")

	w := doPost(mux, fmt.Sprintf("/sessions/deployments/%d/start", sgcID), true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	if len(api.startCalls) != 1 {
		t.Fatalf("StartSession call count = %d, want 1", len(api.startCalls))
	}
	got := api.startCalls[0]
	if got.ServerGameConfigId != sgcID {
		t.Errorf("StartSessionRequest.ServerGameConfigId = %d, want %d", got.ServerGameConfigId, sgcID)
	}
	if got.Force {
		t.Errorf("StartSessionRequest.Force = true, want false")
	}

	body := w.Body.String()
	if !strings.Contains(body, fmt.Sprintf(`id="deployment-row-%d"`, sgcID)) {
		t.Fatalf("expected a row fragment response for SGC %d, got %q", sgcID, body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<body") {
		t.Errorf("expected a bare row fragment (no page navigation), got %q", body)
	}
	if w.Header().Get("HX-Redirect") != "" {
		t.Errorf("expected no HX-Redirect header, got %q", w.Header().Get("HX-Redirect"))
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("expected no Location header (no navigation away from the list), got %q", loc)
	}
}

// ── FR3 ──────────────────────────────────────────────────────────────────

func TestFR3_StopOfferedOnlyWithLiveRunningSession(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const (
		sgcRunning = 301
		sgcStopped = 302
		sgcCrashed = 303
		sgcLost    = 304
		sgcNever   = 305
	)
	for _, id := range []int64{sgcRunning, sgcStopped, sgcCrashed, sgcLost, sgcNever} {
		api.addSGC(id)
	}
	api.seedSession(sgcRunning, "running")
	api.seedSession(sgcStopped, "stopped")
	api.seedSession(sgcCrashed, "crashed")
	api.seedSession(sgcLost, "lost")

	body := doGet(mux, "/sessions?server_id=1").Body.String()

	runningSection := deploymentRowSection(t, body, sgcRunning)
	if !strings.Contains(runningSection, ">Stop<") {
		t.Errorf("running SGC %d: expected Stop offered, got %q", sgcRunning, runningSection)
	}

	for _, id := range []int64{sgcStopped, sgcCrashed, sgcLost, sgcNever} {
		section := deploymentRowSection(t, body, id)
		if strings.Contains(section, ">Stop<") {
			t.Errorf("SGC %d: expected no Stop action without a live running session, got %q", id, section)
		}
	}
}

// ── FR4 ──────────────────────────────────────────────────────────────────

func TestFR4_StopConfirmsThenStops(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const sgcID = 401
	api.addSGC(sgcID)
	api.seedSession(sgcID, "running")

	listBody := doGet(mux, "/sessions?server_id=1").Body.String()
	section := deploymentRowSection(t, listBody, sgcID)
	if !strings.Contains(section, `@click="confirmStop = true"`) {
		t.Fatalf("expected the click-to-reveal Stop confirm gate, got %q", section)
	}
	if !strings.Contains(section, "Yes, Stop") {
		t.Errorf("expected a 'Yes, Stop' confirm button behind the gate, got %q", section)
	}

	api.mu.Lock()
	liveID := api.sessions[sgcID].id
	api.mu.Unlock()

	w := doPost(mux, fmt.Sprintf("/sessions/deployments/%d/stop", sgcID), true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.stopCalls) != 1 {
		t.Fatalf("StopSession call count = %d, want 1", len(api.stopCalls))
	}
	if api.stopCalls[0].SessionId != liveID {
		t.Errorf("StopSession called with SessionId = %d, want the live session's id %d", api.stopCalls[0].SessionId, liveID)
	}
}

// ── FR5 ──────────────────────────────────────────────────────────────────

func TestFR5_RestartOfferedOnRunningCrashedLost(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const (
		sgcRunning = 501
		sgcCrashed = 502
		sgcLost    = 503
		sgcStopped = 504
		sgcNever   = 505
	)
	for _, id := range []int64{sgcRunning, sgcCrashed, sgcLost, sgcStopped, sgcNever} {
		api.addSGC(id)
	}
	api.seedSession(sgcRunning, "running")
	api.seedSession(sgcCrashed, "crashed")
	api.seedSession(sgcLost, "lost")
	api.seedSession(sgcStopped, "stopped")

	body := doGet(mux, "/sessions?server_id=1").Body.String()

	for _, id := range []int64{sgcRunning, sgcCrashed, sgcLost} {
		section := deploymentRowSection(t, body, id)
		if !strings.Contains(section, ">Restart<") {
			t.Errorf("SGC %d: expected Restart offered, got %q", id, section)
		}
	}
	for _, id := range []int64{sgcStopped, sgcNever} {
		section := deploymentRowSection(t, body, id)
		if strings.Contains(section, ">Restart<") {
			t.Errorf("SGC %d: expected no Restart action, got %q", id, section)
		}
	}
}

// ── FR6 ──────────────────────────────────────────────────────────────────

// TestFR6_RestartDispatchesRestartDeploymentOnly covers FR6/FR9 post-#1733:
// restart dispatches a single RestartDeployment RPC and returns as soon as
// it acks -- the UI must never call StopSession/StartSession itself for a
// restart any more, since control-api's own consumer (#1730/#1731) now owns
// the stop-then-start orchestration entirely server-side.
func TestFR6_RestartDispatchesRestartDeploymentOnly(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const sgcID = 601
	api.addSGC(sgcID)
	api.seedSession(sgcID, "running")

	w := doPost(mux, fmt.Sprintf("/sessions/deployments/%d/restart", sgcID), true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.restartCalls) != 1 {
		t.Fatalf("RestartDeployment call count = %d, want 1", len(api.restartCalls))
	}
	if got := api.restartCalls[0].ServerGameConfigId; got != sgcID {
		t.Errorf("RestartDeploymentRequest.ServerGameConfigId = %d, want %d", got, sgcID)
	}
	if len(api.stopCalls) != 0 {
		t.Errorf("StopSession call count = %d, want 0 (restart must not dispatch stop from the UI, #1733)", len(api.stopCalls))
	}
	if len(api.startCalls) != 0 {
		t.Errorf("StartSession call count = %d, want 0 (restart must not dispatch start from the UI, #1733)", len(api.startCalls))
	}
	if len(api.callOrder) != 1 || api.callOrder[0] != "restart" {
		t.Errorf("call order = %v, want [restart]", api.callOrder)
	}
}

// ── FR7 ──────────────────────────────────────────────────────────────────

func TestFR7_RowUpdatesInPlaceWithoutFullReload(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const sgcID = 701
	api.addSGC(sgcID)
	api.seedSession(sgcID, "stopped")

	startResp := doPost(mux, fmt.Sprintf("/sessions/deployments/%d/start", sgcID), true)
	if startResp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", startResp.Code, startResp.Body.String())
	}
	startBody := startResp.Body.String()
	rowMarker := fmt.Sprintf(`id="deployment-row-%d"`, sgcID)
	if !strings.Contains(startBody, rowMarker) {
		t.Fatalf("expected a row fragment, got %q", startBody)
	}
	if strings.Contains(startBody, "<html") || strings.Contains(startBody, "<body") {
		t.Errorf("expected a bare row fragment, not a full page reload, got %q", startBody)
	}
	if !strings.Contains(startBody, "pending") {
		t.Errorf("expected the freshly observed pending status, got %q", startBody)
	}
	if strings.Contains(startBody, ">Stop<") || strings.Contains(startBody, ">Restart<") {
		t.Errorf("expected no actions offered while pending (transient), got %q", startBody)
	}
	if !strings.Contains(startBody, `hx-trigger="every 3s"`) {
		t.Errorf("expected the transient row to carry the self-terminating poll trigger, got %q", startBody)
	}
	if !strings.Contains(startBody, fmt.Sprintf(`hx-get="/api/deployments/%d/row"`, sgcID)) {
		t.Errorf("expected the poll trigger to target this SGC's row-fragment endpoint, got %q", startBody)
	}

	// Poll the row fragment endpoint (mirrors the browser's hx-trigger)
	// before any tick: still pending, still a bare fragment.
	prePoll := doGet(mux, fmt.Sprintf("/api/deployments/%d/row", sgcID))
	if prePoll.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", prePoll.Code, prePoll.Body.String())
	}
	prePollBody := prePoll.Body.String()
	if !strings.Contains(prePollBody, "pending") {
		t.Errorf("expected the poll to still observe pending before the tick, got %q", prePollBody)
	}
	if strings.Contains(prePollBody, "<table") {
		t.Errorf("expected a single row fragment from the poll endpoint, not the whole table, got %q", prePollBody)
	}

	// Advance the fake's lifecycle: pending -> running.
	api.Tick()

	settledResp := doGet(mux, fmt.Sprintf("/api/deployments/%d/row", sgcID))
	if settledResp.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", settledResp.Code, settledResp.Body.String())
	}
	settledBody := settledResp.Body.String()
	if !strings.Contains(settledBody, "running") {
		t.Errorf("expected the settled row to report running after the tick, got %q", settledBody)
	}
	if strings.Contains(settledBody, "hx-trigger") {
		t.Errorf("expected the settled row to stop polling (no hx-trigger), got %q", settledBody)
	}
	if !strings.Contains(settledBody, ">Stop<") || !strings.Contains(settledBody, ">Restart<") {
		t.Errorf("expected Stop/Restart to become available once running, got %q", settledBody)
	}
}

// ── FR8 ──────────────────────────────────────────────────────────────────

func TestFR8_FailedActionShowsInlineErrorAndObservedStatus(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const sgcID = 801
	api.addSGC(sgcID)
	api.seedSession(sgcID, "stopped")
	api.setLaunchFails(sgcID)

	w := doPost(mux, fmt.Sprintf("/sessions/deployments/%d/start", sgcID), true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failure still re-renders the row); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, fmt.Sprintf(`id="deployment-row-%d"`, sgcID)) {
		t.Fatalf("expected a row fragment for SGC %d, got %q", sgcID, body)
	}
	if !strings.Contains(body, "alert-error") {
		t.Errorf("expected an inline alert-error on the immediate response, got %q", body)
	}
	if strings.Contains(body, "running") {
		t.Errorf("expected no assumed-running status on the immediate response, got %q", body)
	}

	// Advance the fake's lifecycle: the launch was actually attempted (a
	// pending session exists), but per setLaunchFails it settles crashed
	// rather than running.
	api.Tick()

	refreshed := doGet(mux, fmt.Sprintf("/api/deployments/%d/row", sgcID))
	if refreshed.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", refreshed.Code, refreshed.Body.String())
	}
	refreshedBody := refreshed.Body.String()
	if !strings.Contains(refreshedBody, "crashed") {
		t.Errorf("expected the subsequent refresh to observe crashed, got %q", refreshedBody)
	}
	if strings.Contains(refreshedBody, "running") {
		t.Errorf("expected no rendered running anywhere in this scenario, got %q", refreshedBody)
	}
	if !strings.Contains(refreshedBody, ">Start<") || !strings.Contains(refreshedBody, ">Restart<") {
		t.Errorf("expected Start+Restart offered again once crashed, got %q", refreshedBody)
	}
}

// ── NFRs ─────────────────────────────────────────────────────────────────

// TestNFR_StartHasNoConfirmation covers the NFR that Start is a single
// click with no confirm gate, contrasted directly against Stop and Restart
// which both are -- using a crashed deployment (Start + Restart both
// offered together) and a running one (Stop + Restart both offered
// together) so the same row proves the contrast rather than merely
// observing that Stop/Restart happen to be absent from a Start-only row.
func TestNFR_StartHasNoConfirmation(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const (
		sgcCrashed = 901
		sgcRunning = 902
	)
	api.addSGC(sgcCrashed)
	api.seedSession(sgcCrashed, "crashed")
	api.addSGC(sgcRunning)
	api.seedSession(sgcRunning, "running")

	body := doGet(mux, "/sessions?server_id=1").Body.String()

	crashedSection := deploymentRowSection(t, body, sgcCrashed)
	if !strings.Contains(crashedSection, ">Start<") {
		t.Fatalf("expected Start offered on a crashed deployment, got %q", crashedSection)
	}
	if !strings.Contains(crashedSection, `@click="confirmRestart = true"`) {
		t.Fatalf("expected Restart's confirm gate on a crashed deployment, got %q", crashedSection)
	}

	// Isolate Start's own <form>...</form> (from its hx-post target through
	// its own closing tag) to prove it individually carries no confirm-gate
	// markup, even though this same row's Restart control (a sibling
	// action) does.
	startTarget := fmt.Sprintf("/sessions/deployments/%d/start", sgcCrashed)
	startFormStart := strings.Index(crashedSection, startTarget)
	if startFormStart < 0 {
		t.Fatalf("expected Start's hx-post target %q in the row, got %q", startTarget, crashedSection)
	}
	startFormEnd := strings.Index(crashedSection[startFormStart:], "</form>")
	if startFormEnd < 0 {
		t.Fatalf("expected a closing </form> after Start's hx-post target, got %q", crashedSection[startFormStart:])
	}
	startForm := crashedSection[startFormStart : startFormStart+startFormEnd]
	if strings.Contains(startForm, "confirm") || strings.Contains(startForm, "Cancel") || strings.Contains(startForm, "x-show") {
		t.Errorf("expected Start's own form to carry no confirm-gate markup, got %q", startForm)
	}

	runningSection := deploymentRowSection(t, body, sgcRunning)
	if !strings.Contains(runningSection, `@click="confirmStop = true"`) {
		t.Errorf("expected Stop's confirm gate on a running deployment, got %q", runningSection)
	}
	if !strings.Contains(runningSection, `@click="confirmRestart = true"`) {
		t.Errorf("expected Restart's confirm gate on a running deployment, got %q", runningSection)
	}
}

// TestNFR_AllActionsReachableFromListWithoutDetailPage asserts every action
// control on /sessions posts directly to a /sessions/deployments/...
// endpoint -- never to a /sgc/{id} or /sessions/{id} detail-page route --
// so no one-click action requires first navigating away from the list.
func TestNFR_AllActionsReachableFromListWithoutDetailPage(t *testing.T) {
	_, api, mux := newAcceptanceFixture(t)
	const (
		sgcRunning = 1001
		sgcCrashed = 1002
		sgcStopped = 1003
	)
	api.addSGC(sgcRunning)
	api.seedSession(sgcRunning, "running")
	api.addSGC(sgcCrashed)
	api.seedSession(sgcCrashed, "crashed")
	api.addSGC(sgcStopped)
	api.seedSession(sgcStopped, "stopped")

	body := doGet(mux, "/sessions?server_id=1").Body.String()

	hxPostRe := regexp.MustCompile(`hx-post="([^"]+)"`)
	matches := hxPostRe.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		t.Fatalf("expected at least one hx-post action target on /sessions, found none")
	}
	for _, m := range matches {
		target := m[1]
		if !strings.HasPrefix(target, "/sessions/deployments/") {
			t.Errorf("action target %q is not a /sessions/deployments/... endpoint -- an action would require navigating elsewhere first", target)
		}
	}
}
