package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// This file guards #1628's self-terminating row-refresh endpoint
// (handleDeploymentRowFragment, GET /api/deployments/{sgcID}/row): the
// target the row's own hx-trigger="every 3s" poll (deploymentRowPollAttrs
// in pages/sessions.templ) hits to refresh itself in place until it settles
// (FR7). It reuses fakeDeploymentAPIClient/newDeploymentTestApp/stoppedSGC
// from handlers_deployment_actions_test.go (#1627, same package) rather
// than redefining a fake -- this endpoint calls exactly the same
// buildDeploymentRowData helper the action endpoints already exercise.
//
// Unlike handleDeploymentAction, this endpoint never takes an actionErr:
// every response is a poll refresh, not an action attempt, so ActionError
// must always come back empty even if a prior action left the row showing
// one (FR8: the error belongs to a single action attempt, not to the row's
// persistent state).

// doRowFragment invokes handleDeploymentRowFragment directly (bypassing the
// mux/auth wrapping main.go's setupRoutes applies), mirroring
// doDeploymentAction's same-shape helper for the action endpoints.
func doRowFragment(app *App, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	app.handleDeploymentRowFragment(w, req)
	return w
}

// TestDeploymentRowFragment_ReturnsRowOnly covers the fragment shape: the
// body is a single <tr id="deployment-row-N"> fragment -- no <html>, no
// layout chrome, no full table -- so hx-swap="outerHTML" can drop it in
// place of the polling row.
func TestDeploymentRowFragment_ReturnsRowOnly(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "running"}},
		liveSession: &manmanpb.Session{SessionId: 999, Status: "running"},
	}
	app := newDeploymentTestApp(api)

	w := doRowFragment(app, http.MethodGet, "/api/deployments/42/row")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="deployment-row-42"`) {
		t.Fatalf("expected the row fragment for SGC 42, got: %s", body)
	}
	if strings.Contains(body, "<html") || strings.Contains(body, "<body") {
		t.Errorf("expected a bare row fragment with no layout chrome, got: %s", body)
	}
	if strings.Contains(body, "<table") {
		t.Errorf("expected a single row, not the whole GSCStatusTable, got: %s", body)
	}
}

// TestDeploymentRowFragment_TransientThenSettled covers the self-terminating
// poll loop end to end: a first call against a transient (starting) session
// includes the poll trigger, and a second call against the same SGC once
// the session has settled (running) omits it and shows Stop/Restart instead
// of the transient no-actions dash.
func TestDeploymentRowFragment_TransientThenSettled(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "starting"}},
	}
	app := newDeploymentTestApp(api)

	starting := doRowFragment(app, http.MethodGet, "/api/deployments/42/row")
	if starting.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", starting.Code, starting.Body.String())
	}
	startingBody := starting.Body.String()
	if !strings.Contains(startingBody, `hx-trigger="every 3s"`) {
		t.Errorf("expected a transient (starting) row to carry the poll trigger, got: %s", startingBody)
	}
	if !strings.Contains(startingBody, `hx-get="/api/deployments/42/row"`) {
		t.Errorf("expected the poll trigger to target this same fragment endpoint, got: %s", startingBody)
	}

	// The session has now settled to running; the same SGC's next poll
	// response must stop polling on its own.
	api.allSessions = []*manmanpb.Session{{SessionId: 999, Status: "running"}}
	api.liveSession = &manmanpb.Session{SessionId: 999, Status: "running"}

	settled := doRowFragment(app, http.MethodGet, "/api/deployments/42/row")
	if settled.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", settled.Code, settled.Body.String())
	}
	settledBody := settled.Body.String()
	if strings.Contains(settledBody, "hx-trigger") {
		t.Errorf("expected a settled (running) row to carry no hx-trigger at all, got: %s", settledBody)
	}
	if !strings.Contains(settledBody, ">Stop<") || !strings.Contains(settledBody, ">Restart<") {
		t.Errorf("expected the settled row to show Stop/Restart, got: %s", settledBody)
	}
}

// TestDeploymentRowFragment_CrashedAfterStart_ShowsStartAndRestart covers
// FR8: a start that ended in crashed renders Start + Restart, not an
// assumed success state, and stops polling (crashed is not transient).
func TestDeploymentRowFragment_CrashedAfterStart_ShowsStartAndRestart(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "crashed"}},
	}
	app := newDeploymentTestApp(api)

	w := doRowFragment(app, http.MethodGet, "/api/deployments/42/row")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, ">Start<") || !strings.Contains(body, ">Restart<") {
		t.Errorf("expected Start+Restart for a crashed deployment, got: %s", body)
	}
	if strings.Contains(body, "hx-trigger") {
		t.Errorf("expected a crashed (settled) row to carry no poll trigger, got: %s", body)
	}
}

// TestDeploymentRowFragment_NoActionError covers FR8's error-clearing rule:
// this endpoint never sets ActionError, even for a deployment whose most
// recent action attempt failed -- a poll refresh is not itself an action
// attempt.
func TestDeploymentRowFragment_NoActionError(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "starting"}},
	}
	app := newDeploymentTestApp(api)

	w := doRowFragment(app, http.MethodGet, "/api/deployments/42/row")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "alert-error") {
		t.Errorf("expected no ActionError/alert-error from a poll refresh, got: %s", w.Body.String())
	}
}

// TestDeploymentRowFragment_MethodNotAllowed covers the 405 guard: only GET
// is accepted on the row fragment route.
func TestDeploymentRowFragment_MethodNotAllowed(t *testing.T) {
	app := newDeploymentTestApp(&fakeDeploymentAPIClient{})

	w := doRowFragment(app, http.MethodPost, "/api/deployments/42/row")

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405; body: %s", w.Code, w.Body.String())
	}
}

// TestDeploymentRowFragment_BadID covers the 400 guard: both an
// unparseable ServerGameConfig id and a missing/unknown path suffix are
// rejected before any RPC is attempted.
func TestDeploymentRowFragment_BadID(t *testing.T) {
	app := newDeploymentTestApp(&fakeDeploymentAPIClient{})

	for _, path := range []string{
		"/api/deployments/not-a-number/row",
		"/api/deployments/42/notrow",
		"/api/deployments/42/",
	} {
		t.Run(path, func(t *testing.T) {
			w := doRowFragment(app, http.MethodGet, path)
			if w.Code != http.StatusBadRequest {
				t.Errorf("path %q: status = %d, want 400; body: %s", path, w.Code, w.Body.String())
			}
		})
	}
}

// TestDeploymentRowFragment_NotFound covers the 404 guard: an SGC id that
// doesn't resolve to an actual ServerGameConfig surfaces as 404, not a
// panic on a nil Config or a silently-empty row.
func TestDeploymentRowFragment_NotFound(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgcErr: errors.New("rpc error: code = NotFound desc = server_game_config not found"),
	}
	app := newDeploymentTestApp(api)

	w := doRowFragment(app, http.MethodGet, "/api/deployments/9999/row")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body: %s", w.Code, w.Body.String())
	}
}

// TestDeploymentRowFragment_DoesNotCollideWithSessionStdinRoute guards the
// "/api/deployments/" prefix against hijacking (or being hijacked by)
// "/api/sessions/"'s existing catch-all (handleSessionStdin): the two
// prefixes are registered as distinct patterns in main.go, and this test
// proves that at the mux level rather than by inspection of the
// registration code alone.
func TestDeploymentRowFragment_DoesNotCollideWithSessionStdinRoute(t *testing.T) {
	api := &fakeDeploymentAPIClient{
		sgc:         stoppedSGC(42),
		allSessions: []*manmanpb.Session{{SessionId: 999, Status: "running"}},
	}
	app := newDeploymentTestApp(api)

	stdinCalled := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sessions/", func(w http.ResponseWriter, r *http.Request) {
		stdinCalled = true
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/deployments/", app.handleDeploymentRowFragment)

	req := httptest.NewRequest(http.MethodGet, "/api/deployments/42/row", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if stdinCalled {
		t.Fatalf("expected /api/deployments/42/row to be routed to handleDeploymentRowFragment, not the /api/sessions/ catch-all")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `id="deployment-row-42"`) {
		t.Errorf("expected the row fragment for SGC 42, got: %s", w.Body.String())
	}

	// And the reverse: an existing /api/sessions/... path must still reach
	// the session-stdin catch-all, not the deployments handler.
	req2 := httptest.NewRequest(http.MethodGet, "/api/sessions/123/stdin", nil)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)
	if !stdinCalled {
		t.Fatalf("expected /api/sessions/123/stdin to still reach the session-stdin catch-all")
	}
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 from the stdin stub; body: %s", w2.Code, w2.Body.String())
	}
}
