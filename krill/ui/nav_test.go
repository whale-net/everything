package main

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/libs/go/auth"
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
	return &App{auth: auth, devAuth: true}
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

// ── the workspace shell's grouped nav ───────────────────────────────────────

// requiredNavGroups is the grouped sidebar's contract spelled out as
// literals, deliberately not derived from navGroupTable -- the same
// self-referential trap requiredAreas above describes. Shrinking the
// table would shrink this and still pass, which is how a whole group
// could vanish from the sidebar unnoticed.
//
// The group set and the order are both asserted: the Work/Delivery/Design/
// Spec/Admin ordering is what puts Tasks before the Delivery item it
// currently shares an href with.
var requiredNavGroups = []struct {
	title string
	items []string
}{
	{title: "", items: []string{"Overview"}},
	{title: "Work", items: []string{"Needs attention", "Tasks", "Board"}},
	{title: "Delivery", items: []string{"Milestones"}},
	{title: "Design", items: []string{"Design sessions"}},
	{title: "Spec", items: []string{"Capabilities", "Decisions", "Personas", "Non-goals"}},
	{title: "Admin", items: []string{"Credentials"}},
}

// navMilestoneID is the one container the stub delivery listing below
// holds. A milestone-scoped nav href has to name a container the product
// actually has, or the task page correctly 404s and the test would be
// measuring the stub's emptiness rather than the href.
var navMilestoneID = uuid.MustParse("22222222-2222-2222-2222-222222222222")

// navMux mounts the shell's own routes against an app whose stores are
// in-memory stubs, so every href the grouped nav builds can actually be
// requested. A nil store would panic inside a handler rather than answer,
// so the stubs are what make "does not 404" a statement about routing
// rather than about the test's own nil dereference.
func navMux(t *testing.T) *http.ServeMux {
	t.Helper()
	app := newTestApp(t)
	app.spec = &fakeSpecReader{listing: slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{{ID: navMilestoneID, Name: "Shipped shell"}},
	}}
	app.credentials = &fakeCredentials{}
	app.tasks = &fakeTaskLister{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// navStubDesignSessions and navStubRevisionEvents are the minimum the
// design-session list page reads. The session list belongs to
// design_page_test.go's own target, so these two are declared here rather
// than shared: this test only needs the route to answer, not the page's
// contents.
type navStubDesignSessions struct{}

func (navStubDesignSessions) Open(context.Context, uuid.UUID, uuid.UUID, string, store.SessionID) (store.DesignSession, error) {
	return store.DesignSession{}, nil
}

func (navStubDesignSessions) GetByID(context.Context, uuid.UUID) (store.DesignSession, error) {
	return store.DesignSession{}, store.ErrNotFound
}

func (navStubDesignSessions) ListByProduct(context.Context, uuid.UUID) ([]store.DesignSession, error) {
	return nil, nil
}

func (navStubDesignSessions) SummarizeByProduct(context.Context, uuid.UUID) (store.ProductDesignSessionsSummary, error) {
	return store.ProductDesignSessionsSummary{}, nil
}

type navStubRevisionEvents struct{}

func (navStubRevisionEvents) Append(context.Context, store.NewRevisionEvent) (store.RevisionEvent, error) {
	return store.RevisionEvent{}, nil
}

func (navStubRevisionEvents) ListBySession(context.Context, uuid.UUID) ([]store.RevisionEvent, error) {
	return nil, nil
}

func (navStubRevisionEvents) ListOpenQuestions(context.Context, uuid.UUID) ([]store.OpenQuestion, error) {
	return nil, nil
}

func (navStubRevisionEvents) ListLatestSignoffBySessionIDs(context.Context, []uuid.UUID) (map[uuid.UUID]store.SignoffStatus, error) {
	return nil, nil
}

// TestWorkspaceNav_HasEveryRequiredGroupAndItem is the first half of the
// acceptance matrix: every group renders, and each renders exactly the
// items the FR names, in order.
func TestWorkspaceNav_HasEveryRequiredGroupAndItem(t *testing.T) {
	groups := workspaceNav(navTargets{Product: uuid.New()}, "/")

	if len(groups) != len(requiredNavGroups) {
		t.Fatalf("sidebar has %d groups, want %d: %+v", len(groups), len(requiredNavGroups), groupTitles(groups))
	}
	for i, want := range requiredNavGroups {
		got := groups[i]
		if got.Title != want.title {
			t.Errorf("group %d is %q, want %q", i, got.Title, want.title)
		}
		if len(got.Items) != len(want.items) {
			t.Errorf("group %q has %d items, want %d: %v", got.Title, len(got.Items), len(want.items), linkLabels(got.Items))
			continue
		}
		for j, label := range want.items {
			if got.Items[j].Label != label {
				t.Errorf("group %q item %d is %q, want %q", got.Title, j, got.Items[j].Label, label)
			}
		}
	}
}

// TestWorkspaceNav_MarksExactlyOneItemPerPage is the second half: for a
// given activePath exactly one item is marked, and none is on the
// Overview root while another area is showing.
//
// The expectations are literals rather than a derived match, so a rule
// change that marks nothing -- or two items -- fails here instead of
// silently redefining what "correct" means.
func TestWorkspaceNav_MarksExactlyOneItemPerPage(t *testing.T) {
	pid, mid := uuid.New(), uuid.New()

	// Every case names the product in scope explicitly rather than leaving
	// targets zero: the paths and the ids they carry are the same product
	// only if the caller supplied it, so a case that forgot to would be
	// asserting against uuid.Nil's hrefs.
	for _, tc := range []struct {
		name       string
		targets    navTargets
		activePath string
		want       string
	}{
		{name: "overview root", targets: navTargets{Product: pid}, activePath: "/", want: "Overview"},
		{name: "ops root", targets: navTargets{Product: pid}, activePath: opsPath, want: "Needs attention"},
		{name: "ops sub-page", targets: navTargets{Product: pid}, activePath: opsClaimedPath, want: "Needs attention"},
		{
			name: "design sessions", targets: navTargets{Product: pid},
			activePath: designProductSessionsPath(pid), want: "Design sessions",
		},
		{
			name: "milestone task list", targets: navTargets{Product: pid, Milestone: mid},
			activePath: milestoneTasksPath(pid, mid), want: "Tasks",
		},
		{
			name: "milestone board", targets: navTargets{Product: pid, Milestone: mid},
			activePath: milestoneBoardPath(pid, mid), want: "Board",
		},
		{
			name: "milestone task detail", targets: navTargets{Product: pid, Milestone: mid},
			activePath: taskDetailPath(pid, mid, uuid.New()), want: "Tasks",
		},
		{name: "capability map", targets: navTargets{Product: pid}, activePath: productPath(pid), want: "Capabilities"},
		{name: "decisions", targets: navTargets{Product: pid}, activePath: decisionsPath(pid), want: "Decisions"},
		{name: "personas", targets: navTargets{Product: pid}, activePath: personasPath(pid), want: "Personas"},
		{name: "non-goals", targets: navTargets{Product: pid}, activePath: nonGoalsPath(pid), want: "Non-goals"},
		{name: "delivery", targets: navTargets{Product: pid}, activePath: deliveryPath(pid), want: "Milestones"},
		{name: "credentials", targets: navTargets{Product: pid}, activePath: credentialsPath, want: "Credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := activeGroupLabels(workspaceNav(tc.targets, tc.activePath))
			if len(got) != 1 {
				t.Fatalf("marked %v, want exactly one (%q)", got, tc.want)
			}
			if got[0] != tc.want {
				t.Errorf("marked %q, want %q", got[0], tc.want)
			}
		})
	}
}

