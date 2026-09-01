package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
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
// codebase today), no deployments-index route or nav entry exists, and the
// status/connect-address block is never served stale from a cache header or
// a background poll.
//
// Red/green discipline (verified by hand, then reverted):
//   - Temporarily moving "/sgc/" in setupRoutes outside RequireAuthFunc (so
//     it read `mux.HandleFunc("/sgc/", app.handleSGCRoutes)`) made
//     TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated's
//     "/sgc/1" subcase fail (the handler ran unauthenticated and panicked
//     reaching the nil test gRPC client, rather than being redirected
//     before ever reaching it); reverting restored green.
//   - Temporarily adding `mux.HandleFunc("/deployments",
//     app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSGCRoutes)))`
//     to setupRoutes made TestNoDeploymentListingRouteOrNavEntry's
//     "/deployments" subcase fail ("/deployments" resolved to its own new
//     pattern instead of falling through to the "/" catch-all); removing it
//     restored green.

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
	"/sgc/1", // #1530/#1531/#1532: the deployment page itself
	"/sgc/add-library",
	"/sgc/remove-library",
	"/sgc/api/available-libraries",
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

// sgcStatusAndSessionRegion isolates the "Status & Connect" and "Session
// history" cards together from the rest of the (much larger) SGCDetail
// page. Mirrors pages/sgc_detail_status_connect_test.go's
// statusConnectSection and pages/sgc_detail_sessions_test.go's
// sessionHistorySection -- both unexported to package pages, so this is a
// same-purpose, same-anchor duplicate rather than a shared import.
func sgcStatusAndSessionRegion(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "Status &amp; Connect")
	if start < 0 {
		t.Fatalf("expected a 'Status & Connect' heading in rendered body, got %q", body)
	}
	end := strings.Index(body[start:], "Danger Zone: intentionally kept")
	if end < 0 {
		t.Fatalf("expected a Danger Zone marker after the Status & Connect / Session history cards, got %q", body)
	}
	return body[start : start+end]
}

func renderSGCDetailPage(t *testing.T, data pages.SGCDetailPageData) string {
	t.Helper()
	var buf strings.Builder
	if err := pages.SGCDetail(data).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return buf.String()
}

// 4. No persona branching (NFR2).
func TestSGCDetail_NoPersonaBranchingInStatusConnectAndSessionHistory(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 42,
		Status:             "active",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		},
	}
	server := &manmanpb.Server{ServerId: 1, Name: "Alpha", HostPublicAddress: "play.example.com"}
	sessions := []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "running"}}

	latest := components.LatestSession(sessions)
	status := components.ComputeDeploymentStatus(latest)
	addrs := components.ComputeConnectAddresses(server.GetHostPublicAddress(), sgc.GetPortBindings())

	buildData := func(user *htmxauth.UserInfo) pages.SGCDetailPageData {
		return pages.SGCDetailPageData{
			Layout:                    components.LayoutData{Title: "SGC", User: user},
			SGC:                       sgc,
			Server:                    server,
			Sessions:                  sessions,
			DeploymentStatus:          status,
			ConnectAddresses:          addrs,
			ConnectAddressUnavailable: len(addrs) == 0,
		}
	}

	gamer := &htmxauth.UserInfo{Sub: "gamer-1", PreferredUsername: "gamer", Name: "Gamer One", Roles: []string{}}
	admin := &htmxauth.UserInfo{Sub: "admin-1", PreferredUsername: "admin", Name: "Admin One", Roles: []string{"admin", "server-manager"}}

	gamerRegion := sgcStatusAndSessionRegion(t, renderSGCDetailPage(t, buildData(gamer)))
	adminRegion := sgcStatusAndSessionRegion(t, renderSGCDetailPage(t, buildData(admin)))

	if gamerRegion != adminRegion {
		t.Fatalf("status/connect-address/session-history regions differ by identity -- there is no role model in this codebase today, so this page must render identically regardless of who is looking at it.\ngamer region:\n%s\nadmin region:\n%s", gamerRegion, adminRegion)
	}
}

// 5. No deployment listing (FR11).
func TestNoDeploymentListingRouteOrNavEntry(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	// "/deployments", "/deployments/", and "/sgc" (no trailing slash) have
	// no dedicated registration in setupRoutes -- so this checks the
	// *pattern* http.ServeMux (1.22+) would dispatch each of them to, via
	// mux.Handler, rather than executing a handler. That is deliberate: an
	// unauthenticated probe (see requestWasAuthBlocked elsewhere in this
	// file) can't distinguish "no distinct route exists" from "a distinct
	// route exists but is also behind RequireAuthFunc" -- both redirect to
	// login identically -- so it would not go red if a
	// `mux.HandleFunc("/deployments", ...)` registration were added (the
	// red/green case this guards). Resolving the pattern directly does:
	// "/deployments" and "/deployments/" must still resolve to "/" (the
	// registered catch-all subtree pattern, which every unclaimed path
	// falls through to -- not a not-found route, not a deployments-index
	// route), and "/sgc" must resolve to "/sgc/" (the existing
	// per-deployment detail route, not a distinct listing pattern).
	for _, tc := range []struct{ path, wantPattern string }{
		{"/deployments", "/"},
		{"/deployments/", "/"},
		{"/sgc", "/sgc/"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			_, pattern := mux.Handler(req)
			if pattern != tc.wantPattern {
				t.Errorf("expected %s to dispatch to pattern %q, got %q -- a different pattern here means a deployments-index route now exists", tc.path, tc.wantPattern, pattern)
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

// 6. Freshness (NFR3): no stale-cache header on the rendered response, and
// no background-poll trigger on the status/connect-address region.
func TestSGCDetailPage_NoStaleCacheHeaderOnRender(t *testing.T) {
	data := pages.SGCDetailPageData{
		Layout: components.LayoutData{Title: "SGC 1"},
		SGC:    &manmanpb.ServerGameConfig{ServerGameConfigId: 1, Status: "active"},
	}

	req := httptest.NewRequest(http.MethodGet, "/sgc/1", nil)
	w := httptest.NewRecorder()
	if err := RenderTempl(w, req, "SGC 1", pages.SGCDetail(data)); err != nil {
		t.Fatalf("RenderTempl failed: %v", err)
	}

	if cc := w.Header().Get("Cache-Control"); cc != "" {
		t.Errorf("expected no Cache-Control header on the deployment page response (NFR3: status/address must be page-load-fresh, not stale-cacheable), got %q", cc)
	}
}

func TestSGCDetail_StatusConnectRegionHasNoBackgroundPollTrigger(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 1,
		Status:             "active",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		},
	}
	server := &manmanpb.Server{ServerId: 1, Name: "Alpha", HostPublicAddress: "play.example.com"}
	sessions := []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "running"}}
	latest := components.LatestSession(sessions)
	status := components.ComputeDeploymentStatus(latest)
	addrs := components.ComputeConnectAddresses(server.GetHostPublicAddress(), sgc.GetPortBindings())

	data := pages.SGCDetailPageData{
		Layout:                    components.LayoutData{Title: "SGC"},
		SGC:                       sgc,
		Server:                    server,
		Sessions:                  sessions,
		DeploymentStatus:          status,
		ConnectAddresses:          addrs,
		ConnectAddressUnavailable: len(addrs) == 0,
	}

	region := sgcStatusAndSessionRegion(t, renderSGCDetailPage(t, data))
	if strings.Contains(region, `hx-trigger="every`) {
		t.Errorf("expected no hx-trigger=\"every ...\" background poll on the status/connect-address region (NFR3), got %q", region)
	}
}
