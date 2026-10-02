// The legacy-URL continuity contract (FR 2544224c): no pre-redesign URL
// ever returns 404, and one whose page has been replaced lands on that
// page's redesigned successor.
package main

import (
	"context"
	"net/http"
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

func (l legacyTasks) ListTasksByMilestone(_ context.Context, id uuid.UUID) ([]store.TaskSummary, error) {
	if id != l.milestoneID {
		return nil, nil
	}
	return []store.TaskSummary{{ID: l.taskID, Title: "Test task"}}, nil
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
	app := newTestApp(t)
	pid, mid, tid, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New()

	app.spec = &fakeSpecReader{
		products: []store.Product{{ID: pid, Name: "Test product"}},
		product:  store.Product{ID: pid, Name: "Test product"},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{
			{ID: mid, Name: "Test milestone", Status: store.MilestoneStatusInProgress},
		}},
	}
	// The console views read the task store rather than the badge-only
	// counter the chrome fixtures install, so a page that lists tasks
	// would otherwise nil-panic instead of rendering its empty state.
	app.tasks = &legacyTasks{taskID: tid, milestoneID: mid}
	app.designSessions = NewDesignSessions(sid, pid)
	app.revisionEvents = emptyRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
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

			// The page that finally renders must be a real in-shell page,
			// not a bare http.NotFound or a mux 405 leaking through.
			if body := fetch(t, f.mux, final).Body.String(); !strings.Contains(body, "</html>") {
				t.Errorf("GET %s landed on %s, which is not a shell page", url, final)
			}
		})
	}
}

// TestEveryLegacyTableEntryIsCovered keeps the production table and the
// FR's URL list from drifting apart: an entry in legacyURLs that no test
// walks is a URL nobody is holding to the contract.
func TestEveryLegacyTableEntryIsCovered(t *testing.T) {
	f := newLegacyFixture(t)
	covered := f.urls()

	for _, l := range legacyURLs() {
		if l.Pattern == "" {
			t.Fatal("legacyURLs has an entry with no pattern")
		}
		// Patterns carry mux wildcards and method prefixes; the covered
		// list carries concrete paths. Matching on the fixed prefix a
		// pattern's concrete form must start with is what ties the two
		// together without duplicating the pattern syntax here. The
		// wildcard is cut first, then the separator it left behind
		// trimmed -- the other order leaves "/spec/products/" and never
		// matches the concrete path.
		prefix := strings.TrimPrefix(l.Pattern, "GET ")
		if i := strings.IndexByte(prefix, '{'); i >= 0 {
			prefix = prefix[:i]
		}
		prefix = strings.TrimSuffix(prefix, "/")
		if prefix == "/{$}" || prefix == "" {
			continue // the shell home, covered above
		}

		found := false
		for _, u := range covered {
			if u == prefix || strings.HasPrefix(u, prefix+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("legacyURLs registers %s, which no legacy-URL test walks", l.Pattern)
		}
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
		if l.Serve == nil && l.Successor == nil {
			t.Errorf("legacy URL %s names neither a page nor a successor", l.Pattern)
		}
		if l.Serve != nil && l.Successor != nil {
			t.Errorf("legacy URL %s names both a page and a successor", l.Pattern)
		}
	}
}

// TestLegacyRedirectUsesFoundAndNotMoved pins what a replaced URL answers
// with, using a stand-in entry wired exactly as mountLegacyRoutes wires a
// real one. It is what a future phase gets the moment it sets a Successor,
// so the behaviour is asserted now rather than discovered then.
//
// 302 rather than 301 is deliberate: a pre-redesign URL stays a live link
// an operator may keep following, and 301 is the one that lets a browser
// pin the old URL in its cache past the page it now names.
func TestLegacyRedirectUsesFoundAndNotMoved(t *testing.T) {
	f := newLegacyFixture(t)

	entry := legacyURL{
		Pattern: "/legacy-redirect-probe",
		Successor: func(_ *App, _ *http.Request) (string, bool) {
			return productHref(f.pid, overviewSuffix), true
		},
	}
	probe := http.NewServeMux()
	probe.HandleFunc(entry.Pattern, f.app.readerRoute(f.app.serveLegacy(entry)))

	rec := fetch(t, probe, entry.Pattern)
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect status %d, want 302", rec.Code)
	}
	if got, want := rec.Header().Get("Location"), productHref(f.pid, overviewSuffix); got != want {
		t.Errorf("redirect Location %q, want %q", got, want)
	}
	if got := fetch(t, f.mux, rec.Header().Get("Location")).Code; got != http.StatusOK {
		t.Errorf("successor page renders %d, want 200", got)
	}
}

// TestLegacyRedirectWithNoProductStaysInShell covers the one case a legacy
// URL cannot redirect on: an empty scope resolves no product to build a
// successor from, and an un-prefixed URL must still land somewhere. It
// renders the product index rather than redirecting to nowhere.
func TestLegacyRedirectWithNoProductStaysInShell(t *testing.T) {
	app := newTestApp(t)
	app.spec = &fakeSpecReader{} // a scope holding no product
	app.tasks = emptyListTasks{}

	entry := legacyURL{
		Pattern: opsEscalatedPath,
		Successor: func(a *App, r *http.Request) (string, bool) {
			product, err := a.resolveProductForUnprefixed(r)
			if err != nil || product.ID == uuid.Nil {
				return "", false
			}
			return productHref(product.ID, needsAttentionSuffix), true
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(entry.Pattern, app.readerRoute(app.serveLegacy(entry)))

	rec := fetch(t, mux, entry.Pattern)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: an empty scope must still land on a page", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "</html>") {
		t.Error("empty-scope legacy URL did not render a shell page")
	}
}
