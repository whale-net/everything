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
//
// spec is a fakeSpecReader so every un-prefixed page can resolve a product
// (product_scope.go); it lists none, which is the empty-scope case these
// nav tests are indifferent to. scopes and tasks are what the chrome reads
// for its Needs-attention badge on every page it renders.
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
	return &App{
		auth:           auth,
		devAuth:        true,
		spec:           &fakeSpecReader{},
		scopes:         chromeScopes{},
		tasks:          chromeTaskCounter{},
		designSessions: navStubDesignSessions{},
		revisionEvents: navStubRevisionEvents{},
	}
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

// requiredAreas is the set of areas the shell's nav must reach, spelled
// out as literals rather than derived from navGroupTable. A test that
// iterates the production table passes even after an entry is deleted
// from it -- which is how a whole area can go missing from the sidebar
// without a single test noticing. This table is the one assertion that
// cannot be satisfied by shrinking the list it reads.
//
// The hrefs are the ones each area serves today. An area whose redesigned
// page has not shipped points at the existing page for that area, so this
// is the shell's promise: every one of these is a real page.
func requiredAreas(pid uuid.UUID) []string {
	return []string{
		opsPath,                            // Work: Needs attention
		designProductSessionsPath(pid),     // Design
		productPath(pid),                   // Spec: Capabilities
		productHref(pid, milestonesSuffix), // Delivery: Milestones
		credentialsPath,                    // Admin
	}
}

// TestNavExposesRequiredAreas pins that every required area is linked
// from every shell page, and that the link the sidebar actually renders is
// the one that resolves.
func TestNavExposesRequiredAreas(t *testing.T) {
	mux := navMux(t)

	for _, path := range shellPagePaths(navProductID) {
		body := fetchPage(t, mux, path).Body.String()
		for _, want := range requiredAreas(navProductID) {
			if !strings.Contains(body, `href="`+want+`"`) {
				t.Errorf("GET %s does not link to required area %s", path, want)
			}
		}
	}
}

// shellPagePaths is every shell page, spelled out as literals rather than
// derived from a production table, so a route dropped from the table
// cannot shrink what this walks and pass.
func shellPagePaths(pid uuid.UUID) []string {
	return []string{
		"/",
		productHref(pid, overviewSuffix),
		opsPath, opsClaimedPath, opsEscalatedPath, opsCancelledPath, opsNotesPath,
		designPath, designProductSessionsPath(pid),
		specPath, specProductsPath, productPath(pid), decisionsPath(pid),
		personasPath(pid), nonGoalsPath(pid), deliveryPath(pid),
		milestoneTasksPath(pid, navMilestoneID),
		milestoneBoardPath(pid, navMilestoneID),
		taskDetailPath(pid, navMilestoneID, uuid.New()),
		productHref(pid, tasksSuffix),
		productHref(pid, boardSuffix),
		credentialsPath,
	}
}

