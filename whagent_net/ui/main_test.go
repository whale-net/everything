package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// newTestOIDCAuthenticator builds a real *htmxauth.Authenticator in OIDC
// mode against a throwaway discovery server, so setupRoutes' guard tests
// below exercise the actual RequireAuthFunc/WithAccessToken session-check
// path rather than a stand-in. AuthModeNone cannot be used here:
// RequireAuth auto-authenticates every request in that mode (see
// htmxauth.Authenticator's doc comment), which would make "unauthenticated
// request" impossible to construct. Mirrors
// manmanv2/ui/main_test.go's identically-named helper.
func newTestOIDCAuthenticator(t *testing.T) *htmxauth.Authenticator {
	t.Helper()

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/auth",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/keys",
		})
	}))
	t.Cleanup(srv.Close)

	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:             htmxauth.AuthModeOIDC,
		SessionSecret:    "test-secret-that-is-at-least-32-bytes-long",
		SessionName:      "whagent_net_ui_guard_test_session",
		OIDCIssuer:       srv.URL,
		OIDCClientID:     "test-client",
		OIDCClientSecret: "test-client-secret",
		OIDCRedirectURL:  "http://localhost/auth/callback",
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	return auth
}

// requestWasAuthBlocked reports whether a response is what
// htmxauth.Authenticator.RequireAuth (or WithAccessToken) produces for an
// unauthenticated request: a 401, or a redirect specifically to
// /auth/login. Mirrors manmanv2/ui/main_test.go's identically-named
// helper -- deliberately not "any redirect", since /login and /logout
// themselves redirect elsewhere as part of their own normal, public
// behavior.
func requestWasAuthBlocked(w *httptest.ResponseRecorder) bool {
	if w.Code == http.StatusUnauthorized {
		return true
	}
	if w.Code >= 300 && w.Code < 400 {
		if loc := w.Header().Get("Location"); strings.HasPrefix(loc, "/auth/login") {
			return true
		}
	}
	return false
}

// whagentPublicRoutes is the NFR1 explicit literal of every route
// setupRoutes registers outside RequireAuthFunc. Adding a public route
// requires deliberately editing this map -- that is the point (mirrors
// manmanv2/ui/main_test.go's manmanv2PublicRoutes).
var whagentPublicRoutes = map[string]bool{
	"/healthz":       true,
	"/login":         true,
	"/auth/login":    true,
	"/auth/callback": true,
	"/logout":        true,
}

// whagentRouteTable mirrors every pattern setupRoutes registers in
// main.go. "/" stands in for both the exact "/" route and any other
// unclaimed path, since the catch-all pattern dispatches both there.
var whagentRouteTable = []string{
	// Public (must match whagentPublicRoutes exactly).
	"/healthz",
	"/login",
	"/auth/login",
	"/auth/callback",
	"/logout",
	// Protected.
	"/",
}

// TestSetupRoutes_OnlyExplicitPublicRoutesReachableUnauthenticated is the
// NFR1 route-table guard: every route not in whagentPublicRoutes must
// redirect an unauthenticated caller to Keycloak sign-in (via
// /auth/login), and every route in whagentPublicRoutes must not.
func TestSetupRoutes_OnlyExplicitPublicRoutesReachableUnauthenticated(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	for _, path := range whagentRouteTable {
		path := path
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			blocked := requestWasAuthBlocked(w)
			wantPublic := whagentPublicRoutes[path]
			if wantPublic && blocked {
				t.Errorf("public route %s was auth-blocked (status %d, Location %q) -- want it reachable without a session", path, w.Code, w.Header().Get("Location"))
			}
			if !wantPublic && !blocked {
				t.Errorf("protected route %s was NOT auth-blocked (status %d) -- want a redirect to /auth/login or a 401", path, w.Code)
			}
		})
	}
}

// TestSetupRoutes_UnauthenticatedIndexRequestRedirectsToLogin is the
// direct NFR1 guard this task's Testing section calls for: an
// unauthenticated request to "/" (the one app route this scaffold
// serves) must redirect to Keycloak sign-in, not render the placeholder
// index page.
//
// Red/green discipline (verified by hand, then reverted): temporarily
// registering "/" outside RequireAuthFunc/WithAccessToken in setupRoutes
// (i.e. `mux.HandleFunc("/", app.handleIndex)`) made this test fail --
// the handler ran unauthenticated instead of redirecting. Restoring the
// RequireAuthFunc(WithAccessToken(...)) wrapping made it pass again.
func TestSetupRoutes_UnauthenticatedIndexRequestRedirectsToLogin(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !requestWasAuthBlocked(w) {
		t.Fatalf("expected unauthenticated request to \"/\" to be auth-blocked, got status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "whagent-net") {
		t.Errorf("expected no index page content in an unauthenticated response body, got %q", w.Body.String())
	}
}

// TestHealthz_ReturnsOKWithoutSession proves "/healthz" is reachable with
// no auth wiring at all (app.auth is nil here), matching the chart's
// health check and Tilt, which never present a Keycloak session.
func TestHealthz_ReturnsOKWithoutSession(t *testing.T) {
	mux := http.NewServeMux()
	app := &App{}
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Errorf("expected ok status body, got %q", w.Body.String())
	}
}
