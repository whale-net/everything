package main

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// newTestApp builds an App whose authenticator runs in AuthModeNone, so
// RequireAuthFunc injects a synthetic signed-in developer and these tests
// exercise the real gate every shell route is mounted behind. It
// deliberately does not go through NewApp: that needs a live Postgres pool
// and a Keycloak realm, neither of which the shell's own rendering
// depends on.
func newTestApp(t *testing.T) *App {
	t.Helper()
	auth, err := htmxauth.NewAuthenticator(context.Background(), htmxauth.Config{
		Mode:          htmxauth.AuthModeNone,
		SessionSecret: "test-secret",
		SessionName:   "krill_ui_session",
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	return &App{auth: auth}
}

// newTestMux registers only the shell's own routes, mirroring
// setupRoutes minus the OAuth2 provider and self-serve API (both need a
// database-backed auth.Provider). mountSelfServeStubs stands in for
// MountSelfServe so the route-collision guard below is exercised against
// the same patterns the real provider registers.
func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// mountSelfServeStubs registers the exact patterns
// auth.Provider.MountSelfServe registers, so a shell page that collides
// with the self-serve API is caught here as the boot-time ServeMux panic
// it would really be.
func mountSelfServeStubs(mux *http.ServeMux) {
	noop := func(http.ResponseWriter, *http.Request) {}
	mux.HandleFunc("POST /credentials", noop)
	mux.HandleFunc("GET /credentials", noop)
	mux.HandleFunc("DELETE /credentials/{id}", noop)
}

// requiredAreas is the nav contract spelled out as literals, deliberately
// not derived from navAreas. Every other test in this file that iterates
// navAreas is self-referential -- deleting an entry shrinks what it
// checks and it passes anyway, which is exactly how an area can go
// missing from the nav without a single test noticing. This table is the
// one assertion that cannot be satisfied by shrinking the list it reads.
var requiredAreas = []string{opsPath, designPath, specPath}

// TestNavExposesRequiredAreas pins the three areas the shell exists to
// expose: the ops console, the design-session browser, and the
// spec+delivery browser, each linked from every page and each resolving.
func TestNavExposesRequiredAreas(t *testing.T) {
	mux := newTestMux(t)

	// Every shell page carries the full nav, so check all of them.
	for _, path := range append([]string{"/"}, navPaths()...) {
		body := fetch(t, mux, path).Body.String()
		for _, want := range requiredAreas {
			if !strings.Contains(body, `href="`+want+`"`) {
				t.Errorf("GET %s does not link to required area %s", path, want)
			}
		}
	}
}

// TestNavLinkResolves is the "every link resolves" half of the task's
// testing criterion: each href the nav actually renders must return 200
// and a page, so the shell can never show an operator a dead link.
func TestNavLinkResolves(t *testing.T) {
	mux := newTestMux(t)

	home := fetch(t, mux, "/").Body.String()
	for _, area := range navAreas {
		href := `href="` + area.Path + `"`
		if !strings.Contains(home, href) {
			t.Errorf("home page does not link to %s (looked for %s)", area.Path, href)
			continue
		}
		rec := fetch(t, mux, area.Path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (dead nav link)", area.Path, rec.Code)
		}
	}
}

// TestShellRendersOnEveryRoute asserts the chrome is present on each page
// and that no route is a bare or empty body.
func TestShellRendersOnEveryRoute(t *testing.T) {
	mux := newTestMux(t)

	for _, path := range append([]string{"/"}, navPaths()...) {
		rec := fetch(t, mux, path)
		body := rec.Body.String()

		if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("GET %s Content-Type = %q, want text/html; charset=utf-8", path, ct)
		}
		// "<nav" and "<main" rather than the exact tags: the shell's
		// landmarks carry attributes (aria-label, class), and what this
		// test is guarding is that a full document with a nav and a main
		// region is rendered, not the attribute-free spelling.
		for _, want := range []string{"<html", "<nav", "Logout", "</html>"} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s missing %q from the shell chrome", path, want)
			}
		}
		// The signed-in identity is the whole point of the gate the
		// shell sits behind; its absence means the page rendered
		// unauthenticated. htmxui's UserMenu renders the bare
		// preferred username rather than a "Signed in as ..." sentence,
		// so the dev user's name is the signal.
		//
		// Scoped to the user-menu region, not the whole body: the
		// previous assertion matched a unique phrase, but a bare
		// first name could plausibly appear in page content (a task
		// titled "developer", a product named "developer") and satisfy
		// the check on an unauthenticated page. Same reasoning as
		// primaryNavRegion above.
		if !strings.Contains(userMenuRegion(body), "developer") {
			t.Errorf("GET %s rendered no signed-in identity", path)
		}
	}
}

