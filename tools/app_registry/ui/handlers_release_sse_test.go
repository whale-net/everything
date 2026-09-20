package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxsse"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// --- test fixtures -----------------------------------------------------

// releaseSSETestSecret/releaseSSETestSessionName are shared between the
// Authenticator under test and the standalone htmxauth.SessionManager used
// by releaseSSESessionCookie below -- both derive the same cookie-store
// HMAC key from secret+name (htmxauth.NewSessionManager), so a cookie
// minted by the standalone manager decodes cleanly inside the
// Authenticator's own internal session store.
const (
	releaseSSETestSecret      = "dev-secret-at-least-32-bytes-long-xxxx"
	releaseSSETestSessionName = "app_registry_ui_session"
)

// newReleaseSSESessionAuth builds an Authenticator with Mode left at its
// zero value (neither AuthModeNone nor AuthModeOIDC), matching
// TestHandlePromoStatusSSE_CookieAbsent_Returns401's setup -- this takes
// RequireAuth's cookie-session branch instead of the AuthModeNone bypass,
// so GetAccessToken/CurrentUser exercise the real SessionManager against a
// request's actual cookie.
func newReleaseSSESessionAuth(t *testing.T) *htmxauth.Authenticator {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		SessionSecret: releaseSSETestSecret,
		SessionName:   releaseSSETestSessionName,
	})
	if err != nil {
		t.Fatalf("failed to create authenticator: %v", err)
	}
	return auth
}

// newReleaseSSENoAuth builds an Authenticator in AUTH_MODE=none, for tests
// that only care about the handler/fragment behavior, not auth
// discrimination.
func newReleaseSSENoAuth(t *testing.T) *htmxauth.Authenticator {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: releaseSSETestSecret,
		SessionName:   releaseSSETestSessionName,
	})
	if err != nil {
		t.Fatalf("failed to create authenticator: %v", err)
	}
	return auth
}

// releaseSSESessionCookie mints a session cookie carrying exactly the given
// session values (bypassing SetUserInfo's OIDC-token requirement, which
// needs a real *oidc.IDToken this package has no way to construct) via
// htmxauth.SessionManager's exported GetSession/session.Save, so tests can
// freely control "authenticated" and "access_token" independently -- the
// two session.Values keys GetUserInfo and GetAccessToken read
// (auth.go/db_session.go's GetUserInfo checks only "authenticated";
// GetAccessToken checks only "access_token" -- letting a test construct a
// session that is authenticated but token-less, the transient class).
func releaseSSESessionCookie(t *testing.T, values map[string]interface{}) *http.Cookie {
	t.Helper()
	sm := htmxauth.NewSessionManager(releaseSSETestSecret, releaseSSETestSessionName)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	session, err := sm.GetSession(req)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	for k, v := range values {
		session.Values[k] = v
	}
	if err := session.Save(req, rec); err != nil {
		t.Fatalf("failed to save session: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("expected a session cookie to be set")
	}
	return cookies[0]
}

// --- FR9/NFR5: auth composition (401/no-redirect, missing id) ----------

// TestHandleReleaseStatusSSE_CookieAbsent_Returns401 mirrors
// TestHandlePromoStatusSSE_CookieAbsent_Returns401 (handlers_sse_test.go)
// for the release-run route: no session cookie must yield 401 with no
// Location header, no Content-Type, zero-length body, and no /auth/login
// in the response -- the noRedirectWriter guarantee (FR9, reused verbatim,
// no libs/go/htmxauth change).
func TestHandleReleaseStatusSSE_CookieAbsent_Returns401(t *testing.T) {
	auth := newReleaseSSESessionAuth(t)

	req := httptest.NewRequest(http.MethodGet, "/releases/run-1/status/sse", nil)
	req.SetPathValue("id", "run-1")
	recorder := httptest.NewRecorder()

	// Wrap the writer with noRedirectWriter before RequireAuthFunc, exactly
	// as main.go's route registration does.
	w := newNoRedirectWriter(recorder)
	authHandler := auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached without a session")
	})
	authHandler(w, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
	if loc := recorder.Header().Get("Location"); loc != "" {
		t.Errorf("expected no Location header, got %q", loc)
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "" {
		t.Errorf("expected no Content-Type header, got %q", ct)
	}
	if recorder.Body.Len() != 0 {
		t.Errorf("expected zero-length body, got %d bytes: %q", recorder.Body.Len(), recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "/auth/login") {
		t.Errorf("expected no /auth/login in body, but found it: %q", recorder.Body.String())
	}
}

// TestHandleReleaseStatusSSE_MissingID_Returns400 exercises the real
// handleReleaseStatusSSE (unlike the scaffold placeholder this replaces),
// confirming an empty {id} short-circuits with 400 before any SSE upgrade.
func TestHandleReleaseStatusSSE_MissingID_Returns400(t *testing.T) {
	auth := newReleaseSSENoAuth(t)
	app := &App{auth: auth}

	req := httptest.NewRequest(http.MethodGet, "/releases//status/sse", nil)
	// Deliberately no SetPathValue("id").
	recorder := httptest.NewRecorder()
	w := newNoRedirectWriter(recorder)

	authHandler := auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		app.handleReleaseStatusSSE(w, r)
	})
	authHandler(w, req)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

// --- FR10/NFR2/NFR6: 200, initial full-state fragment, Hub-unattached --

// TestHandleReleaseStatusSSE_Authenticated_ReturnsInitialFragment covers
// the 200 + text/event-stream + full-state-before-any-event path (FR10),
// and simultaneously NFR6: newReleaseStatusTestHub's attachFunc always
// fails (no broker reachable), yet the route must still serve normally --
// no 500, no blank page, live updates simply absent until the Hub
// attaches.
func TestHandleReleaseStatusSSE_Authenticated_ReturnsInitialFragment(t *testing.T) {
	auth := newReleaseSSENoAuth(t)
	getResp := &pb.GetReleaseResponse{
		ReleaseRunId: "run-7",
		Targets: []*pb.ReleaseRunTarget{
			{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING},
		},
	}
	rel := &fakeReleaseClient{getResp: getResp}
	app := &App{
		auth:         auth,
		registry:     &RegistryClient{Release: rel},
		sseHub:       newReleaseStatusTestHub(), // NFR6: attach always fails
		buildCommits: newBuildCommitCache(),
	}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-7/status/sse", nil)
	req.SetPathValue("id", "run-7")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	w := newNoRedirectWriter(recorder)

	authHandler := auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		app.handleReleaseStatusSSE(w, r)
	})

	done := make(chan struct{})
	go func() {
		authHandler(w, req)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if ct := recorder.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: release_run.run-7") {
		t.Errorf("expected an initial swap event on the release_run.run-7 topic before any event, body: %s", body)
	}
	if !strings.Contains(body, "platform-worker") {
		t.Errorf("expected the initial fragment to render the target, body: %s", body)
	}
}

