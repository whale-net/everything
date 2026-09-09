package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/mcpauth"
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

// stubCredentialStore is a mcpauth.CredentialStore that never actually
// mints/verifies anything -- newTestMCPProvider only needs a non-nil
// CredentialStore to satisfy mcpauth.NewProvider's construction-time
// validation (setupRoutes' route-table guard below never exercises
// /token, so no method here needs to succeed).
type stubCredentialStore struct{}

func (stubCredentialStore) Mint(ctx context.Context, identity string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("stubCredentialStore: not implemented")
}

func (stubCredentialStore) Verify(ctx context.Context, rawToken string) (string, mcpauth.Credential, error) {
	return "", mcpauth.Credential{}, errors.New("stubCredentialStore: not implemented")
}

func (stubCredentialStore) Revoke(ctx context.Context, id uuid.UUID, identity string) error {
	return errors.New("stubCredentialStore: not implemented")
}

func (stubCredentialStore) List(ctx context.Context, identity string) ([]mcpauth.Credential, error) {
	return nil, errors.New("stubCredentialStore: not implemented")
}

// newTestMCPProvider builds a real *mcpauth.Provider against loopback
// issuer/resource URLs and a resolver that never resolves (mirrors this
// scaffold's mcpCallerResolver stub, mcpauth.go), so setupRoutes' guard
// test below exercises the actual Provider.Mount route registration
// rather than a stand-in. Clients/AuthCodes are left at mcpauth's
// in-memory defaults -- fine for this route-table guard, which only
// checks whether each mcpauth path is reachable without a Keycloak
// session, never a full authorization-code exchange.
func newTestMCPProvider(t *testing.T) *mcpauth.Provider {
	t.Helper()

	p, err := mcpauth.NewProvider(mcpauth.ProviderConfig{
		Issuer:      "http://localhost",
		Resource:    "http://localhost:8082",
		Resolver:    mcpauth.CallerResolverFunc(func(r *http.Request) (string, bool) { return "", false }),
		Credentials: stubCredentialStore{},
		SignInURL:   "/login",
	})
	if err != nil {
		t.Fatalf("mcpauth.NewProvider: %v", err)
	}
	return p
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
	"/healthz":                              true,
	"/login":                                true,
	"/auth/login":                           true,
	"/auth/callback":                        true,
	"/logout":                               true,
	"/.well-known/oauth-protected-resource": true,
	"/.well-known/oauth-authorization-server": true,
	"/register":  true,
	"/authorize": true,
	"/token":     true,
}

// whagentRouteTable mirrors every pattern setupRoutes registers in
// main.go, paired with the method that actually reaches each one. "/"
// stands in for both the exact "/" route and any other unclaimed path,
// since the catch-all pattern dispatches both there.
//
// The mcpauth.Provider.Mount routes (FR9, issue #2245) need their real
// method: "/register" and "/token" are registered as "POST <path>", and
// Go's http.ServeMux (1.22+) falls through a method mismatch on an exact
// pattern to a still-matching *broader* pattern rather than 405ing --
// for these two paths that broader pattern is the "/" catch-all, wrapped
// in RequireAuthFunc, so a GET against them would be (wrongly) reported
// auth-blocked here. Using each route's real method avoids that mux
// fallthrough entirely.
var whagentRouteTable = []struct {
	method string
	path   string
}{
	// Public (path set must match whagentPublicRoutes exactly).
	{http.MethodGet, "/healthz"},
	{http.MethodGet, "/login"},
	{http.MethodGet, "/auth/login"},
	{http.MethodGet, "/auth/callback"},
	{http.MethodGet, "/logout"},
	{http.MethodGet, "/.well-known/oauth-protected-resource"},
	{http.MethodGet, "/.well-known/oauth-authorization-server"},
	{http.MethodPost, "/register"},
	{http.MethodGet, "/authorize"},
	{http.MethodPost, "/token"},
	// Protected.
	{http.MethodGet, "/"},
	{http.MethodGet, "/sessions/new"},
}

// TestSetupRoutes_OnlyExplicitPublicRoutesReachableUnauthenticated is the
// NFR1 route-table guard: every route not in whagentPublicRoutes must
// redirect an unauthenticated caller to Keycloak sign-in (via
// /auth/login), and every route in whagentPublicRoutes must not.
func TestSetupRoutes_OnlyExplicitPublicRoutesReachableUnauthenticated(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t), mcpProvider: newTestMCPProvider(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	for _, route := range whagentRouteTable {
		route := route
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			blocked := requestWasAuthBlocked(w)
			wantPublic := whagentPublicRoutes[route.path]
			if wantPublic && blocked {
				t.Errorf("public route %s was auth-blocked (status %d, Location %q) -- want it reachable without a session", route.path, w.Code, w.Header().Get("Location"))
			}
			if !wantPublic && !blocked {
				t.Errorf("protected route %s was NOT auth-blocked (status %d) -- want a redirect to /auth/login or a 401", route.path, w.Code)
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
	app := &App{auth: newTestOIDCAuthenticator(t), mcpProvider: newTestMCPProvider(t)}
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
	app := &App{mcpProvider: newTestMCPProvider(t)}
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
