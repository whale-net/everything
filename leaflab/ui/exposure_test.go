package main

// Covers this issue's (#1335) Testing section, on the BFF side:
//   - a principal not on the allowlist is refused on every authenticated
//     BFF route
//   - a principal on the allowlist gets through
//   - empty/missing configuration refuses everyone (fail-closed), asserted
//     explicitly
//   - the BFF refusal renders HTML with a plain sentence, not a JSON body
//     and not an empty page
//   - the gate is applied generically (RequireExposureFunc wraps an
//     arbitrary handler, not one hardcoded to a specific route), so a
//     newly added route wrapped the same way as the existing ones
//     inherits it
//
// devAuth(t) (fakes_test.go) is used throughout to get a fixed,
// authenticated principal ("dev@localhost") into request context via the
// real RequireAuthFunc middleware -- htmxauth's user-context key is
// unexported, so this is the only way to reach RequireExposureFunc/
// exposureAllows with a real *htmxauth.UserInfo in context, exactly as
// setupRoutes wires it in production. What varies across these tests is
// the allowlist, not the user, which is sufficient to exercise every case
// this gate branches on.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "github.com/whale-net/everything/leaflab/api/proto"
)

const devUserEmail = "dev@localhost" // matches htmxauth.Authenticator.RequireAuth's AuthModeNone dev user

// --- ParseExposureAllowlist (ui) -----------------------------------------

func TestParseExposureAllowlist(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]struct{}
	}{
		{"empty string admits nobody", "", map[string]struct{}{}},
		{"whitespace-only admits nobody", "   ", map[string]struct{}{}},
		{"single entry", "alice@example.com", map[string]struct{}{"alice@example.com": {}}},
		{"multiple entries", "alice@example.com,bob@example.com", map[string]struct{}{"alice@example.com": {}, "bob@example.com": {}}},
		{"whitespace around entries is trimmed", " alice@example.com , bob@example.com ", map[string]struct{}{"alice@example.com": {}, "bob@example.com": {}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseExposureAllowlist(tc.raw)
			if len(got) != len(tc.want) {
				t.Fatalf("ParseExposureAllowlist(%q) = %v, want %v", tc.raw, got, tc.want)
			}
			for k := range tc.want {
				if _, ok := got[k]; !ok {
					t.Errorf("ParseExposureAllowlist(%q) missing key %q, got %v", tc.raw, k, got)
				}
			}
			if got == nil {
				t.Errorf("ParseExposureAllowlist(%q) returned a nil map, want a non-nil empty map (fail-closed)", tc.raw)
			}
		})
	}
}

func TestLoadExposureAllowlistFromEnv_MissingVar_RefusesEveryone(t *testing.T) {
	got := LoadExposureAllowlistFromEnv()
	if len(got) != 0 {
		t.Fatalf("LoadExposureAllowlistFromEnv() with unset env var = %v, want empty (fail-closed)", got)
	}
}

// --- exposureAllows unit tests -------------------------------------------

func TestExposureAllows(t *testing.T) {
	allowlist := map[string]struct{}{devUserEmail: {}}

	t.Run("nil user", func(t *testing.T) {
		if exposureAllows(nil, allowlist) {
			t.Error("expected exposureAllows to return false for a nil user")
		}
	})
}

// --- RequireExposureFunc: refused path -----------------------------------

// TestRequireExposureFunc_NotAllowlisted_RefusesArbitraryHandler proves
// the gate refuses generically: next is an arbitrary handler that exists
// nowhere in setupRoutes, standing in for "a newly added route" wrapped
// the same way as the existing ones (app.requireExposure). If this test
// passes, any handler wired through RequireExposureFunc inherits the gate,
// not just today's three routes.
func TestRequireExposureFunc_NotAllowlisted_RefusesArbitraryHandler(t *testing.T) {
	auth := devAuth(t)

	var reached bool
	next := func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}

	// devUserEmail deliberately absent from this allowlist.
	allowlist := map[string]struct{}{"someone-else@example.com": {}}
	handler := auth.RequireAuthFunc(RequireExposureFunc(allowlist, next))

	req := httptest.NewRequest(http.MethodGet, "/some/newly/added/route", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if reached {
		t.Fatal("wrapped handler was reached despite a non-allowlisted principal")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (FR59 permission_denied parity); body = %s", w.Code, http.StatusForbidden, w.Body.String())
	}

	body := w.Body.String()
	if len(body) == 0 {
		t.Fatal("expected a non-empty body -- never a blank page (FR59.2)")
	}
	if !strings.Contains(body, "This isn't open yet") {
		t.Errorf("expected the plain FR59.2 refusal sentence, body: %s", body)
	}
	for _, leaked := range []string{"allowlist", "LEAFLAB_UI_EXPOSURE", "env"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(leaked)) {
			t.Errorf("body leaks internal mechanism detail %q, body: %s", leaked, body)
		}
	}
}