// --- FR16: an all-terminal run still streams and heartbeats ------------

// TestHandleReleaseStatusSSE_FR16_AllTargetsTerminalStillStreams asserts
// there is no "all terminal -> close the stream" branch anywhere in the
// handler: a run whose every target is already SUCCEEDED/FAILED still
// establishes a live connection and keeps heartbeating.
func TestHandleReleaseStatusSSE_FR16_AllTargetsTerminalStillStreams(t *testing.T) {
	auth := newReleaseSSENoAuth(t)
	getResp := &pb.GetReleaseResponse{
		ReleaseRunId: "run-9",
		Targets: []*pb.ReleaseRunTarget{
			{OwnerFullName: "a", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED},
			{OwnerFullName: "b", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_FAILED},
		},
	}
	rel := &fakeReleaseClient{getResp: getResp}

	config := htmxsse.DefaultConfig()
	config.HeartbeatInterval = 20 * time.Millisecond
	config.AdvertisedRetryInterval = 5 * time.Millisecond
	hub := htmxsse.NewHub(func(ctx context.Context) (htmxsse.Transport, error) {
		return nil, fmt.Errorf("test stub: no transport")
	}, config)

	app := &App{
		auth:         auth,
		registry:     &RegistryClient{Release: rel},
		sseHub:       hub,
		buildCommits: newBuildCommitCache(),
	}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-9/status/sse", nil)
	req.SetPathValue("id", "run-9")
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	w := newNoRedirectWriter(recorder)

	authHandler := auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
		app.handleReleaseStatusSSE(w, r)
	})

	done := make(chan struct{})
	go func() {
		authHandler(w, req)
		close(done)
	}()

	// Sleep past several heartbeat intervals -- if a hidden "all terminal"
	// branch existed, it would have closed the stream well before this.
	time.Sleep(120 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("stream closed early for an all-terminal release run (FR16 violation)")
	default:
	}
	cancel()
	<-done

	body := recorder.Body.String()
	if !strings.Contains(body, "event: release_run.run-9") {
		t.Errorf("expected the initial swap event, body: %s", body)
	}
	if !strings.Contains(body, "release_run.run-9-keepalive") {
		t.Errorf("expected at least one heartbeat keepalive for the unchanged terminal state, body: %s", body)
	}
}

// --- route precedence ---------------------------------------------------

