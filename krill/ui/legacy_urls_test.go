// The legacy-URL continuity contract (FR 2544224c): no pre-redesign URL
// ever returns 404, and one whose page has been replaced lands on that
// page's redesigned successor.
package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// legacyTasks is the task store behind the legacy-URL fixture: the badge
// count, the four console list views (all empty), and the one task the
// milestone's task-detail URL resolves.
//
// It embeds store.TaskStore so a read the fixture does not model still
// nil-panics rather than quietly returning a zero value.
type legacyTasks struct {
	store.TaskStore
	taskID      uuid.UUID
	milestoneID uuid.UUID
}

func (legacyTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

func (legacyTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

func (legacyTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

func (legacyTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}

func (legacyTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}

// The Overview home renders its stat tiles and in-flight panel, so a
// fixture reaching "/" owes both reads. Empty figures are a real answer
// for a scope with nothing in flight.
func (legacyTasks) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}

func (legacyTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: []store.ContainerTaskProgress{}}, nil
}

func (l legacyTasks) ListTasksByMilestone(_ context.Context, id uuid.UUID) ([]store.TaskSummary, error) {
	if id != l.milestoneID {
		return nil, nil
	}
	return []store.TaskSummary{{ID: l.taskID, Title: "Test task"}}, nil
}

// The per-milestone list and board URLs now redirect into the product-wide
// views, so the walk lands on a page whose read is the product-wide one
// rather than the milestone's. Answering it with the same single task is
// what makes the walk check the redirect and the page behind it together:
// the successor renders a real row, not a well-chromed empty table.
func (l legacyTasks) ListProductTasks(_ context.Context, params store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	if params.Scope.Kind != store.ProductTaskScopeMilestone || params.Scope.ContainerID != l.milestoneID {
		return store.Page[store.ProductTaskRow]{}, nil
	}
	return store.Page[store.ProductTaskRow]{Items: []store.ProductTaskRow{{
		TaskID:       l.taskID,
		Title:        "Test task",
		Milestone:    store.ProductTaskMilestoneRef{ID: l.milestoneID, Name: "Test milestone", Status: store.MilestoneStatusInProgress},
		CurrentLane:  store.LaneImplementation,
		AttemptCount: 1,
		AttemptCap:   store.DefaultAttemptCap,
	}}}, nil
}

func (l legacyTasks) CountProductTasks(_ context.Context, params store.ListProductTasksParams) (int, error) {
	page, err := l.ListProductTasks(context.Background(), params)
	return len(page.Items), err
}

func (l legacyTasks) GetTaskByID(_ context.Context, id uuid.UUID) (store.Task, error) {
	if id != l.taskID {
		return store.Task{}, store.ErrNotFound
	}
	return store.Task{ID: l.taskID, MilestoneID: l.milestoneID, Title: "Test task"}, nil
}

// The task detail page reads dependencies and notes alongside the task
// itself; the fixture's task has neither, so both are empty.
func (legacyTasks) ListDependencies(context.Context, uuid.UUID, uuid.UUID) ([]store.TaskDependency, error) {
	return nil, nil
}

func (legacyTasks) ListNotesForTask(context.Context, uuid.UUID, uuid.UUID) ([]store.Note, error) {
	return nil, nil
}

var _ store.TaskStore = legacyTasks{}

// legacyFixture is the world a legacy-URL test walks: one product holding
// one milestone with one task, and empty design sessions.
//
// The milestone and task are real rather than fresh random ids because the
// milestone task-list, task-detail and board URLs resolve their id against
// the product's delivery listing -- a well-formed id that belongs to no
// milestone is a correct 404, so testing URL continuity with one would be
// testing the 404 path and calling it a pass.
type legacyFixture struct {
	mux       *http.ServeMux
	app       *App
	pid       uuid.UUID
	mid       uuid.UUID
	tid       uuid.UUID
	sessionID uuid.UUID
}

func newLegacyFixture(t *testing.T) *legacyFixture {
	t.Helper()
	return newLegacyFixtureWith(t, legacyURLs())
}

// newLegacyFixtureWith is newLegacyFixture with the legacy table as an
// argument, so a test can mount a doctored copy alongside the real shell
// pages rather than hand-wiring one route and leaving the rest untested.
func newLegacyFixtureWith(t *testing.T, table []legacyURL) *legacyFixture {
	t.Helper()
	app := newTestApp(t)
	pid, mid, tid, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	// fakeSliceSpec, not a bare fakeSpecReader: production's specReader
	// answers the task-detail page's embedded slice read, so a fixture
	// that cannot renders that page's slice region as a read failure --
	// a well-chromed 200 whose content is an error, which the status-only
	// walk would happily pass.
	app.spec = &fakeSliceSpec{fakeSpecReader: &fakeSpecReader{
		products: []store.Product{{ID: pid, Name: "Test product"}},
		product:  store.Product{ID: pid, Name: "Test product"},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
			{ID: mid, Name: "Test milestone", Status: store.MilestoneStatusInProgress},
		}},
	}}
	// The console views read the task store rather than the badge-only
	// counter the chrome fixtures install, so a page that lists tasks
	// would otherwise nil-panic instead of rendering its empty state.
	app.tasks = &legacyTasks{taskID: tid, milestoneID: mid}
	app.designSessions = NewDesignSessions(sid, pid)
	app.revisionEvents = emptyRevisionEvents{}

	mux := http.NewServeMux()
	app.mountLegacyTable(mux, table)
	app.mountShellPages(mux)
	return &legacyFixture{mux: mux, app: app, pid: pid, mid: mid, tid: tid, sessionID: sid}
}