// TestRequireExposureFunc_NotAllowlisted_ContentTypeIsHTML is the same
// refusal as the test above, but driven over a real HTTP round trip
// (httptest.NewServer, matching TestHandleHome_Unauthenticated_
// FullRedirectChainReachesLoginFormHTML's precedent in
// handlers_auth_test.go) rather than httptest.NewRecorder. A real
// net/http.Server defers writing response headers -- including sniffing
// Content-Type from the body -- until the first Write() call, regardless
// of when WriteHeader(403) was called; httptest.ResponseRecorder finalizes
// headers synchronously inside WriteHeader instead, so it cannot observe
// the sniffed Content-Type this handler relies on. Asserting over the
// wire is what actually proves FR59.2's "never a JSON body" for the
// production runtime.
func TestRequireExposureFunc_NotAllowlisted_ContentTypeIsHTML(t *testing.T) {
	auth := devAuth(t)
	next := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	allowlist := map[string]struct{}{"someone-else@example.com": {}}
	handler := auth.RequireAuthFunc(RequireExposureFunc(allowlist, next))

	mux := http.NewServeMux()
	mux.HandleFunc("/some/newly/added/route", handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/some/newly/added/route")
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(bodyBytes)

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body = %s", resp.StatusCode, http.StatusForbidden, body)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html (never a JSON body -- FR59.2)", ct)
	}
	var js map[string]any
	if err := json.Unmarshal(bodyBytes, &js); err == nil {
		t.Errorf("body parsed as JSON, want an HTML page: %s", body)
	}
	if !strings.Contains(body, "This isn't open yet") {
		t.Errorf("expected the plain FR59.2 refusal sentence, body: %s", body)
	}
}

// TestRequireExposureFunc_EmptyAllowlist_RefusesEveryone is the Testing
// section's fail-closed bullet, asserted explicitly at the BFF layer: an
// empty (or nil) allowlist -- exactly what LoadExposureAllowlistFromEnv
// returns for a missing/empty LEAFLAB_UI_EXPOSURE_ALLOWLIST -- refuses
// even the app's own fixed authenticated dev user.
func TestRequireExposureFunc_EmptyAllowlist_RefusesEveryone(t *testing.T) {
	auth := devAuth(t)

	assertRefused := func(t *testing.T, allowlist map[string]struct{}) {
		t.Helper()
		var reached bool
		next := func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(http.StatusOK) }
		handler := auth.RequireAuthFunc(RequireExposureFunc(allowlist, next))

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler(w, req)

		if reached {
			t.Fatalf("handler was reached with allowlist=%v -- fail-closed requires refusal", allowlist)
		}
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
		}
	}

	t.Run("empty map", func(t *testing.T) { assertRefused(t, map[string]struct{}{}) })
	t.Run("nil map", func(t *testing.T) { assertRefused(t, nil) })
}

// --- RequireExposureFunc: allowed path ------------------------------------

// TestRequireExposureFunc_Allowlisted_ReachesHandler is the Testing
// section's "a principal on the allowlist gets through" bullet.
func TestRequireExposureFunc_Allowlisted_ReachesHandler(t *testing.T) {
	auth := devAuth(t)

	var reached bool
	next := func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}

	allowlist := map[string]struct{}{devUserEmail: {}}
	handler := auth.RequireAuthFunc(RequireExposureFunc(allowlist, next))

	req := httptest.NewRequest(http.MethodGet, "/some/route", nil)
	w := httptest.NewRecorder()
	handler(w, req)

	if !reached {
		t.Fatal("wrapped handler was not reached despite an allowlisted principal")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

// --- end-to-end through setupRoutes ---------------------------------------

// TestSetupRoutes_NotAllowlisted_RefusesEveryProtectedRoute proves the gate
// is actually wired into every one of the app's protected routes (not just
// RequireExposureFunc in isolation): "/", "/boards" and "/boards/rows" all
// refuse a non-allowlisted dev user before reaching app.api (which is left
// nil here -- if the gate did not run first, these would panic on a nil
// api client instead of returning 403).
func TestSetupRoutes_NotAllowlisted_RefusesEveryProtectedRoute(t *testing.T) {
	app := &App{auth: devAuth(t), exposureAllowlist: map[string]struct{}{}}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	for _, path := range []string{"/", "/boards", "/boards/rows"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusForbidden, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "This isn't open yet") {
				t.Errorf("expected the plain refusal sentence, body: %s", w.Body.String())
			}
		})
	}
}

// TestSetupRoutes_Allowlisted_ReachesBoardsLandingPage proves the allowed
// path end-to-end: an allowlisted dev user reaches "/" and gets the real
// #1330 boards landing content, not the refusal page.
func TestSetupRoutes_Allowlisted_ReachesBoardsLandingPage(t *testing.T) {
	fake := &fakeLeafLabClient{healthResp: &pb.GetHealthResponse{Status: pb.HealthStatus_HEALTH_UP}}
	app := &App{
		auth:              devAuth(t),
		api:               &APIClient{LeafLab: fake},
		exposureAllowlist: map[string]struct{}{devUserEmail: {}},
	}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "Boards") {
		t.Errorf("expected the boards landing page content, body: %s", body)
	}
	if strings.Contains(body, "This isn't open yet") {
		t.Errorf("an allowlisted principal must never see the refusal page, body: %s", body)
	}
}