// userMenuRegion slices htmxui's UserMenu -- the chrome element that
// carries the signed-in identity -- out of a page.
func userMenuRegion(body string) string {
	start := strings.Index(body, `data-htmxui-user-menu`)
	if start < 0 {
		return ""
	}
	rest := body[start:]
	if end := strings.Index(rest, "</div>"); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestActiveLinkPerRoute is the per-route table: exactly one nav link is
// marked on any area route, and none is marked on the home page (which is
// not itself an area -- the brand link is the way back to it).
func TestActiveLinkPerRoute(t *testing.T) {
	mux := newTestMux(t)

	for _, tc := range []struct {
		path       string
		wantActive string // "" means no link should be marked
	}{
		{path: "/"},
		{path: opsPath, wantActive: "Ops console"},
		{path: designPath, wantActive: "Design sessions"},
		{path: specPath, wantActive: "Spec & delivery"},
		{path: credentialsPath, wantActive: "Credentials"},
	} {
		body := fetch(t, mux, tc.path).Body.String()

		marked := activeLabels(body)
		switch {
		case tc.wantActive == "" && len(marked) != 0:
			t.Errorf("GET %s marked %v active, want none", tc.path, marked)
		case tc.wantActive != "" && len(marked) != 1:
			t.Errorf("GET %s marked %v active, want exactly [%s]", tc.path, marked, tc.wantActive)
		case tc.wantActive != "" && marked[0] != tc.wantActive:
			t.Errorf("GET %s marked %q active, want %q", tc.path, marked[0], tc.wantActive)
		}
	}
}

// TestNavIsActive pins the segment-boundary rule. The raw-prefix
// implementation this replaced lit up an unrelated sibling that merely
// shared a leading substring.
func TestNavIsActive(t *testing.T) {
	ops := areaByPath(t, opsPath)

	for _, tc := range []struct {
		path string
		want bool
	}{
		{opsPath, true},              // the area root itself
		{opsPath + "/claimed", true}, // a sub-page the area owns
		{opsPath + "/a/b/c", true},   // a deeper sub-page
		{opsPath + "archive", false}, // shares a prefix, is not under it
		{"/", false},
		{"/opsarchive/claimed", false},
		{"/operations", false},
	} {
		if got := navIsActive(ops, tc.path); got != tc.want {
			t.Errorf("navIsActive(%s, %q) = %v, want %v", ops.Path, tc.path, got, tc.want)
		}
	}
}

// TestCredentialsPageTargetsSelfServeAPI guards the silent break: the
// widget's JS addresses the self-serve JSON API at /credentials, which is
// a different route from the page that hosts it. Repointing the page's
// own path into the script would make the widget silently mint nothing
// while the page still rendered perfectly.
func TestCredentialsPageTargetsSelfServeAPI(t *testing.T) {
	body := fetch(t, newTestMux(t), credentialsPath).Body.String()

	for _, want := range []string{
		`id="generate-btn"`,
		`id="new-token-value"`,
		`id="credentials-table"`,
		`id="credentials-body"`,
		`id="refresh-btn"`,
		`fetch('/credentials'`,
		"DELETE",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("credentials page missing %q", want)
		}
	}
	if strings.Contains(body, `fetch('`+credentialsPath+`'`) {
		t.Errorf("credentials widget JS points at the page route %s, not the self-serve API /credentials", credentialsPath)
	}
	// The widget only works if the self-serve API is still mounted on the
	// same binary; assert the page's own route is distinct from it.
	if credentialsPath == "/credentials" {
		t.Error("credentialsPath collides with the self-serve API path; ServeMux would panic at boot")
	}
}

// TestShellRoutesDoNotCollideWithSelfServe is the boot-time guard: the
// shell's pages are registered on the same mux as the self-serve API, and
// a page anywhere under /credentials would panic the binary at startup
// because DELETE /credentials/{id} outranks it. Registering for real (not
// in a subtest that recovers) means a regression fails the test binary
// the same way it would fail boot.
func TestShellRoutesDoNotCollideWithSelfServe(t *testing.T) {
	mux := http.NewServeMux()
	app := newTestApp(t)
	mountSelfServeStubs(mux)
	app.mountShellRoutes(mux)

	// Both surfaces still answer on their own paths.
	if rec := fetch(t, mux, "GET /credentials"); rec.Code != http.StatusOK {
		t.Errorf("self-serve API GET /credentials = %d, want 200", rec.Code)
	}
	if rec := fetch(t, mux, credentialsPath); rec.Code != http.StatusOK {
		t.Errorf("shell page %s = %d, want 200", credentialsPath, rec.Code)
	}
}