// TestReleasesRoutePrecedence asserts /releases, /releases/trigger,
// /releases/{id}, and /releases/{id}/status/sse each dispatch to their own
// intended handler through the real mux built by setupRoutes -- the
// longer /releases/{id}/status/sse pattern does not collide with the
// shorter /releases/{id} wildcard, and the two literal paths still take
// precedence over the wildcard for their exact segments.
func TestReleasesRoutePrecedence(t *testing.T) {
	auth := newReleaseSSENoAuth(t)
	rel := &fakeReleaseClient{getResp: &pb.GetReleaseResponse{ReleaseRunId: "run-1"}}
	app := &App{
		auth:         auth,
		registry:     &RegistryClient{App: &releaseAppClient{}, Release: rel},
		sseHub:       newReleaseStatusTestHub(),
		buildCommits: newBuildCommitCache(),
	}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	t.Run("/releases dispatches to release history", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/releases", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "<title>Release History") {
			t.Errorf("expected the Release History page, body: %s", w.Body.String())
		}
	})

	t.Run("/releases/trigger dispatches to release trigger", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/releases/trigger", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "<title>Trigger Release") {
			t.Errorf("expected the Trigger Release page, body: %s", w.Body.String())
		}
	})

	t.Run("/releases/{id} dispatches to release status, a plain page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/releases/run-1", nil)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "<title>Release Status") {
			t.Errorf("expected the Release Status page, body: %s", w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "text/event-stream") {
			t.Errorf("expected a plain page, got event-stream Content-Type")
		}
	})

	t.Run("/releases/{id}/status/sse dispatches to the SSE route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/releases/run-1/status/sse", nil)
		ctx, cancel := context.WithCancel(req.Context())
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			mux.ServeHTTP(w, req)
			close(done)
		}()
		time.Sleep(50 * time.Millisecond)
		cancel()
		<-done

		if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("Content-Type = %q, want text/event-stream", ct)
		}
	})
}

// --- NFR2: the fragment renders exactly pages.ReleaseStatusLiveBody ----

// TestReleaseStatusFragment_NFR2_MatchesReleaseStatusLiveBody proves the
// SSE fragment is byte-identical to the same component the initial GET
// renders -- no second, SSE-only rendering path.
//
// Red/green discipline for this test (performed manually, not committed):
// temporarily replace renderReleaseStatusFragmentComponent.Render's final
// `return pages.ReleaseStatusLiveBody(resp, commits).Render(ctx, w)` with a
// hand-rolled `io.WriteString(w, "<p>fake</p>")`, rerun this test, confirm
// it fails on the byte comparison, then revert.
func TestReleaseStatusFragment_NFR2_MatchesReleaseStatusLiveBody(t *testing.T) {
	auth := newReleaseSSENoAuth(t)
	getResp := &pb.GetReleaseResponse{
		ReleaseRunId: "run-1",
		TriggeredBy:  "alice",
		Targets: []*pb.ReleaseRunTarget{
			{OwnerFullName: "platform-worker", State: pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_SUCCEEDED, BuildId: "build-1"},
		},
	}
	rel := &fakeReleaseClient{getResp: getResp}
	artifact := &fakeArtifactClient{getBuildResps: map[string]*pb.GetBuildResponse{
		"build-1": {Build: &pb.Build{GitSha: "abc123"}},
	}}
	app := &App{
		auth:         auth,
		registry:     &RegistryClient{Release: rel, Artifact: artifact},
		buildCommits: newBuildCommitCache(),
	}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-1/status/sse", nil)
	c := renderReleaseStatusFragmentComponent{r: req, releaseRunID: "run-1", cancel: func() {}, app: app}

	var got bytes.Buffer
	if err := c.Render(context.Background(), &got); err != nil {
		t.Fatalf("fragment render failed: %v", err)
	}

	commits := app.resolveTargetCommits(context.Background(), getResp.GetTargets())
	var want bytes.Buffer
	if err := pages.ReleaseStatusLiveBody(getResp, commits).Render(context.Background(), &want); err != nil {
		t.Fatalf("ReleaseStatusLiveBody render failed: %v", err)
	}

	if got.String() != want.String() {
		t.Errorf("fragment does not equal pages.ReleaseStatusLiveBody's output\ngot:  %s\nwant: %s", got.String(), want.String())
	}
}

// --- FR9: terminal vs transient failure discrimination ------------------

// TestReleaseStatusFragment_ReacquireFails_SessionGone_Terminal drives
// ReacquireGRPCContext's failure path with no session cookie at all --
// both GetAccessToken and CurrentUser fail, which is the terminal class:
// the fragment's cancel closure must be invoked.
func TestReleaseStatusFragment_ReacquireFails_SessionGone_Terminal(t *testing.T) {
	auth := newReleaseSSESessionAuth(t)
	app := &App{auth: auth}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-1/status/sse", nil)
	// Deliberately no cookie attached.

	cancelled := false
	cancel := func() { cancelled = true }
	c := renderReleaseStatusFragmentComponent{r: req, releaseRunID: "run-1", cancel: cancel, app: app}

	var buf bytes.Buffer
	err := c.Render(context.Background(), &buf)
	if err == nil {
		t.Fatal("expected an error when the session is gone")
	}
	if !cancelled {
		t.Error("terminal class (session gone) must cancel the stream")
	}
	if buf.Len() != 0 {
		t.Errorf("expected no bytes written on a terminal error, got %q", buf.String())
	}
}