// legacyURLsUnderTest is every pre-redesign URL the FR names, spelled out
// with real ids rather than walked from the production table.
//
// It is deliberately not derived from legacyURLs(): a test that iterates
// the table it is meant to police passes just as well after an entry is
// deleted, which is exactly how a bookmarked URL goes dark without a
// single failure. This list is the promise; legacyURLs is the
// implementation that has to keep earning it.
//
// The next phases append here as they ship a page, alongside the entry
// they move to a successor in legacyURLs.
func (f *legacyFixture) urls() []string {
	p := "/spec/products/" + f.pid.String()
	m := p + "/milestones/" + f.mid.String()
	return []string{
		"/",
		opsPath,
		opsClaimedPath,
		opsEscalatedPath,
		opsCancelledPath,
		opsNotesPath,

		specPath,
		specProductsPath,
		p,
		p + "/decisions",
		p + "/personas",
		p + "/non-goals",
		p + "/delivery",
		m + "/tasks",
		m + "/tasks/" + f.tid.String(),
		m + "/board",

		designPath,
		"/design/products/" + f.pid.String() + "/design-sessions",
		"/design/design-sessions/" + f.sessionID.String(),
	}
}

// followRedirect resolves a URL to the page that finally renders it,
// following redirects up to a small bound, and returns the final status
// and the path it landed on.
func followRedirect(t *testing.T, f *legacyFixture, target string) (code int, final string) {
	t.Helper()
	const maxHops = 5
	for range maxHops {
		rec := fetch(t, f.mux, target)
		if c := rec.Code; c != http.StatusFound && c != http.StatusMovedPermanently {
			return c, target
		}
		next := rec.Header().Get("Location")
		if next == "" {
			t.Fatalf("%s redirected with no Location", target)
		}
		// A successor is an in-shell page by construction, so an off-site
		// or scheme-ful Location here would be a defect rather than
		// something to follow.
		if !strings.HasPrefix(next, "/") {
			t.Fatalf("%s redirected off-site to %q", target, next)
		}
		target = next
	}
	t.Fatalf("%s redirected more than %d times", target, maxHops)
	return 0, ""
}

// TestPreRedesignURLsResolve is the FR's acceptance: every pre-redesign URL
// resolves, none 404s, and one that redirects lands on a page that renders
// 200 inside the shell.
//
// "Resolves" is asserted as the full workspace chrome, not merely a 200: a
// route left serving the bare pre-shell layout would answer 200 with an
// operator stranded in a page with no way back, which is the outcome this
// contract exists to prevent.
func TestPreRedesignURLsResolve(t *testing.T) {
	f := newLegacyFixture(t)

	for _, url := range f.urls() {
		t.Run(url, func(t *testing.T) {
			code, final := followRedirect(t, f, url)

			if code == http.StatusNotFound {
				t.Fatalf("GET %s 404s: a pre-redesign URL went dark", url)
			}
			if code != http.StatusOK {
				t.Fatalf("GET %s resolved to %d, want 200 or a redirect to one", url, code)
			}

			body := fetch(t, f.mux, final).Body.String()
			assertShellChrome(t, url, final, body)
		})
	}
}