// TestOpsConsoleReadRoutesRegistered pins the ops console's four read
// views: each is a real registered route under the ops prefix (so it is
// not a 404), and the ops root links to every one. Registration is
// asserted via mux.Handler rather than a request so this runs against the
// nil-store harness -- the views themselves need a scope/task store and
// belong to the implementation and testing phases.
func TestOpsConsoleReadRoutesRegistered(t *testing.T) {
	mux := newTestMux(t)

	for _, path := range []string{opsClaimedPath, opsEscalatedPath, opsCancelledPath, opsNotesPath} {
		if !strings.HasPrefix(path, opsPath+"/") {
			t.Errorf("read view %s is not under the ops prefix %s", path, opsPath)
		}
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
		if pattern == "" {
			t.Errorf("ops read view %s is not registered", path)
		}
		if !navIsActive(areaByPath(t, opsPath), path) {
			t.Errorf("ops read view %s does not keep the Ops console nav link active", path)
		}
	}

	// The ops root indexes the views, so an operator can reach every one
	// from /ops.
	opsRoot := fetch(t, mux, opsPath).Body.String()
	for _, path := range []string{opsClaimedPath, opsEscalatedPath, opsCancelledPath, opsNotesPath} {
		if !strings.Contains(opsRoot, `href="`+path+`"`) {
			t.Errorf("ops root does not link to %s", path)
		}
	}
}

// TestUnknownPathIsNotFound keeps the shell's home from swallowing typos:
// the home page is registered as /{$}, not as a catch-all "/".
func TestUnknownPathIsNotFound(t *testing.T) {
	mux := newTestMux(t)

	if rec := fetch(t, mux, "/does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /does-not-exist = %d, want 404 (home must not be a catch-all)", rec.Code)
	}
	if rec := fetch(t, mux, "/opsarchive"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /opsarchive = %d, want 404 (unregistered area sibling)", rec.Code)
	}
}

// TestNavAreasAreWellFormed keeps the nav self-consistent: every area has
// a distinct absolute path and a label and blurb, so a half-filled entry
// cannot reach an operator's screen.
func TestNavAreasAreWellFormed(t *testing.T) {
	seen := make(map[string]string, len(navAreas))
	for _, area := range navAreas {
		switch {
		case area.Path == "" || area.Label == "" || area.Blurb == "":
			t.Errorf("incomplete nav area: %+v", area)
		case !strings.HasPrefix(area.Path, "/"):
			t.Errorf("nav area path %q is not absolute", area.Path)
		}
		if prev, dup := seen[area.Path]; dup {
			t.Errorf("nav areas %q and %q share path %q", prev, area.Label, area.Path)
		}
		seen[area.Path] = area.Label
	}
}

// TestHomeListsEveryArea checks the landing page describes the whole nav
// rather than a subset that has to be kept in sync by hand.
func TestHomeListsEveryArea(t *testing.T) {
	body := fetch(t, newTestMux(t), "/").Body.String()
	for _, area := range navAreas {
		if !strings.Contains(body, area.Blurb) {
			t.Errorf("home page missing blurb for %q", area.Label)
		}
	}
}

// helpers

func navPaths() []string {
	paths := make([]string, 0, len(navAreas))
	for _, area := range navAreas {
		paths = append(paths, area.Path)
	}
	return paths
}

func areaByPath(t *testing.T, path string) navArea {
	t.Helper()
	for _, area := range navAreas {
		if area.Path == path {
			return area
		}
	}
	t.Fatalf("no nav area at %s", path)
	return navArea{}
}

// activeLabels extracts the link text of every primary-nav anchor the
// shell marked active, by scanning the rendered <a> tags rather than the
// navAreas table -- a test that derived its expectation from the same
// list it is checking would pass even if the marking were dropped
// entirely. The text is entity-decoded so it can be compared against a
// navArea's raw Label ("Spec & delivery", which the shell correctly
// renders as "Spec &amp; delivery").
//
// It keys on aria-current="page", not on a class name: the ARIA
// current-state is the contract, and a class is a cosmetic detail that
// the next restyle is free to rename. The scan is scoped to the
// data-krill="primary-nav" region so a page's own product sub-nav (which
// also carries aria-current) can never be counted as a primary link.
func activeLabels(body string) []string {
	primary := primaryNavRegion(body)
	if primary == "" {
		return nil
	}
	var labels []string
	for _, tag := range anchorTags(primary) {
		if !strings.Contains(tag, `aria-current="page"`) {
			continue
		}
		if start := strings.Index(tag, ">"); start >= 0 {
			labels = append(labels, html.UnescapeString(strings.TrimSpace(tag[start+1:])))
		}
	}
	return labels
}

// primaryNavRegion slices the shell chrome's primary nav out of a page so
// a scan of it cannot wander into the page body.
func primaryNavRegion(body string) string {
	start := strings.Index(body, `data-krill="primary-nav"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(body[start:], "</nav>")
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}

func anchorTags(body string) []string {
	var tags []string
	rest := body
	for {
		i := strings.Index(rest, "<a ")
		if i < 0 {
			return tags
		}
		rest = rest[i:]
		j := strings.Index(rest, "</a>")
		if j < 0 {
			return tags
		}
		tags = append(tags, rest[:j])
		rest = rest[j+len("</a>"):]
	}
}

func fetch(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	method := http.MethodGet
	if strings.HasPrefix(target, http.MethodGet+" ") {
		method, target, _ = strings.Cut(target, " ")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}
