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
	taskID       uuid.UUID
	milestoneID  uuid.UUID
	milepebbleID uuid.UUID
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
//
// The two single-container scopes are answered separately because they are
// the thing the milepebble redirect has to get right: a redirect that
// spelled "milestone" for a milepebble id would still land a 200 here (the
// resolver 404s before the read, so the read is never asked), which is why
// this answers both rather than only the milestone one.
func (l legacyTasks) ListProductTasks(_ context.Context, params store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	row := store.ProductTaskRow{
		TaskID:       l.taskID,
		Title:        "Test task",
		Milestone:    store.ProductTaskMilestoneRef{ID: l.milestoneID, Name: "Test milestone", Status: store.MilestoneStatusInProgress},
		CurrentLane:  store.LaneImplementation,
		AttemptCount: 1,
		AttemptCap:   store.DefaultAttemptCap,
	}
	switch {
	case params.Scope.Kind == store.ProductTaskScopeMilestone && params.Scope.ContainerID == l.milestoneID:
	case params.Scope.Kind == store.ProductTaskScopeMilepebble && params.Scope.ContainerID == l.milepebbleID:
		row.Milepebble = &store.ProductTaskMilepebbleRef{
			ID:     l.milepebbleID,
			Name:   "Test milepebble",
			Status: store.MilestoneStatusInProgress,
		}
	default:
		return store.Page[store.ProductTaskRow]{}, nil
	}
	return store.Page[store.ProductTaskRow]{Items: []store.ProductTaskRow{row}}, nil
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

// GetEscalationEventByID is the read behind a task's own
// current_escalation_id. This fixture's task carries none, so it is never
// asked for; it is declared because store.TaskStore includes it and
// leaving it to the embedded nil interface would panic the moment a
// fixture did escalate a task.
func (legacyTasks) GetEscalationEventByID(context.Context, uuid.UUID) (store.EscalationEvent, error) {
	return store.EscalationEvent{}, store.ErrNotFound
}

var _ store.TaskStore = legacyTasks{}

// legacyFixture is the world a legacy-URL test walks: one product holding
// one milestone -- cut into one milepebble -- with one task, and empty
// design sessions.
//
// The milestone, milepebble and task are real rather than fresh random ids
// because the milestone task-list, task-detail and board URLs resolve their
// id against the product's delivery listing -- a well-formed id that belongs
// to no milestone is a correct 404, so testing URL continuity with one would
// be testing the 404 path and calling it a pass.
//
// The milepebble is there for the same reason and is not decoration: the
// delivery page renders a milepebble's Tasks and Board links at the same
// /milestones/{id}/ path a milestone's use, so that is a real operator URL
// this walk has to keep alive.
type legacyFixture struct {
	mux       *http.ServeMux
	app       *App
	pid       uuid.UUID
	mid       uuid.UUID
	mpid      uuid.UUID
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
	pid, mid, mpid, tid, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()

	// fakeSliceSpec, not a bare fakeSpecReader: production's specReader
	// answers the task-detail page's embedded slice read, so a fixture
	// that cannot renders that page's slice region as a read failure --
	// a well-chromed 200 whose content is an error, which the status-only
	// walk would happily pass.
	app.spec = &fakeSliceSpec{fakeSpecReader: &fakeSpecReader{
		products: []store.Product{{ID: pid, Name: "Test product"}},
		product:  store.Product{ID: pid, Name: "Test product"},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
			{ID: mid, Name: "Test milestone", Status: store.MilestoneStatusInProgress,
				Milepebbles: []slice.MilepebbleListingEntry{
					{ID: mpid, Name: "Test milepebble", Status: store.MilestoneStatusInProgress},
				}},
		}},
	}}
	// The console views read the task store rather than the badge-only
	// counter the chrome fixtures install, so a page that lists tasks
	// would otherwise nil-panic instead of rendering its empty state.
	app.tasks = &legacyTasks{taskID: tid, milestoneID: mid, milepebbleID: mpid}
	app.designSessions = NewDesignSessions(sid, pid)
	app.revisionEvents = emptyRevisionEvents{}

	mux := http.NewServeMux()
	app.mountLegacyTable(mux, table)
	app.mountShellPages(mux)
	return &legacyFixture{mux: mux, app: app, pid: pid, mid: mid, mpid: mpid, tid: tid, sessionID: sid}
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
	mp := p + "/milestones/" + f.mpid.String()
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

		// The delivery page renders a milepebble's Tasks and Board at this
		// same path, so these are URLs an operator really follows -- the
		// walk covers them for the same reason it covers the milestone's.
		mp + "/tasks",
		mp + "/board",

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
//
// Both container kinds are driven, because the same path serves both and
// only the id says which. scope and container_id are spelled as literals
// rather than read back through the parser's constants: a redirect that
// built its query with the same constant the parser reads would agree with
// itself even if both were misspelled, and pinning the two to each other is
// what this assertion is for.
func TestLegacyListAndBoardRedirectScopedToTheirContainer(t *testing.T) {
	f := newLegacyFixture(t)
	p := "/spec/products/" + f.pid.String() + "/milestones/"

	for _, container := range []struct {
		id   uuid.UUID
		kind string
	}{
		{id: f.mid, kind: "milestone"},
		{id: f.mpid, kind: "milepebble"},
	} {
		for _, suffix := range []string{tasksSuffix, boardSuffix} {
			legacy := p + container.id.String() + suffix
			t.Run(container.kind+legacy[len(p):], func(t *testing.T) {
				rec := fetch(t, f.mux, legacy)
				if rec.Code != http.StatusFound {
					t.Fatalf("GET %s = %d, want 302: the product-wide view replaces this page", legacy, rec.Code)
				}
				loc, err := url.Parse(rec.Header().Get("Location"))
				if err != nil {
					t.Fatalf("GET %s redirected to an unparseable Location: %v", legacy, err)
				}
				if want := productHref(f.pid, suffix); loc.Path != want {
					t.Errorf("GET %s redirected to %s, want the product-wide %s", legacy, loc.Path, want)
				}
				q := loc.Query()
				if got := q.Get("scope"); got != container.kind {
					t.Errorf("GET %s redirected with scope=%q, want %s: the id names a %s, and the mode has to agree with it",
						legacy, got, container.kind, container.kind)
				}
				if got := q.Get("container_id"); got != container.id.String() {
					t.Errorf("GET %s redirected with container_id=%q, want the container the old URL named (%s)",
						legacy, got, container.id)
				}
			})
		}
	}
}