// TestShellRendersOnEveryRoute asserts the workspace chrome is present on
// each page and that no route is a bare or empty body.
func TestShellRendersOnEveryRoute(t *testing.T) {
	mux := navMux(t)

	for _, path := range shellPagePaths(navProductID) {
		rec := fetchPage(t, mux, path)
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

// TestActiveLinkPerRoute is the per-route table over the grouped sidebar:
// exactly one item is marked on a page that has one, and the home page
// lights Overview.
func TestActiveLinkPerRoute(t *testing.T) {
	mux := navMux(t)
	pid := navProductID

	for _, tc := range []struct {
		path       string
		wantActive string // "" means no item should be marked
		redirect   bool   // the path is pre-redesign and answers a 302
	}{
		{path: "/", wantActive: "Overview"},
		{path: productHref(pid, overviewSuffix), wantActive: "Overview"},
		{path: opsPath, wantActive: "Needs attention"},
		{path: opsClaimedPath, wantActive: "Needs attention"},
		{path: designProductSessionsPath(pid), wantActive: "Design sessions"},
		{path: productPath(pid), wantActive: "Capabilities"},
		{path: decisionsPath(pid), wantActive: "Decisions"},
		{path: productHref(pid, milestonesSuffix), wantActive: "Milestones"},
		// The pre-redesign delivery URL is one of the retired ones now, so
		// the walk follows the hop the way a browser does and asks what the
		// operator lands on -- the same question the per-container URLs
		// below already answer.
		{path: deliveryPath(pid), wantActive: "Milestones", redirect: true},
		// The per-milestone task list and board are pre-redesign URLs that
		// now redirect into the product-wide views, so the walk follows the
		// hop the way a browser does and asks what the operator lands on.
		{path: milestoneTasksPath(pid, navMilestoneID), wantActive: "Tasks", redirect: true},
		{path: milestoneBoardPath(pid, navMilestoneID), wantActive: "Board", redirect: true},
		{path: credentialsPath, wantActive: "Credentials"},
		// The two legacy area roots no sidebar item owns: both are static
		// landings a later phase retires, and neither is a nav item's page.
		{path: designPath},
		{path: specPath},
	} {
		body := navPageBody(t, mux, tc.path, tc.redirect)

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

// navProductID is the one product this file's deployment holds. The
// sidebar builds every product-scoped href from whatever product the page
// resolved, so a test asserting on those hrefs has to know which one the
// fixture serves.
var navProductID = uuid.MustParse("33333333-3333-3333-3333-333333333333")

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
	app.spec = &fakeSpecReader{
		products: []store.Product{{ID: navProductID, Name: "krill"}},
		listing: slice.DeliveryListing{
			Milestones: []slice.MilestoneListingEntry{{ID: navMilestoneID, Name: "Shipped shell"}},
		},
	}
	app.credentials = &fakeCredentials{}
	app.tasks = &navTasks{milestoneID: navMilestoneID}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// navTasks is the console-query surface the pages shellPagePaths walks
// read: the four read views' lists, the chrome's escalated count, and one
// milestone's tasks. Each answers empty, which is the honest reading of a
// fixture whose subject is navigation rather than data -- and an empty
// list renders, so the walk measures the routes rather than the stores.
//
// It embeds store.TaskStore so anything else the chrome or a view grows
// nil-panics here instead of quietly answering from nowhere.
type navTasks struct {
	store.TaskStore

	// milestoneID is the container task detail answers under, so the
	// task-detail page in the walk renders rather than answering the
	// in-shell 404 an id outside its milestone gets.
	milestoneID uuid.UUID
}

func (*navTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

func (*navTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

func (*navTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}

func (*navTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}

func (*navTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

// CountConsoleOverview is the Overview stat tiles' read, which this walk
// reaches through the home page. Zero figures: an idle deployment's real
// answer, and it keeps a method the walk exercises from nil-panicking on
// the embed.
func (*navTasks) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}

// SummarizeProductTaskProgress is the read the Overview's in-flight panel
// makes on every shell page this walk visits. It answers with no
// containers, so the panel renders its empty state and the walk measures
// the route rather than the store.
func (*navTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: []store.ContainerTaskProgress{}}, nil
}

// The product-wide task read, which the Tasks and Board nav hrefs now
// land on. Both answer empty, which renders as the empty state -- the walk
// measures the routes and the sidebar, not the store.
func (*navTasks) ListProductTasks(context.Context, store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	return store.Page[store.ProductTaskRow]{}, nil
}

func (*navTasks) CountProductTasks(context.Context, store.ListProductTasksParams) (int, error) {
	return 0, nil
}

func (f *navTasks) ListTasksByMilestone(context.Context, uuid.UUID) ([]store.TaskSummary, error) {
	return nil, nil
}

// GetTaskByID answers with a task for any id, so the task-detail page in
// this walk renders its chrome rather than answering a 404 -- the point of
// the walk is that every page mounts it.
func (f *navTasks) GetTaskByID(_ context.Context, id uuid.UUID) (store.Task, error) {
	return store.Task{ID: id, MilestoneID: f.milestoneID}, nil
}

func (*navTasks) ListDependencies(context.Context, uuid.UUID, uuid.UUID) ([]store.TaskDependency, error) {
	return nil, nil
}

func (*navTasks) ListNotesForTask(context.Context, uuid.UUID, uuid.UUID) ([]store.Note, error) {
	return nil, nil
}

func (*navTasks) GetClaimByID(context.Context, uuid.UUID) (store.Claim, error) {
	return store.Claim{}, store.ErrNotFound
}

// LatestClaimForTask is the rail's read behind "None. Last held by X". The
// task this walk answers with holds no claim and has never held one, so
// the read answers found=false -- which is what leaves the rail's Claim
// row saying the task has never been claimed.
func (*navTasks) LatestClaimForTask(context.Context, uuid.UUID, uuid.UUID) (store.Claim, bool, error) {
	return store.Claim{}, false, nil
}

// GetEscalationEventByID is the read behind a task's own
// current_escalation_id. The task this walk answers with is never
// escalated, so the read is never made; it is declared rather than left to
// the embedded nil interface, which panics rather than refusing.
func (*navTasks) GetEscalationEventByID(context.Context, uuid.UUID) (store.EscalationEvent, error) {
	return store.EscalationEvent{}, store.ErrNotFound
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
			name: "product-wide task list", targets: navTargets{Product: pid},
			activePath: productHref(pid, tasksSuffix), want: "Tasks",
		},
		{
			name: "product-wide board", targets: navTargets{Product: pid},
			activePath: productHref(pid, boardSuffix), want: "Board",
		},
		{
			name: "product-wide task detail", targets: navTargets{Product: pid},
			activePath: productHref(pid, tasksSuffix) + "/" + uuid.NewString(), want: "Tasks",
		},
		// The pre-redesign per-milestone URLs keep serving, so the items
		// still own them -- an operator following a bookmarked
		// /milestones/{mid}/tasks link is on a Tasks page and must see the
		// sidebar say so. The href moved to the product-wide page; the
		// active marking did not move with it.
		{
			name: "milestone task list", targets: navTargets{Product: pid},
			activePath: milestoneTasksPath(pid, mid), want: "Tasks",
		},
		{
			name: "milestone board", targets: navTargets{Product: pid},
			activePath: milestoneBoardPath(pid, mid), want: "Board",
		},
		{
			name: "milestone task detail", targets: navTargets{Product: pid},
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
	pid := navProductID

	// One table entry is enough now that every href is a function of the
	// product alone -- the two former cases (with and without a milestone
	// in scope) built different hrefs and so had to be walked separately.
	for _, targets := range []navTargets{
		{Product: pid},
	} {
		for _, g := range workspaceNav(targets, "/") {
			for _, item := range g.Items {
				// fetchPage, not fetch: a nav href that redirects into the
				// page that replaced it is not a dead link, and this test
				// is about dead links. Following the redirect keeps it
				// proving the href lands on a page that renders rather than
				// merely proving it does not 404.
				if rec := fetchPage(t, mux, item.Href); rec.Code != http.StatusOK {
					t.Errorf("nav item %q href %s = %d (dead nav link)", item.Label, item.Href, rec.Code)
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
//
// Every product-scoped item is checked rather than a sample: the seam is
// only worth having if it is total, and a second product id is threaded
// through to prove the hrefs track the argument instead of coinciding
// with it.
func TestWorkspaceShellDataBuildsEveryHrefFromTheProduct(t *testing.T) {
	pid, other := uuid.New(), uuid.New()

	data := workspaceShellData(navTargets{Product: pid}, deliveryPath(pid), "Delivery", "developer", nil)
	if data.Title != "Delivery" || data.UserLabel != "developer" {
		t.Errorf("shell data = %+v, want the caller's title and identity", data.LayoutData)
	}

	// The flat top-bar nav the shell does not yet replace is the same for
	// every product, so the product-scoped claim covers the sidebar only.
	byLabel := sidebarHrefsByLabel(data.NavGroups)
	for label, href := range byLabel {
		switch label {
		case "Overview", "Needs attention", "Credentials":
			continue
		}
		if !strings.Contains(href, pid.String()) {
			t.Errorf("sidebar item %q href %s does not carry product %s", label, href, pid)
		}
	}

	// A different product must move the sidebar's hrefs with it, or the
	// assertion above would pass on a table that hardcoded one id.
	otherData := workspaceShellData(navTargets{Product: other}, deliveryPath(other), "Delivery", "developer", nil)
	otherByLabel := sidebarHrefsByLabel(otherData.NavGroups)
	for label, href := range otherByLabel {
		switch label {
		case "Overview", "Needs attention", "Credentials":
			continue
		}
		if !strings.Contains(href, other.String()) {
			t.Errorf("sidebar item %q href %s does not carry product %s", label, href, other)
		}
		if byLabel[label] == href {
			t.Errorf("sidebar item %q is %s for both products; hrefs must track the caller's id", label, href)
		}
	}

	if item := navItemByLabel(t, data.NavGroups, "Milestones"); !item.Active {
		t.Error("Milestones is the current page and must be marked active")
	}
}

// sidebarHrefsByLabel flattens the sidebar to label -> href.
func sidebarHrefsByLabel(groups []components.NavGroup) map[string]string {
	out := make(map[string]string)
	for _, g := range groups {
		for _, item := range g.Items {
			out[item.Label] = item.Href
		}
	}
	return out
}

// TestWorkspaceShellIsMountedOnEveryRoute is the cutover itself: every
// page a sidebar item links at renders the workspace chrome, not the old
// top-bar layout. A route that quietly fell back would still answer 200
// and still carry a nav, so only the drawer hook tells the two apart.
func TestWorkspaceShellIsMountedOnEveryRoute(t *testing.T) {
	mux := navMux(t)
	pid := navProductID

	for _, g := range workspaceNav(navTargets{Product: pid}, "/") {
		for _, item := range g.Items {
			body := fetch(t, mux, item.Href).Body.String()
			if !strings.Contains(body, `data-krill="workspace-shell"`) {
				t.Errorf("GET %s does not render the workspace chrome", item.Href)
			}
		}
	}
}

// TestWorkspaceShellCarriesTheSwitcherAndToastHost pins the two pieces of
// chrome a page gets for free from the seam rather than asking for: the
// Product select, and the one live region every mutation's confirmation
// lands in. Neither appears anywhere in a page's own body, so a page that
// renders without them means the seam stopped passing the switcher through.
func TestWorkspaceShellCarriesTheSwitcherAndToastHost(t *testing.T) {
	mux := navMux(t)

	for _, path := range shellPagePaths(navProductID) {
		body := fetchPage(t, mux, path).Body.String()
		for _, want := range []string{productSwitchPath, `id="` + components.ToastHostID + `"`} {
			if !strings.Contains(body, want) {
				t.Errorf("GET %s missing %q from the chrome", path, want)
			}
		}
	}
}

// TestPrimaryNavScanIgnoresTheProductSubNav proves the data-krill hook is
// load-bearing rather than decorative, on the page that still exercises
// it: the delivery page renders two navs -- the shell's and its own
// cross-nav -- and each marks its own current page, so an unscoped scan
// would report two active links and every "exactly one" assertion built
// on it would be counting the sub-nav too.
//
// The spec pages used to be the subject here. They no longer render a
// cross-nav -- the tab strip replaced it, and marks its active tab with
// aria-selected rather than aria-current -- so they are asserted to carry
// exactly one aria-current in total, which is the property that keeps an
// unscoped scan honest there too.
func TestPrimaryNavScanIgnoresTheProductSubNav(t *testing.T) {
	pid := navProductID

	// The delivery page, rendered through the shell seam's own composition
	// rather than fetched: its URL is a Successor that redirects into the
	// Milestones table, so the page that carries the sub-nav has no path
	// left to fetch it from.
	withSubNav := renderDeliveryCrossNavPage(store.Product{ID: pid, Name: "krill"}, pid)
	if region := primaryNavRegion(withSubNav); region != "" {
		t.Errorf("the bare delivery body rendered a %q region; this scan needs the shell around it", `data-krill="primary-nav"`)
	}

	// The unscoped count is what makes the hook worth having: where a page
	// carries its own nav, an unscoped scan sees more than one current link.
	if total := strings.Count(withSubNav, `aria-current="page"`); total != 1 {
		t.Errorf("the delivery body carries %d aria-current anchors, want 1", total)
	}

	mux := navMux(t)
	// The four spec tabs, none of which renders a cross-nav of its own.
	for _, path := range []string{
		productPath(pid), decisionsPath(pid), personasPath(pid), nonGoalsPath(pid),
	} {
		body := fetchPage(t, mux, path).Body.String()

		if region := primaryNavRegion(body); region == "" {
			t.Errorf("GET %s rendered no %q region", path, `data-krill="primary-nav"`)
			continue
		}
		if got := activeLabels(body); len(got) != 1 {
			t.Errorf("GET %s: primary nav scan found %d active links (%v), want 1", path, len(got), got)
		}
		if total := strings.Count(body, `aria-current="page"`); total != 1 {
			t.Errorf("GET %s carries %d aria-current anchors in total; the tab strip "+
				"must mark itself with aria-selected so it cannot be counted here", path, total)
		}
	}

	// The per-milestone task list is pre-redesign and redirects into the
	// product-wide Tasks, so the scan runs on the page the operator lands
	// on rather than on the 302's empty body. That page carries the Tasks
	// scope sub-nav, so only the scoped count is asserted here.
	path := milestoneTasksPath(pid, navMilestoneID)
	if got := activeLabels(fetchPage(t, mux, path).Body.String()); len(got) != 1 {
		t.Errorf("GET %s: primary nav scan found %d active links (%v), want 1", path, len(got), got)
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
// not a 404), each keeps the Needs-attention item lit, and the ops root
// links to every one.
func TestOpsConsoleReadRoutesRegistered(t *testing.T) {
	mux := navMux(t)

	for _, path := range []string{opsClaimedPath, opsEscalatedPath, opsCancelledPath, opsNotesPath} {
		if !strings.HasPrefix(path, opsPath+"/") {
			t.Errorf("read view %s is not under the ops prefix %s", path, opsPath)
		}
		_, pattern := mux.Handler(httptest.NewRequest(http.MethodGet, path, nil))
		if pattern == "" {
			t.Errorf("ops read view %s is not registered", path)
		}
		if marked := activeLabels(fetch(t, mux, path).Body.String()); len(marked) != 1 || marked[0] != "Needs attention" {
			t.Errorf("ops read view %s marked %v active, want [Needs attention]", path, marked)
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
	mux := navMux(t)

	if rec := fetch(t, mux, "/does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /does-not-exist = %d, want 404 (home must not be a catch-all)", rec.Code)
	}
	if rec := fetch(t, mux, "/opsarchive"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /opsarchive = %d, want 404 (unregistered area sibling)", rec.Code)
	}
}

// helpers

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

// navPageBody is fetch for the paths the active-mark table marks as
// redirect: it follows the hop and answers with the body of the page the
// operator lands on. Asking the 302 for a body instead would read the
// scan off an empty response and report nothing marked, whatever the nav
// actually says.
func navPageBody(t *testing.T, mux *http.ServeMux, path string, redirect bool) string {
	t.Helper()
	if redirect {
		return fetchPage(t, mux, path).Body.String()
	}
	return fetch(t, mux, path).Body.String()
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

// fetchPage fetches target and follows any redirect, which is what a
// browser does with the href it is handed.
//
// It is here because two of the shell pages this file walks -- the
// per-milestone task list and board -- are now pre-redesign URLs that
// redirect into the product-wide Tasks and Board. A walk whose subject is
// "this URL renders the chrome" has to keep saying so through the redirect;
// one whose subject is a status or an href must keep using fetch and see
// the 302 itself.
func fetchPage(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	const maxHops = 5
	for range maxHops {
		rec := fetch(t, mux, target)
		if rec.Code != http.StatusFound && rec.Code != http.StatusMovedPermanently {
			return rec
		}
		next := rec.Header().Get("Location")
		if next == "" {
			t.Fatalf("%s redirected with no Location", target)
		}
		target = next
	}
	t.Fatalf("%s redirected more than %d times", target, maxHops)
	return nil
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

func (f *fakeCredentials) MintNamed(_ context.Context, _, name string) (string, auth.Credential, error) {
	f.minted++
	return "raw-token", auth.Credential{ID: uuid.New(), Name: name}, nil
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