// TestLegacyListAndBoardRedirectScopedToTheirContainer is FR f41a352d's
// legacy-URL rule at the level of the redirect itself: the two retired
// per-container URLs answer 302, and the Location is the product-wide view
// of the same area SCOPED to the container the old URL named.
//
// The scope is the part worth asserting. A redirect that merely changed the
// page would resolve, would render, and would still be wrong -- an operator
// who bookmarked one milestone's board would land on every incomplete
// milestone's, with nothing on the page to say which view they had left.
func TestLegacyListAndBoardRedirectScopedToTheirContainer(t *testing.T) {
	f := newLegacyFixture(t)
	p := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String()

	for _, tc := range []struct {
		legacy string
		suffix string
	}{
		{legacy: p + "/tasks", suffix: tasksSuffix},
		{legacy: p + "/board", suffix: boardSuffix},
	} {
		t.Run(tc.legacy, func(t *testing.T) {
			rec := fetch(t, f.mux, tc.legacy)
			if rec.Code != http.StatusFound {
				t.Fatalf("GET %s = %d, want 302: the product-wide view replaces this page", tc.legacy, rec.Code)
			}
			loc, err := url.Parse(rec.Header().Get("Location"))
			if err != nil {
				t.Fatalf("GET %s redirected to an unparseable Location: %v", tc.legacy, err)
			}
			if want := productHref(f.pid, tc.suffix); loc.Path != want {
				t.Errorf("GET %s redirected to %s, want the product-wide %s", tc.legacy, loc.Path, want)
			}
			// Spelled as literals rather than read back through the
			// parser's constants: a redirect that built its query with the
			// same constant the parser reads would agree with itself even
			// if both were misspelled, and pinning the two to each other
			// is what this assertion is for.
			q := loc.Query()
			if got := q.Get("scope"); got != "milestone" {
				t.Errorf("GET %s redirected with scope=%q, want milestone", tc.legacy, got)
			}
			if got := q.Get("container_id"); got != f.mid.String() {
				t.Errorf("GET %s redirected with container_id=%q, want the milestone the old URL named (%s)",
					tc.legacy, got, f.mid)
			}
		})
	}
}

// TestLegacyTaskDetailURLStillServes is the other half of FR f41a352d's
// rule, and the half a redirect-everything change gets wrong: the
// per-container task DETAIL keeps serving its own page rather than
// redirecting.
//
// The detail is the one URL of the three where redirecting would lose the
// operator something. A list and a board both exist in the product-wide
// view, so sending the old URL there changes which page answers. A task
// detail does not: its replacement has not shipped, so a redirect would
// trade a page that answers for one that does not.
func TestLegacyTaskDetailURLStillServes(t *testing.T) {
	f := newLegacyFixture(t)
	detail := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String() +
		"/tasks/" + f.tid.String()

	rec := fetch(t, f.mux, detail)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: the task detail still serves its own page", detail, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("GET %s answered a redirect to %q: the detail keeps serving until its successor ships", detail, loc)
	}
	// And it is the task's own page rather than an in-shell status page:
	// the fixture's one task's title is what the region carries.
	if body := rec.Body.String(); !strings.Contains(body, "Test task") {
		t.Errorf("GET %s did not render the task itself", detail)
	}
}

// TestProductWideTaskTitleLinksResolve is the "no dead href" half of FR
// f41a352d: every title link in the product-wide Tasks table resolves.
//
// The table is not scoped to a container, so a row's detail link cannot be
// derived from the row's milestone -- it has to be the product-scoped
// detail URL, and that URL has to be one the mux actually serves. Both
// halves are driven through the real registrations: a link built from a
// route nobody mounts, or one naming the wrong container, is what this
// catches, and neither shows up in a test that only reads the string.
func TestProductWideTaskTitleLinksResolve(t *testing.T) {
	f := newLegacyFixture(t)

	body := fetch(t, f.mux, productHref(f.pid, tasksSuffix)+
		"?scope=milestone&container_id="+f.mid.String()).Body.String()

	links := taskTitleLinksIn(body)
	if len(links) == 0 {
		t.Fatalf("the product-wide Tasks table rendered no title link, so this proves nothing")
	}
	for _, href := range links {
		t.Run(href, func(t *testing.T) {
			if code, _ := followRedirect(t, f, href); code != http.StatusOK {
				t.Errorf("title link %s resolved to %d, want 200: a dead href in the Tasks table", href, code)
			}
		})
	}
}