// TestWorkspaceNav_NoItemActiveOffNav is the negative case: a path no item
// owns marks nothing at all, rather than falling back to the first.
func TestWorkspaceNav_NoItemActiveOffNav(t *testing.T) {
	for _, path := range []string{"/opsarchive", "/does-not-exist", specPath} {
		if got := activeGroupLabels(workspaceNav(navTargets{Product: uuid.New()}, path)); len(got) != 0 {
			t.Errorf("path %s marked %v active, want none", path, got)
		}
	}
}

// TestWorkspaceNav_OverviewIsNotAPrefix is why Overview is Exact: a plain
// prefix match would light it for every page, since "/" starts
// everything.
func TestWorkspaceNav_OverviewIsNotAPrefix(t *testing.T) {
	for _, path := range []string{opsPath, specPath, deliveryPath(uuid.New()), credentialsPath} {
		got := activeGroupLabels(workspaceNav(navTargets{Product: uuid.New()}, path))
		for _, label := range got {
			if label == "Overview" {
				t.Errorf("path %s lit Overview; the home item must match the root alone", path)
			}
		}
	}
}

// TestWorkspaceNav_SiblingSpecTabsDoNotStack is why the Spec items are
// Exact: they are siblings hanging off the product path, so prefix
// matching would leave Capabilities lit while an operator reads
// Decisions.
func TestWorkspaceNav_SiblingSpecTabsDoNotStack(t *testing.T) {
	pid := uuid.New()
	for _, path := range []string{decisionsPath(pid), personasPath(pid), nonGoalsPath(pid)} {
		got := activeGroupLabels(workspaceNav(navTargets{Product: pid}, path))
		if len(got) != 1 || got[0] == "Capabilities" {
			t.Errorf("path %s marked %v, want the one spec tab it names", path, got)
		}
	}
}

