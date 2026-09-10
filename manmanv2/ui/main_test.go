package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

func TestFaviconRoute(t *testing.T) {
	mux := http.NewServeMux()
	app := &App{}
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Errorf("Content-Type = %q, want image/x-icon", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q, want public, max-age=86400", got)
	}
	if len(w.Body.Bytes()) == 0 {
		t.Errorf("expected non-empty favicon body")
	}
}

// ── Guard tests: auth/authz unchanged, no deployment listing, page-load-fresh status (#1533) ──
//
// Locks in FR10, FR11, NFR2, and NFR3 as regression guards for the whole
// "gamer reaches the deployment page" milestone (#1526): a Gamer's path to
// /sgc/{id} is the same authenticated OIDC session flow as Admin's and
// Server Manager's, no token/query-param bypass exists, nothing on the
// deployment page varies by identity (there is no role model in this
// codebase today), no deployments-index nav entry exists, and the
// status/connect-address block is never served stale from a cache header or
// a background poll.
//
// Note (#2279): "/sgc/{id}" itself retired to a redirect (FR16) -- it no
// longer renders deployment content at all, for anyone, authenticated or
// not -- and "/deployments"/"/deployments/<id>" now exist as dedicated
// routes (amendment A3) rather than falling through to "/". The
// route-existence assertions below were updated for that; the
// auth/no-bypass/no-nav-entry guards they sit alongside were not, and still
// hold.
//
// Red/green discipline (verified by hand, then reverted):
//   - Temporarily moving "/sgc/" in setupRoutes outside RequireAuthFunc (so
//     it read `mux.HandleFunc("/sgc/", app.handleSGCRoutes)`) made
//     TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated's
//     "/sgc/1" subcase fail (the handler ran unauthenticated and panicked
//     reaching the nil test gRPC client, rather than being redirected
//     before ever reaching it); reverting restored green.

// newTestOIDCAuthenticator builds a real *htmxauth.Authenticator in OIDC
// mode against a throwaway discovery server, so these guard tests exercise
// the actual RequireAuthFunc/WithAccessToken session-check path rather than
// a stand-in. AuthModeNone cannot be used here: RequireAuth auto-
// authenticates every request in that mode (see htmxauth.Authenticator's
// doc comment), which would make "unauthenticated request" impossible to
// construct. htmxauth.Authenticator's config/sessions fields are
// unexported, so unlike libs/go/htmxauth's own white-box tests this cannot
// build one by struct literal from outside the package -- it has to go
// through the real constructor and OIDC discovery, hence the fake server.
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
		SessionName:      "manmanv2_ui_guard_test_session",
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
// unauthenticated/unauthorized request: a 401, or a redirect specifically
// to /auth/login. This deliberately does not treat every redirect as
// "blocked" -- /auth/login and /auth/logout themselves redirect elsewhere
// (to the provider's authorization endpoint, and to "/", respectively) as
// part of their normal public behavior, and those must not be mistaken for
// an auth gate.
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

// manmanv2PublicRoutes is the FR10/NFR2 explicit literal of every route
// setupRoutes registers outside RequireAuthFunc. Adding a public route
// requires deliberately editing this map -- that is the point.
var manmanv2PublicRoutes = map[string]bool{
	"/favicon.ico":   true,
	"/health":        true,
	"/auth/login":    true,
	"/auth/callback": true,
	"/auth/logout":   true,
}

// manmanv2RouteTable is an explicit literal mirroring every pattern
// setupRoutes registers in main.go, each given a concrete probe path that
// http.ServeMux dispatches to that pattern (patterns ending in "/" get a
// path with an extra segment to exercise the subtree match). It is
// necessarily a hand-maintained duplicate of setupRoutes' route table:
// net/http.ServeMux exposes no route-enumeration API, so there is no way to
// derive "every route that exists" from the mux object itself. Keeping this
// list exhaustive (rather than only the routes #1526 touched) is what lets
// TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated enforce "any
// other path ... must redirect" across the whole app, not just the new
// deployment-page routes.
var manmanv2RouteTable = []string{
	// Public (must match manmanv2PublicRoutes exactly).
	"/favicon.ico",
	"/health",
	"/auth/login",
	"/auth/callback",
	"/auth/logout",
	// Protected.
	"/select-server",
	"/",
	"/sessions",
	"/sessions/42",
	"/sessions/start",
	"/api/sessions/check-active",
	"/api/sessions/historical-logs",
	"/api/sessions/42",
	"/games",
	"/games/new",
	"/games/create",
	"/games/42",
	"/docs/config-strategies",
	"/servers",
	"/servers/6",
	"/servers/6/update-address", // #1528 FR4: host connect-address edit
	"/workshop/library",
	"/workshop/search",
	"/workshop/addon",
	"/workshop/library-detail",
	"/workshop/create-library",
	"/workshop/delete-library",
	"/workshop/add-addon-to-library",
	"/workshop/remove-addon-from-library",
	"/workshop/add-library-reference",
	"/workshop/remove-library-reference",
	"/workshop/installations",
	"/workshop/install",
	"/workshop/remove",
	"/workshop/reset",
	"/workshop/fetch-metadata",
	"/workshop/create-addon",
	"/workshop/update-addon-details",
	"/workshop/update-library",
	"/workshop/delete-addon",
	"/workshop/api/available-addons",
	"/workshop/api/available-libraries",
	"/workshop/api/presets-for-game",
	"/workshop/bulk-add-collection",
	"/workshop/batch-create-addons",
	"/sgc/1", // #1530-#1532's deployment page; retired to a redirect by #2279 (FR16) -- still its own auth-gated route
	"/sgc/add-library",
	"/sgc/remove-library",
	"/sgc/api/available-libraries",
	"/deployments",   // #2279 amendment A3: deployment-first name for "/sgc/"'s redirect
	"/deployments/1", // #2279 amendment A3: deployment-first name for "/sgc/<id>"'s redirect
	"/backup-configs/create",
	"/backup-configs/1",
	"/api/dashboard-summary",
	"/api/dashboard-sessions",
}