// taskTitleLinksIn is every rendered Tasks row's title href, unescaped and
// in the order they appear.
//
// It matches the whole anchor and then reads its href rather than pinning
// an attribute order, so it keeps finding the row's title link if the
// markup's attribute order changes -- and it keys on the row's own
// data-krill hook, so what it returns is the table's links rather than the
// chrome's.
func taskTitleLinksIn(body string) []string {
	anchors := regexp.MustCompile(`<a\s[^>]*>`).FindAllString(body, -1)
	href := regexp.MustCompile(`href="([^"]+)"`)
	var out []string
	for _, a := range anchors {
		if !strings.Contains(a, `data-krill="task-title"`) {
			continue
		}
		if m := href.FindStringSubmatch(a); m != nil {
			out = append(out, strings.ReplaceAll(m[1], "&amp;", "&"))
		}
	}
	return out
}

// assertShellChrome asserts a resolved legacy URL renders the whole
// workspace shell rather than a bare page.
//
// The markers are the chrome's own data-krill hooks rather than page copy:
// the drawer wrapper, the grouped nav (whose group headings are what
// separate the groups from their items), and the Product select. A page
// that answered 200 with the old top-bar layout, or with the nav but no
// switcher, would fail here rather than reading as "resolves".
func assertShellChrome(t *testing.T, url, final, body string) {
	t.Helper()
	if !strings.Contains(body, "</html>") {
		t.Fatalf("GET %s landed on %s, which is not an HTML page", url, final)
	}
	for _, want := range []string{
		`data-krill="workspace-shell"`,  // the drawer sidebar, not the pre-shell layout
		`data-krill="primary-nav"`,      // the grouped nav, with its group headings
		`menu-title`,                    // a group heading, distinct from the items under it
		`data-krill="product-switcher"`, // the Product select, listing every product in scope
		`id="krill-product-switcher"`,
		`aria-label="Open navigation"`, // the sub-lg drawer toggle
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET %s landed on %s, missing %q: not the full workspace chrome", url, final, want)
		}
	}
}

// TestPreRedesignURLsRenderNoReadFailure guards the fixture's honesty.
//
// A stub that answers every read with an empty success makes a page that
// rendered an inline "could not load" alert indistinguishable from one that
// rendered its content -- and the walk above only checks status and chrome,
// so a page degraded by a failing read would pass it. The walk therefore
// asserts none of the 19 carries an error alert: every one of them must be
// rendering real content, not a well-chromed error.
func TestPreRedesignURLsRenderNoReadFailure(t *testing.T) {
	f := newLegacyFixture(t)

	for _, url := range f.urls() {
		t.Run(url, func(t *testing.T) {
			body := fetch(t, f.mux, url).Body.String()
			for _, marker := range []string{`alert-error`, `role="alert"`} {
				if strings.Contains(body, marker) {
					t.Errorf("GET %s renders %q: the fixture answered a read with a failure the walk does not see", url, marker)
				}
			}
		})
	}
}

// failingSliceSpec is fakeSliceSpec whose slice read fails, standing in
// for a store that cannot answer. It exists so TestPreRedesignURLsRenderNoReadFailure
// can be shown to have teeth: a spec reader that cannot answer the
// task-detail page's slice read is a page rendering an error behind a
// well-chromed 200, which the status-only walk cannot see.
type failingSliceSpec struct {
	*fakeSpecReader
}

func (failingSliceSpec) MilestoneDeliversSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return slice.Document{}, errors.New("slice read unavailable")
}

// TestPreRedesignURLsRenderNoReadFailure_HasTeeth proves the honesty
// check above is not vacuous: given a reader that genuinely fails, the
// task-detail URL renders an error alert and the check catches it.
//
// Without this, a future edit could relax the marker list down to nothing
// and the test would still pass, leaving the fixture free to mask a
// failing read again -- which is the exact failure the check exists for.
func TestPreRedesignURLsRenderNoReadFailure_HasTeeth(t *testing.T) {
	app := newTestApp(t)
	pid, mid, tid := uuid.New(), uuid.New(), uuid.New()
	app.spec = failingSliceSpec{fakeSpecReader: &fakeSpecReader{
		products: []store.Product{{ID: pid, Name: "Test product"}},
		product:  store.Product{ID: pid, Name: "Test product"},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
			{ID: mid, Name: "Test milestone", Status: store.MilestoneStatusInProgress},
		}},
	}}
	app.tasks = &legacyTasks{taskID: tid, milestoneID: mid}
	app.designSessions = NewDesignSessions(uuid.New(), pid)
	app.revisionEvents = emptyRevisionEvents{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	body := fetch(t, mux, milestoneTasksPath(pid, mid)+"/"+tid.String()).Body.String()
	if !strings.Contains(body, `alert-error`) {
		t.Fatalf("a task detail page whose slice read failed rendered no error alert: " +
			"the honesty check has nothing to catch and is vacuous")
	}
}