// TestLegacyMilepebbleURLDoesNot404 is why the redirect's mode is read off
// the container's own kind rather than hardcoded to milestone.
//
// The legacy per-container path served a milepebble's tasks at exactly the
// path a milestone's used -- delivery_page.go renders both under
// /milestones/{id}/ -- so a redirect that always said scope=milestone would
// send a milepebble link to a scope that refuses a milepebble's id. The
// refusal is an in-shell 404 (productTaskScopeNotFound), which is precisely
// the outcome FR f41a352d forbids: the old URL must not 404.
//
// So this asserts the end-to-end answer rather than the query's shape: the
// milepebble's legacy URL redirects, and where it lands renders the
// milepebble's own task. The redirect test above already pins the mode
// literal; what only an end-to-end walk can catch is the mode being right
// while the page behind it still refuses the id.
func TestLegacyMilepebbleURLDoesNot404(t *testing.T) {
	f := newLegacyFixture(t)
	base := "/spec/products/" + f.pid.String() + "/milestones/" + f.mpid.String()

	for _, suffix := range []string{tasksSuffix, boardSuffix} {
		legacy := base + suffix
		t.Run(suffix, func(t *testing.T) {
			code, final := followRedirect(t, f, legacy)
			if code == http.StatusNotFound {
				t.Fatalf("GET %s 404s: the redirect named a scope the product-wide page refuses", legacy)
			}
			if code != http.StatusOK {
				t.Fatalf("GET %s resolved to %d, want 200", legacy, code)
			}
			body := fetch(t, f.mux, final).Body.String()
			assertShellChrome(t, legacy, final, body)
			// And it is the milepebble's own work, not an empty page that
			// merely avoided the 404. The fixture answers this milepebble's
			// scope with the one task and every other scope with nothing,
			// so a row here means the scope resolved to the right container.
			if !strings.Contains(body, "Test task") {
				t.Errorf("GET %s landed on %s rendering no task: the milepebble's scope resolved to something else", legacy, final)
			}
		})
	}
}