// TestReleaseStatusFragment_ReacquireFails_SessionLive_Transient drives
// ReacquireGRPCContext's failure path with an authenticated session that
// simply has no access token on record -- GetAccessToken fails but
// CurrentUser succeeds, the transient class: the fragment must return an
// error without invoking cancel.
func TestReleaseStatusFragment_ReacquireFails_SessionLive_Transient(t *testing.T) {
	auth := newReleaseSSESessionAuth(t)
	app := &App{auth: auth}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-1/status/sse", nil)
	req.AddCookie(releaseSSESessionCookie(t, map[string]interface{}{
		"authenticated": true,
		"sub":           "tester",
		// No "access_token" key: GetAccessToken fails, GetUserInfo (and so
		// CurrentUser) still succeeds.
	}))

	cancelled := false
	cancel := func() { cancelled = true }
	c := renderReleaseStatusFragmentComponent{r: req, releaseRunID: "run-1", cancel: cancel, app: app}

	var buf bytes.Buffer
	err := c.Render(context.Background(), &buf)
	if err == nil {
		t.Fatal("expected an error when the access token is missing")
	}
	if cancelled {
		t.Error("transient class (session live) must not cancel the stream")
	}
}

// TestReleaseStatusFragment_GetReleaseFails_IntactSession_Transient covers
// the handler's own second discrimination point: a GetRelease gRPC
// failure with sessionMgr left nil (the state every other test in this
// package constructs App with -- see renderReleaseStatusFragmentComponent
// .Render's nil-safe `if c.app.sessionMgr != nil` guard) always takes the
// transient branch, since there is no DB-backed session manager available
// in this package's unit tests to force a GetUserInfo failure.
func TestReleaseStatusFragment_GetReleaseFails_IntactSession_Transient(t *testing.T) {
	auth := newReleaseSSENoAuth(t)
	rel := &fakeReleaseClient{getErr: status.Error(codes.Unavailable, "registry unreachable")}
	app := &App{auth: auth, registry: &RegistryClient{Release: rel}}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-1/status/sse", nil)
	cancelled := false
	cancel := func() { cancelled = true }
	c := renderReleaseStatusFragmentComponent{r: req, releaseRunID: "run-1", cancel: cancel, app: app}

	var buf bytes.Buffer
	err := c.Render(context.Background(), &buf)
	if err == nil {
		t.Fatal("expected an error from the GetRelease failure")
	}
	if cancelled {
		t.Error("a GetRelease failure with an intact session must not cancel the stream")
	}
}

// --- FR12/NFR9: GetBuild is cached across renders -----------------------

// TestReleaseStatusFragment_FR12NFR9_SingleGetBuildAcrossNRenders
// simulates N heartbeat/event deliveries over a run with one distinct
// build_id and asserts exactly one GetBuild call is issued across all of
// them -- the process-lifetime buildCommitCache (#1703) is what keeps a
// live page's periodic re-render from re-issuing GetBuild every time.
func TestReleaseStatusFragment_FR12NFR9_SingleGetBuildAcrossNRenders(t *testing.T) {
	auth := newReleaseSSENoAuth(t)
	getResp := &pb.GetReleaseResponse{
		ReleaseRunId: "run-1",
		Targets: []*pb.ReleaseRunTarget{
			{OwnerFullName: "platform-worker", BuildId: "build-1"},
		},
	}
	rel := &fakeReleaseClient{getResp: getResp}
	artifact := &fakeArtifactClient{getBuildResps: map[string]*pb.GetBuildResponse{
		"build-1": {Build: &pb.Build{GitSha: "abc123"}},
	}}
	app := &App{
		auth:         auth,
		registry:     &RegistryClient{Release: rel, Artifact: artifact},
		buildCommits: newBuildCommitCache(),
	}

	req := httptest.NewRequest(http.MethodGet, "/releases/run-1/status/sse", nil)
	c := renderReleaseStatusFragmentComponent{r: req, releaseRunID: "run-1", cancel: func() {}, app: app}

	const n = 5
	for i := 0; i < n; i++ {
		var buf bytes.Buffer
		if err := c.Render(context.Background(), &buf); err != nil {
			t.Fatalf("render %d failed: %v", i, err)
		}
	}

	if artifact.getBuildCalls != 1 {
		t.Errorf("GetBuild calls = %d across %d renders, want exactly 1", artifact.getBuildCalls, n)
	}
}