// TestLegacyTableEntryNamesExactlyOneDestination pins the table's own
// invariant: an entry either serves its existing page or redirects to a
// successor, never both and never neither. Both-set would make the
// redirect unreachable dead weight; neither-set would register a route
// serving nothing, which is the dark link this whole table exists to
// prevent.
func TestLegacyTableEntryNamesExactlyOneDestination(t *testing.T) {
	for _, l := range legacyURLs() {
		if l.Pattern == "" {
			t.Fatal("legacyURLs has an entry with no pattern")
		}
		if l.Serve == nil && l.Successor == nil {
			t.Errorf("legacy URL %s names neither a page nor a successor", l.Pattern)
		}
		if l.Serve != nil && l.Successor != nil {
			t.Errorf("legacy URL %s names both a page and a successor", l.Pattern)
		}
	}
}

// TestLegacyTableAndLiveRoutesAgree is the two halves of one agreement,
// and it needs both to be load-bearing.
//
//   - Every table entry is mounted: a pattern the table names but the mux
//     does not serve is a URL the FR believes is kept alive and nothing is.
//   - Every pre-redesign registration appears in the table: a route
//     serving one of these URLs that the table does not name is a URL
//     outside the retirement mechanism, which is how a page gets replaced
//     without its old link ever being repointed.
//
// Either half alone misses a real failure. Checking only that the table is
// mounted passes when a URL has been dropped from the table and still
// resolves by accident through some other route -- and checking only that
// each URL resolves passes when the route it resolves through is not the
// table's, so replacing the page leaves the old URL serving the old page
// forever.
func TestLegacyTableAndLiveRoutesAgree(t *testing.T) {
	f := newLegacyFixture(t)
	table := legacyURLs()

	inTable := make(map[string]bool, len(table))
	for _, l := range table {
		if l.Pattern == "" {
			t.Fatal("legacyURLs has an entry with no pattern")
		}
		inTable[l.Pattern] = true
	}

	// Half one: each table pattern is a real registration, not just a
	// declared one. mux.Handler reports the pattern that actually matched,
	// so an entry the mount dropped (or shadowed by a more specific route
	// registered elsewhere) shows up here.
	for _, l := range table {
		if _, matched := f.mux.Handler(concreteRequest(l.Pattern, f)); matched == "" {
			t.Errorf("legacyURLs names %s but the mux does not serve it: the entry is declared, not mounted", l.Pattern)
		}
	}

	// Half two: each URL the FR names resolves through the table, so every
	// pre-redesign route is one a phase can retire by editing one field.
	for _, url := range f.urls() {
		_, matched := f.mux.Handler(httptest.NewRequest(http.MethodGet, url, nil))
		if matched == "" {
			t.Errorf("GET %s is not registered at all", url)
			continue
		}
		if !inTable[matched] {
			t.Errorf("GET %s resolves through %q, which legacyURLs does not name: "+
				"this URL is outside the retirement mechanism", url, matched)
		}
	}
}

// concretePathFor turns one legacyURLs pattern into a request path the
// fixture resolves, so a table entry can be probed for registration
// without the probe re-deriving the mux's wildcard names.
func concretePathFor(pattern string, f *legacyFixture) string {
	path := strings.ReplaceAll(pattern, "{$}", "")
	path = strings.ReplaceAll(path, "{productID}", f.pid.String())
	// The one pattern whose {id} names something other than the product.
	if strings.Contains(path, "design-sessions/{id}") {
		path = strings.Replace(path, "design-sessions/{id}",
			"design-sessions/"+f.sessionID.String(), 1)
	}
	return strings.NewReplacer(
		"{id}", f.pid.String(),
		"{mid}", f.mid.String(),
		"{tid}", f.tid.String(),
	).Replace(path)
}