// TestLegacyTaskDetailRedirectsToTheProductScopedDetail is FR 0c03eac1's
// legacy-URL clause: a pre-redesign milestone task detail URL redirects to
// the redesigned detail.
//
// It retires into the DETAIL and not into a list the way the per-container
// list and board do, because the operator following a bookmarked task link
// came to read that task. That is also why the target carries the tid alone
// rather than a container scope: the product-scoped detail resolves the
// task's own milestone from the task, so a redirect that pinned the old
// URL's {mid} would send a task that had since moved between containers to
// a page that cannot find it.
//
// Both halves are driven because either alone is satisfiable by a wrong
// redirect: the Location pins the target's shape, and the walk proves the
// target actually serves the task rather than a well-chromed 404.
func TestLegacyTaskDetailRedirectsToTheProductScopedDetail(t *testing.T) {
	f := newLegacyFixture(t)
	detail := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String() +
		"/tasks/" + f.tid.String()

	rec := fetch(t, f.mux, detail)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want 302: the redesigned detail replaces this page", detail, rec.Code)
	}
	want := productTaskDetailPath(f.pid, f.tid)
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("GET %s redirected to %q, want the product-scoped detail %q", detail, got, want)
	}

	code, final := followRedirect(t, f, detail)
	if code != http.StatusOK {
		t.Fatalf("GET %s resolved to %d at %s, want 200: an old task link must still read the task",
			detail, code, final)
	}
	if final != want {
		t.Errorf("GET %s landed on %s, want %s", detail, final, want)
	}
	if body := fetch(t, f.mux, final).Body.String(); !strings.Contains(body, "Test task") {
		t.Errorf("GET %s landed on %s rendering no task: the redirect preserved the URL but not the task", detail, final)
	}
}

// TestLegacyTaskDetailRedirectIsNotContainerScoped pins the {mid} half of
// the redirect rule: the old URL's container does not travel.
//
// A target that carried the container -- a scope query, or the per-
// container URL itself -- would be a redirect that resolves and still
// strands the operator whenever the task's own milestone is not the one the
// old URL named. This drives that case end to end rather than reading the
// query: the milepebble URL for the milestone's own task is the exact
// mismatch, and the product-scoped detail serves it because it resolves the
// task's container itself.
func TestLegacyTaskDetailRedirectIsNotContainerScoped(t *testing.T) {
	f := newLegacyFixture(t)
	// The URL names the milepebble; the fixture's task belongs to the
	// milestone. The per-container URL would have refused this outright.
	detail := "/spec/products/" + f.pid.String() + "/milestones/" + f.mpid.String() +
		"/tasks/" + f.tid.String()

	rec := fetch(t, f.mux, detail)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want 302", detail, rec.Code)
	}
	want := productTaskDetailPath(f.pid, f.tid)
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("GET %s redirected to %q, want %q: the redirect carries the tid, not the old URL's container",
			detail, got, want)
	}

	body := fetch(t, f.mux, want).Body.String()
	if !strings.Contains(body, "Test task") {
		t.Errorf("GET %s reached %s but rendered no task", detail, want)
	}
}

// TestLegacyTaskDetailUnresolvableURLStaysInShell is the one case this
// redirect cannot make: a URL whose ids do not parse has no product-scoped
// detail to name. It answers in the shell rather than redirecting nowhere,
// the same rule every other successor obeys.
func TestLegacyTaskDetailUnresolvableURLStaysInShell(t *testing.T) {
	f := newLegacyFixture(t)
	p := "/spec/products/" + f.pid.String() + "/milestones/"

	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "unparseable task id", url: p + f.mid.String() + "/tasks/not-a-uuid"},
		{name: "unparseable container id", url: p + "not-a-uuid/tasks/" + f.tid.String()},
		// The product id is the third wildcard this URL declares, and the
		// one legacyTaskDetailSuccessor parses before the others. A
		// successor that built the target from a pid it had not checked
		// would answer /products/ 404 for a URL whose tid was perfectly
		// good -- so the unparseable pid is its own case, not a repeat.
		{name: "unparseable product id", url: "/spec/products/not-a-uuid/milestones/" + f.mid.String() + "/tasks/" + f.tid.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := fetch(t, f.mux, tc.url)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200: an unresolvable legacy URL must still land in the shell",
					tc.url, rec.Code)
			}
			if loc := rec.Header().Get("Location"); loc != "" {
				t.Errorf("GET %s redirected to %q, which has nowhere to go", tc.url, loc)
			}
			if body := rec.Body.String(); !strings.Contains(body, `data-krill="no-products"`) {
				t.Errorf("GET %s did not render the product index:\n%s", tc.url, body)
			}
		})
	}
}

