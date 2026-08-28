package main

// Covers this issue's (#1329) Testing section:
//   - unauthenticated request to an authenticated route redirects to login
//   - authenticated request renders the home page
//   - an expired/invalid session returns HTML containing the login form
//     (the full redirect chain: leaflab-ui -> /auth/login -> the IdP's
//     hosted login page, faked by newFakeOIDCServer)
//   - API returning Unavailable (or HEALTH_DEGRADED) renders the degraded
//     page, never a 500 with a Go error string
//
// Session persistence (survives a store reopen) and sign-out (deletes the
// DB session row) are covered by session_integration_test.go, which needs
// a real Postgres and so is gated behind the "integration" build tag —
// these tests deliberately stay Docker-free so `bazel test //leaflab/...`
// runs everywhere.

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

// --- unauthenticated -> redirect to login --------------------------------

// TestHandleHome_Unauthenticated_RedirectsToLogin proves the "/" route
// (wrapped by app.auth.RequireAuthFunc in setupRoutes) never falls through
// to handleHome without a session: no cookie at all, OIDC mode, must
// redirect (never a blank page, a raw error, or JSON -- FR13).
func TestHandleHome_Unauthenticated_RedirectsToLogin(t *testing.T) {
	oidcSrv := newFakeOIDCServer(t)
	auth := newOIDCTestAuthenticator(t, oidcSrv)

	app := &App{auth: auth}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (a redirect, not a rendered error or blank page); body = %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/auth/login?next=/" {
		t.Errorf("Location = %q, want /auth/login?next=/", got)
	}
}

// TestHandleHome_Unauthenticated_FullRedirectChainReachesLoginFormHTML
// proves the whole user-observable outcome of hitting a protected route
// with no session: following the real redirect chain from "/" (leaflab-ui's
// own /auth/login hop, then on to the IdP) lands on real HTML containing a
// login form -- a non-empty body with Content-Type text/html, never a
// blank screen, a raw error, or a JSON status body (FR13). leaflab-ui
// itself never renders a login form (HandleLogin always redirects
// straight to the IdP), so the "login page" this proves reachability to
// is necessarily the IdP's hosted page -- faked here by newFakeOIDCServer.
// The DB-backed equivalent of this same assertion for a genuinely EXPIRED
// session (a real ui_sessions row past its expires_at) lives in
// session_integration_test.go's TestExpiredSession_..., since expiry is a
// DB-backed-session concept that the cookie-only Authenticator used by fast
// tests here cannot represent.
func TestHandleHome_Unauthenticated_FullRedirectChainReachesLoginFormHTML(t *testing.T) {
	oidcSrv := newFakeOIDCServer(t)
	auth := newOIDCTestAuthenticator(t, oidcSrv)

	app := &App{auth: auth}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar}

	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("client.Get: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(bodyBytes)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("final status = %d, want %d; body = %s", resp.StatusCode, http.StatusOK, body)
	}
	if len(body) == 0 {
		t.Fatal("expected a non-empty body -- never a blank screen (FR13)")
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html (never a JSON status body -- FR13)", ct)
	}
	if !strings.Contains(body, "<form") {
		t.Errorf("expected the response to contain a login form, body: %s", body)
	}
}

// --- authenticated -> renders home page -----------------------------------

// TestHandleHome_Authenticated_HealthUp_RendersHomePage proves the
// login -> session -> protected-route path renders real content (never a
// blank body) once leaflab-api reports itself healthy.
func TestHandleHome_Authenticated_HealthUp_RendersHomePage(t *testing.T) {
	fake := &fakeLeafLabClient{healthResp: &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_UP}}
	app := &App{auth: devAuth(t), api: &APIClient{LeafLab: fake}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHome))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Welcome to LeafLab") {
		t.Errorf("expected the home page content, body: %s", body)
	}
	if fake.healthCalls != 1 {
		t.Errorf("GetHealth calls = %d, want 1", fake.healthCalls)
	}
}

// --- degraded page (NFR14) -------------------------------------------------

// TestHandleHome_APIHealthDegraded_RendersDegradedPage proves a
// HEALTH_DEGRADED response resolves to the "our problem, not yours" page
// (NFR14), not the normal home content and not an HTTP error status.
func TestHandleHome_APIHealthDegraded_RendersDegradedPage(t *testing.T) {
	fake := &fakeLeafLabClient{healthResp: &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_DEGRADED}}
	app := &App{auth: devAuth(t), api: &APIClient{LeafLab: fake}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHome))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (the degraded page renders normally, it is not a 500) ; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "We're having trouble right now") {
		t.Errorf("expected the degraded page content, body: %s", body)
	}
	if strings.Contains(body, "Welcome to LeafLab") {
		t.Errorf("a degraded API must never render as the normal home page, body: %s", body)
	}
}

// TestHandleHome_APIUnavailable_RendersDegradedPageWithoutLeakingErrorDetail
// proves a dial/RPC failure (e.g. codes.Unavailable) resolves to the same
// honest "our problem" page, not a 500 with a raw Go error string --
// FR63.2/NFR14: the rendered page carries no dependency name, address, or
// status code, only "our problem, not yours".
func TestHandleHome_APIUnavailable_RendersDegradedPageWithoutLeakingErrorDetail(t *testing.T) {
	sensitive := "dial tcp 10.42.0.17:50051: connect: connection refused"
	fake := &fakeLeafLabClient{healthErr: status.Error(codes.Unavailable, sensitive)}
	app := &App{auth: devAuth(t), api: &APIClient{LeafLab: fake}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHome))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (never a raw 500) ; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "We're having trouble right now") {
		t.Errorf("expected the degraded page content, body: %s", body)
	}
	for _, leaked := range []string{sensitive, "10.42.0.17", "50051", "Unavailable", "connection refused", "rpc error"} {
		if strings.Contains(body, leaked) {
			t.Errorf("body leaked technical detail %q -- NFR14 requires only 'our problem, not yours', body: %s", leaked, body)
		}
	}
}

// TestHandleHome_GenericDialError_RendersDegradedPageWithoutLeakingErrorDetail
// covers the non-gRPC-status error path (e.g. a plain dial failure before
// any RPC even reaches the wire) -- must degrade identically.
func TestHandleHome_GenericDialError_RendersDegradedPageWithoutLeakingErrorDetail(t *testing.T) {
	fake := &fakeLeafLabClient{healthErr: errors.New("dial tcp: lookup leaflab-api: no such host")}
	app := &App{auth: devAuth(t), api: &APIClient{LeafLab: fake}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHome))(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "We're having trouble right now") {
		t.Errorf("expected the degraded page content, body: %s", body)
	}
	if strings.Contains(body, "no such host") || strings.Contains(body, "leaflab-api") {
		t.Errorf("body leaked technical detail, body: %s", body)
	}
}