// concreteRequest builds the request concretePathFor names, honouring the
// method prefix a pattern may carry.
func concreteRequest(pattern string, f *legacyFixture) *http.Request {
	path := strings.TrimPrefix(pattern, http.MethodGet+" ")
	return httptest.NewRequest(http.MethodGet, concretePathFor(path, f), nil)
}

// TestLegacyRedirectUsesFoundAndNotMoved drives the redirect path the way a
// future phase will: the production table, with one real entry moved from
// serving its page to naming its successor, mounted through the same
// mountLegacyTable production calls.
//
// A stand-in route hand-wired onto its own mux would pass whether or not
// mountLegacyTable honours Successor at all -- and that wiring is exactly
// what a phase inherits when it flips a field. 302 rather than 301 is
// deliberate: a pre-redesign URL stays a live link an operator may keep
// following, and 301 lets a browser pin the old URL in its cache past the
// page it now names.
func TestLegacyRedirectUsesFoundAndNotMoved(t *testing.T) {
	const retired = opsEscalatedPath

	table := legacyURLs()
	replaced := false
	for i, l := range table {
		if l.Pattern != retired {
			continue
		}
		replaced = true
		table[i] = legacyURL{
			Pattern: retired,
			// The successor FR 2544224c names for this URL: the
			// needs-attention page's escalated tab.
			Successor: func(a *App, r *http.Request) (string, bool) {
				product, err := a.resolveProductForUnprefixed(r)
				if err != nil || product.ID == uuid.Nil {
					return "", false
				}
				return productHref(product.ID, needsAttentionSuffix) + "?tab=escalated", true
			},
		}
	}
	if !replaced {
		t.Fatal("legacyURLs no longer registers " + retired)
	}
	f := newLegacyFixtureWith(t, table)

	rec := fetch(t, f.mux, retired)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want 302 now that the table names a successor", retired, rec.Code)
	}
	if rec.Code == http.StatusMovedPermanently {
		t.Fatal("a pre-redesign URL must not answer 301; the browser would cache it past the page it now names")
	}
	want := productHref(f.pid, needsAttentionSuffix) + "?tab=escalated"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("redirect Location %q, want %q", got, want)
	}

	// The successor has to be a real in-shell page, and one entry's
	// retirement must leave the rest of the table untouched.
	target := rec.Header().Get("Location")
	if code, _ := followRedirect(t, f, retired); code != http.StatusOK {
		t.Errorf("following %s resolved to %d, want 200", retired, code)
	}
	assertShellChrome(t, retired, target, fetch(t, f.mux, target).Body.String())

	for _, url := range f.urls() {
		if url == retired {
			continue
		}
		if code, _ := followRedirect(t, f, url); code != http.StatusOK {
			t.Errorf("retiring %s broke %s: it now resolves to %d", retired, url, code)
		}
	}
}

// TestLegacyRedirectWithNoProductStaysInShell covers the one case a legacy
// URL cannot redirect on: an empty scope resolves no product to build a
// successor from, and an un-prefixed URL must still land somewhere. It
// renders the product index rather than redirecting to nowhere.
func TestLegacyRedirectWithNoProductStaysInShell(t *testing.T) {
	table := legacyURLs()
	replaced := false
	for i, l := range table {
		if l.Pattern != opsEscalatedPath {
			continue
		}
		replaced = true
		table[i] = legacyURL{
			Pattern: opsEscalatedPath,
			Successor: func(a *App, r *http.Request) (string, bool) {
				product, err := a.resolveProductForUnprefixed(r)
				if err != nil || product.ID == uuid.Nil {
					return "", false
				}
				return productHref(product.ID, needsAttentionSuffix), true
			},
		}
	}
	if !replaced {
		t.Fatal("legacyURLs no longer registers " + opsEscalatedPath)
	}

	// The whole table, on an app whose scope holds no product -- the same
	// mux a deployment starts with, not a lone probe route.
	app := newTestApp(t)
	app.spec = &fakeSpecReader{}
	app.tasks = emptyListTasks{}
	mux := http.NewServeMux()
	app.mountLegacyTable(mux, table)
	app.mountShellPages(mux)

	rec := fetch(t, mux, opsEscalatedPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: an empty scope must still land on a page", opsEscalatedPath, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("empty scope answered a redirect to %q, which has nowhere to go", loc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-krill="no-products"`) {
		t.Errorf("empty-scope legacy URL did not render the product index: %s", body)
	}
}