// TestLegacyTaskDetailRedirectAnswersEveryMethodWithFound pins the half of
// the redirect a status code alone does not: which methods answer it, and
// with which code.
//
// The legacy pattern carries no method prefix, so the redirect is
// mounted for every method rather than for GET alone, and 302 is the code
// that makes that safe here. The target is registered GET-only (a task
// detail is a read), and 302 is the status a client may re-issue as a GET,
// so a non-GET that follows the redirect lands on the page. A 307 or 308
// would preserve the method instead and land on a 405; a 301 would let a
// browser cache the mapping past the product-membership check the target
// applies, which is the whole reason this URL is a redirect and not a
// copy. So this asserts 302 for every method, and asserts the two codes
// that would each be a defect in their own right.
//
// The 405 the target answers a preserved method with is read from the real
// registration rather than asserted as a constant, so the reason 302 is
// the right code here is checked against the route that has to accept the
// redirected request.
func TestLegacyTaskDetailRedirectAnswersEveryMethodWithFound(t *testing.T) {
	f := newLegacyFixture(t)
	detail := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String() +
		"/tasks/" + f.tid.String()
	want := productTaskDetailPath(f.pid, f.tid)

	// Read the target's own method set off the live registration: the
	// 302's method contract is only correct relative to what the target
	// accepts, so a target that later grows a POST would make a
	// hard-coded "405" here a lie.
	target := httptest.NewRecorder()
	f.mux.ServeHTTP(target, httptest.NewRequest(http.MethodPost, want, nil))
	if target.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s = %d, want 405: the redirect's method contract is asserted against a target that answers it, "+
			"and this one does not refuse POST", want, target.Code)
	}
	if allow := target.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Fatalf("POST %s answers Allow %q, which does not include GET: a 302's method downgrade has nothing to land on", want, allow)
	}

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			f.mux.ServeHTTP(rec, httptest.NewRequest(method, detail, nil))

			if rec.Code != http.StatusFound {
				t.Errorf("%s %s = %d, want 302", method, detail, rec.Code)
			}
			// Named rather than folded into the 302 check above, because
			// each of these is a redirect that is wrong in its own way and
			// an operator would feel all three: 301 and 308 are cacheable
			// and outlive the page they name, 307 preserves a method the
			// target refuses.
			for _, wrong := range []int{http.StatusMovedPermanently, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
				if rec.Code == wrong {
					t.Errorf("%s %s answers %d, which must not replace 302: a cacheable or method-preserving redirect "+
						"either pins the old URL past the product-membership check or lands a non-GET on a 405", method, detail, wrong)
				}
			}
			if got := rec.Header().Get("Location"); got != want {
				t.Errorf("%s %s redirected to %q, want %q", method, detail, got, want)
			}
		})
	}
}

// TestLegacyTaskDetailRedirectLocationIsCanonical pins that every spelling
// of the same task id redirects to the same Location.
//
// A successor that interpolated the path segment would hand back whatever
// spelling arrived, and the target would then be a URL no link in the UI
// spells and no cache keys the same way twice. Each case below is the same
// task as the canonical one, so a Location built from the parsed UUID is
// the same string for all of them; each is driven through a full follow so
// a canonicalized Location is also shown to be a Location that resolves.
//
// The upper-case spelling is the one that discriminates today: the mux
// hands a wildcard's segment to the handler already unescaped, so the
// percent-encoded case reaches uuid.Parse identically either way and pins
// the weaker half -- that the Location never carries an escape of its own.
func TestLegacyTaskDetailRedirectLocationIsCanonical(t *testing.T) {
	f := newLegacyFixture(t)
	p := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String() + "/tasks"
	want := productTaskDetailPath(f.pid, f.tid)

	for _, tc := range []struct {
		name string
		tid  string
	}{
		// %2D is a "-". The mux unescapes a wildcard's segment before the
		// handler reads it, so the successor sees the same task -- which
		// is why this case pins that the Location it builds carries no
		// escape, rather than that the two spellings differ at all.
		{name: "percent-encoded", tid: strings.Replace(f.tid.String(), "-", "%2D", 1)},
		{name: "upper case", tid: strings.ToUpper(f.tid.String())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := fetch(t, f.mux, p+"/"+tc.tid)
			if rec.Code != http.StatusFound {
				t.Fatalf("GET %s/%s = %d, want 302", p, tc.tid, rec.Code)
			}
			if got := rec.Header().Get("Location"); got != want {
				t.Errorf("GET %s/%s redirected to %q, want the canonical %q: the Location must come from the "+
					"parsed id, not from the request path's own spelling", p, tc.tid, got, want)
			}
			if code, final := followRedirect(t, f, p+"/"+tc.tid); code != http.StatusOK || final != want {
				t.Errorf("GET %s/%s resolved to %d at %s, want 200 at %s", p, tc.tid, code, final, want)
			}
		})
	}
}