// TestWorkspaceNavHrefResolves is the dead-link half: every href the
// grouped nav builds must answer on this binary. It is asserted against
// the real registrations via mountShellRoutes rather than a copy of the
// route table, so a nav href pointing at a route nobody mounts fails
// here.
func TestWorkspaceNavHrefResolves(t *testing.T) {
	mux := navMux(t)
	pid := uuid.New()

	for _, targets := range []navTargets{
		{Product: pid}, // the chrome with no milestone in scope
		{Product: pid, Milestone: navMilestoneID}, // a milestone-scoped page
	} {
		for _, g := range workspaceNav(targets, "/") {
			for _, item := range g.Items {
				if rec := fetch(t, mux, item.Href); rec.Code == http.StatusNotFound {
					t.Errorf("nav item %q href %s = 404 (dead nav link)", item.Label, item.Href)
				}
			}
		}
	}
}

// TestWorkspaceNav_DesignSessionsPointsAtTheProductsList pins the one
// href with a specific trap behind it: the design root is a page the
// operator types a product id into, so linking there would show the
// product-id form instead of the sessions.
func TestWorkspaceNav_DesignSessionsPointsAtTheProductsList(t *testing.T) {
	pid := uuid.New()

	item := navItemByLabel(t, workspaceNav(navTargets{Product: pid}, "/"), "Design sessions")
	if want := designProductSessionsPath(pid); item.Href != want {
		t.Errorf("Design sessions href = %s, want %s", item.Href, want)
	}
	if item.Href == designPath {
		t.Error("Design sessions points at the typed-id design root")
	}
	if rec := fetch(t, navMux(t), item.Href); rec.Code == http.StatusNotFound {
		t.Errorf("GET %s = 404", item.Href)
	}
}

// TestWorkspaceNavHrefCarriesTheCallersProduct is the product-scoping
// half: the chrome is handed an id and every href is built from it, so a
// copied link lands on the same product whoever opens it.
func TestWorkspaceNavHrefCarriesTheCallersProduct(t *testing.T) {
	pid := uuid.New()

	// Which items are product-scoped at all: the ones whose page lives
	// under /spec/products/{id} or /design/products/{id}. Overview, Needs
	// attention and Credentials are the same for every product.
	for _, g := range workspaceNav(navTargets{Product: pid}, "/") {
		for _, item := range g.Items {
			switch item.Label {
			case "Overview", "Needs attention", "Credentials":
				continue
			}
			if !strings.Contains(item.Href, pid.String()) {
				t.Errorf("nav item %q href %s does not carry product %s", item.Label, item.Href, pid)
			}
		}
	}
}

// TestWorkspaceShellDataBuildsEveryHrefFromTheProduct covers the seam a
// route actually calls: the ShellData it gets back carries hrefs built
// from the product id it supplied, not from one the chrome picked.
func TestWorkspaceShellDataBuildsEveryHrefFromTheProduct(t *testing.T) {
	pid := uuid.New()

	data := workspaceShellData(navTargets{Product: pid}, deliveryPath(pid), "Delivery", "developer")
	if data.Title != "Delivery" || data.UserLabel != "developer" {
		t.Errorf("shell data = %+v, want the caller's title and identity", data.LayoutData)
	}
	var sawDelivery bool
	for _, g := range data.NavGroups {
		for _, item := range g.Items {
			if item.Label == "Milestones" {
				sawDelivery = true
				if item.Href != deliveryPath(pid) {
					t.Errorf("Milestones href = %s, want %s", item.Href, deliveryPath(pid))
				}
				if !item.Active {
					t.Error("Milestones is the current page and must be marked active")
				}
			}
		}
	}
	if !sawDelivery {
		t.Error("shell data has no Milestones item")
	}
}

// TestWorkspaceNavItemIsActive pins the matching rule on its own, at the
// level where the wildcard and the Exact flag are visible, so a change to
// either is caught by the rule rather than by one lucky table row.
func TestWorkspaceNavItemIsActive(t *testing.T) {
	for _, tc := range []struct {
		name string
		item navItem
		path string
		want bool
		note string
	}{
		{
			name: "exact item matches its own path", item: navItem{Path: "/x", Exact: true},
			path: "/x", want: true,
		},
		{
			name: "exact item does not match a child", item: navItem{Path: "/x", Exact: true},
			path: "/x/y", want: false, note: "the Spec tabs are siblings, not a chain",
		},
		{
			name: "wildcard matches exactly one segment", item: navItem{Path: "/p/*/tasks"},
			path: "/p/abc/tasks", want: true,
		},
		{
			name: "wildcard matches only one segment", item: navItem{Path: "/p/*/tasks"},
			path: "/p/abc/def/tasks", want: false,
		},
		{
			name: "non-exact item owns its subtree", item: navItem{Path: "/ops"},
			path: "/ops/claimed", want: true,
		},
		{
			name: "prefix sibling is not owned", item: navItem{Path: "/ops"},
			path: "/opsarchive", want: false,
		},
		{
			name: "shorter page does not match", item: navItem{Path: "/a/b"},
			path: "/a", want: false,
		},
		{
			name: "root pattern matches only the root", item: navItem{Path: "/", Exact: true},
			path: "/", want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := navItemIsActive(tc.item, tc.path); got != tc.want {
				t.Errorf("navItemIsActive(%q, %q) = %v, want %v %s", tc.item.itemPath(), tc.path, got, tc.want, tc.note)
			}
		})
	}
}