// 1. Route-table guard (FR10, NFR2).
func TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	for _, path := range manmanv2RouteTable {
		path := path
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			blocked := requestWasAuthBlocked(w)
			wantPublic := manmanv2PublicRoutes[path]
			if wantPublic && blocked {
				t.Errorf("public route %s was auth-blocked (status %d, Location %q) -- want it reachable without a session", path, w.Code, w.Header().Get("Location"))
			}
			if !wantPublic && !blocked {
				t.Errorf("protected route %s was NOT auth-blocked (status %d) -- want a redirect to /auth/login or a 401", path, w.Code)
			}
		})
	}
}

// 2. Deployment page requires auth.
func TestSGCDetailRoute_UnauthenticatedRequestHasNoDeploymentContent(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/sgc/1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !requestWasAuthBlocked(w) {
		t.Fatalf("expected /sgc/1 to be auth-blocked for an unauthenticated request, got status %d", w.Code)
	}
	body := w.Body.String()
	for _, marker := range []string{"Status &amp; Connect", "Session history", "Connect address unavailable", "play.example.com"} {
		if strings.Contains(body, marker) {
			t.Errorf("expected no deployment content (%q) in an unauthenticated response body, got %q", marker, body)
		}
	}
}

// 3. No token/query-param bypass.
func TestSGCDetailRoute_TokenQueryParamAndBearerHeaderDoNotBypassAuth(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	cases := []struct {
		name string
		req  func() *http.Request
	}{
		{
			name: "token query param",
			req: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/sgc/1?token=totally-fake-token", nil)
			},
		},
		{
			name: "bearer header, no session cookie",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/sgc/1", nil)
				r.Header.Set("Authorization", "Bearer totally-fake-token")
				return r
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, tc.req())

			if !requestWasAuthBlocked(w) {
				t.Fatalf("expected %s to be treated as unauthenticated (redirect to login or 401), got status %d", tc.name, w.Code)
			}
			if strings.Contains(w.Body.String(), "Status &amp; Connect") {
				t.Errorf("expected no deployment content to leak through via %s", tc.name)
			}
		})
	}
}

// 4. Deployment-first routes exist, but there is still no nav entry (task
// #2279, FR16/A3; NFR9 -- this task changes no nav). Before #2279,
// "/deployments" and "/deployments/" had no dedicated registration at all
// and fell through to the "/" catch-all -- that was itself a guard
// (TestNoDeploymentListingRouteOrNavEntry, superseded by this test), which
// #2279 deliberately inverts: FR16/A3 requires "/deployments" and
// "/deployments/<id>" to exist now, as the deployment-first names for the
// two retired "/sgc/..." pages (handlers_redirects_test.go covers their
// actual redirect behaviour in full). What has NOT changed is that no nav
// link points at them -- this task adds redirects, not a new nav-visible
// surface.
func TestDeploymentsRouteExistsWithNoNavEntry(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	for _, tc := range []struct{ path, wantPattern string }{
		{"/deployments", "/deployments"},
		{"/deployments/", "/deployments/"},
		{"/sgc", "/sgc/"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			_, pattern := mux.Handler(req)
			if pattern != tc.wantPattern {
				t.Errorf("expected %s to dispatch to pattern %q, got %q", tc.path, tc.wantPattern, pattern)
			}
		})
	}

	var buf strings.Builder
	if err := components.Layout(components.LayoutData{Title: "Dashboard"}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("Layout render failed: %v", err)
	}
	nav := buf.String()
	for _, needle := range []string{">Deployments<", `href="/deployments"`, `href="/sgc"`} {
		if strings.Contains(nav, needle) {
			t.Errorf("expected no deployments-index nav entry in components.Layout's emitted nav, found %q", needle)
		}
	}
}