// TestNoPerContainerTaskURLRendersTheDetail is the retirement's negative
// half: no URL under the pre-redesign per-container task subtree serves the
// task detail any more, and the one canonical form that does resolve is the
// redirect.
//
// The table already pins that the pattern redirects, and re-adding a Serve
// beside the Successor is caught by the one-destination check. What neither
// catches is a second registration somewhere that happens to cover a
// neighbouring spelling of the same URL -- a subtree pattern, a trailing
// slash, a sub-page hung under the detail -- which would leave the old page
// serving through a door the table does not name. So this walks the
// near-miss forms rather than the one registered pattern: a route that
// still dispatched handleTaskDetail anywhere in that subtree would render
// the page's own markers at one of them.
//
// The forms that answer 404 did so before the retirement too -- the pattern
// was never a subtree -- so this is not a new dark URL claim. What is
// asserted is only what the retirement owns: none of them renders the
// detail, and the canonical form's redirect target is the page that does.
//
// It also carries the redirect's loop-freedom, which no other check states
// directly: the Location answers 200 with no Location of its own, so a
// successor pointed at another successor's target could not chain.
func TestNoPerContainerTaskURLRendersTheDetail(t *testing.T) {
	f := newLegacyFixture(t)
	p := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String() + "/tasks"
	sp := "/spec/products/" + f.pid.String()
	want := productTaskDetailPath(f.pid, f.tid)

	// The markers are the detail page's own, not a status: a 200 that
	// rendered them would mean the old page still answers, and a 404 or a
	// 302 that somehow carried them would mean the check cannot tell the
	// three apart.
	const rail = `data-krill="task-properties-rail"`
	detailPage := func(body string) bool {
		return strings.Contains(body, rail) || strings.Contains(body, "Test task")
	}

	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "canonical legacy URL", url: p + "/" + f.tid.String()},
		{name: "trailing slash", url: p + "/" + f.tid.String() + "/"},
		{name: "sub-page under the detail", url: p + "/" + f.tid.String() + "/notes"},
		{name: "query string under the detail", url: p + "/" + f.tid.String() + "/?tab=overview"},
		{name: "query string on the list", url: p + "?task=" + f.tid.String()},
		{name: "repeated tasks segment", url: p + "/tasks/" + f.tid.String()},
		{name: "container after the id", url: p + "/" + f.tid.String() + "/milestones/" + f.mid.String()},
		{name: "product-scoped without the container", url: sp + "/tasks/" + f.tid.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := fetch(t, f.mux, tc.url)
			if body := rec.Body.String(); detailPage(body) {
				t.Errorf("GET %s = %d and rendered the task detail:\n%s\n"+
					"a per-container task URL is still serving: the pre-redesign detail was not fully retired",
					tc.url, rec.Code, body)
			}
		})
	}

	// The canonical form is the one that resolves, and it resolves by
	// redirecting to a page that does not redirect again.
	rec := fetch(t, f.mux, p+"/"+f.tid.String())
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("GET %s = %d at %q, want 302 at %s", p+"/"+f.tid.String(), rec.Code, rec.Header().Get("Location"), want)
	}
	landed := fetch(t, f.mux, want)
	if landed.Code != http.StatusOK {
		t.Errorf("GET %s = %d, want 200: the redirect must not chain", want, landed.Code)
	}
	if loc := landed.Header().Get("Location"); loc != "" {
		t.Errorf("GET %s redirected again to %q: the retirement chains", want, loc)
	}
	if !detailPage(landed.Body.String()) {
		t.Errorf("GET %s did not render the task detail:\n%s", want, landed.Body.String())
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
// asserts none of the listed URLs carries an error alert: every one of them
// must be rendering real content, not a well-chromed error.
//
// It follows redirects, and that is the point: half of these URLs are now
// pre-redesign redirects, and a 302 carries no page. Checking the redirect's
// own body would find no alert on every one of them and quietly stop
// covering the Tasks and Board pages entirely -- the check would survive a
// fixture that made every read behind them fail. So it asserts on the page
// each URL actually lands the operator on.
func TestPreRedesignURLsRenderNoReadFailure(t *testing.T) {
	f := newLegacyFixture(t)

	for _, url := range f.urls() {
		t.Run(url, func(t *testing.T) {
			_, final := followRedirect(t, f, url)
			body := fetch(t, f.mux, final).Body.String()
			for _, marker := range []string{`alert-error`, `role="alert"`} {
				if strings.Contains(body, marker) {
					t.Errorf("GET %s landed on %s rendering %q: the fixture answered a read with a failure the walk does not see", url, final, marker)
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

	// The product-scoped detail, not the pre-redesign URL: that one is a
	// 302 now, so fetching it here would assert on a redirect body rather
	// than on the page a failing slice read actually degrades.
	body := fetch(t, mux, productTaskDetailPath(pid, tid)).Body.String()
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