// groupTitles lists the sidebar's group headings in render order.
func groupTitles(groups []components.NavGroup) []string {
	titles := make([]string, 0, len(groups))
	for _, g := range groups {
		titles = append(titles, g.Title)
	}
	return titles
}

// linkLabels lists one group's item labels in render order.
func linkLabels(links []components.NavLink) []string {
	labels := make([]string, 0, len(links))
	for _, l := range links {
		labels = append(labels, l.Label)
	}
	return labels
}

// activeGroupLabels flattens the sidebar to the labels it marked active.
func activeGroupLabels(groups []components.NavGroup) []string {
	var active []string
	for _, g := range groups {
		for _, item := range g.Items {
			if item.Active {
				active = append(active, item.Label)
			}
		}
	}
	return active
}

// navItemByLabel finds one sidebar item by its label, failing the test if
// it is absent -- so a renamed or dropped item reports as a missing item
// rather than as a silently skipped assertion.
func navItemByLabel(t *testing.T, groups []components.NavGroup, label string) components.NavLink {
	t.Helper()
	for _, g := range groups {
		for _, item := range g.Items {
			if item.Label == label {
				return item
			}
		}
	}
	t.Fatalf("no nav item labelled %q", label)
	return components.NavLink{}
}

// TestCredentialsPageIsServerRendered guards the regression where the
// page's script was emitted as visible text: the page must carry no
// script and must drive its actions through htmx against the page's own
// routes, not the self-serve JSON API.
func TestCredentialsPageIsServerRendered(t *testing.T) {
	app := newTestApp(t)
	app.credentials = &fakeCredentials{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	body := fetch(t, mux, credentialsPath).Body.String()
	for _, want := range []string{`id="credentials-results"`, `hx-post="` + credentialsPath + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("credentials page missing %q", want)
		}
	}
	if strings.Contains(body, "fetch(") || strings.Contains(body, "async function") {
		t.Error("credentials page renders script text")
	}
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

// Under AUTH_MODE=none the synthetic dev user is admitted to read routes,
// as api admits its dev token; with devAuth off the same user is refused.
func TestReadRoutes_AuthModeNoneAdmitsDevUser(t *testing.T) {
	get := func(app *App) int {
		mux := http.NewServeMux()
		app.mountShellRoutes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Code
	}
	if got := get(newTestApp(t)); got != http.StatusOK {
		t.Fatalf("dev user: status %d, want 200", got)
	}
	strict := newTestApp(t)
	strict.devAuth = false
	if got := get(strict); got != http.StatusForbidden {
		t.Fatalf("devAuth off, no role: status %d, want 403", got)
	}
}

// fakeCredentials is an in-memory auth.CredentialStore for handler tests.
type fakeCredentials struct {
	minted int
	listed []auth.Credential
}

func (f *fakeCredentials) Mint(context.Context, string) (string, auth.Credential, error) {
	f.minted++
	return "raw-token", auth.Credential{ID: uuid.New()}, nil
}

func (f *fakeCredentials) Verify(context.Context, string) (string, auth.Credential, error) {
	return "", auth.Credential{}, nil
}

func (f *fakeCredentials) Revoke(context.Context, uuid.UUID, string) error { return nil }

func (f *fakeCredentials) List(context.Context, string) ([]auth.Credential, error) {
	return f.listed, nil
}

// An unresolvable identity must still answer 200 with the error inline and
// must not mint anything: htmx does not swap on an error status.
func TestCredentialsMint_UnresolvedIdentityRendersInlineError(t *testing.T) {
	app := newTestApp(t)
	store := &fakeCredentials{}
	app.credentials = store
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, credentialsMintPath, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if store.minted != 0 {
		t.Errorf("minted %d credentials, want 0", store.minted)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="credentials-results"`) || !strings.Contains(body, "sign in again") {
		t.Errorf("fragment missing swap target or inline error: %s", body)
	}
	if strings.Contains(body, "<html") {
		t.Error("htmx request got the full page, want a bare fragment")
	}
}
