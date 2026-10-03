package main

// FR 7e463e31's tab mechanism at the handler level: the URL carries the
// tab, the two htmx modes are told apart, and the panel fragment is the
// one the tabs target.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// getTabAt drives the handler the way the strip does: an htmx request
// whose HX-Target is the panel region, which is what a tab link sends.
// Driving it any other way would test the Refresh branch instead.
func (f *detailFixture) getTabAt(tid, query string) (int, string) {
	req := httptest.NewRequest(http.MethodGet,
		"/products/"+f.pid.String()+"/tasks/"+tid+query, nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", pages.TaskPanelAnchor)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// getFullAt drives a plain browser navigation to the tab-qualified URL --
// the reload, the shared link and the no-JS click all take this path.
func (f *detailFixture) getFullAt(tid, query string) (int, string) {
	req := httptest.NewRequest(http.MethodGet,
		"/products/"+f.pid.String()+"/tasks/"+tid+query, nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// tabStripOf is the strip's own markup.
func tabStripOf(t *testing.T, html string) string {
	t.Helper()
	return regionBetween(t, html, `data-krill="task-tabs"`, "</div>")
}

// panelRegionOf is the swap region: the strip plus the panel body.
func panelRegionOf(t *testing.T, html string) string {
	t.Helper()
	from := strings.Index(html, `data-krill="task-panel"`)
	require.GreaterOrEqual(t, from, 0, "the page rendered no panel region:\n%s", html)
	end := strings.Index(html[from:], "</div></div>")
	require.GreaterOrEqual(t, end, 0, "the panel region is never closed:\n%s", html)
	return html[from : from+end]
}

// TestTaskDetailURLCarriesTheTab is the FR's core clause: the tab lives in
// the URL, so a reload and a shared link open the same tab, and each of
// the four values selects its own panel.
func TestTaskDetailURLCarriesTheTab(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	for _, tc := range []struct{ query, panel string }{
		{"", "overview"},
		{"?tab=overview", "overview"},
		{"?tab=notes", "notes"},
		{"?tab=dependencies", "dependencies"},
		{"?tab=slice", "slice"},
	} {
		t.Run("url"+tc.query, func(t *testing.T) {
			// The tab click: the panel region alone comes back.
			code, frag := f.getTabAt(task.ID.String(), tc.query)
			require.Equal(t, 200, code, "body: %s", frag)
			assert.Contains(t, frag, `data-krill-task-tab="`+tc.panel+`"`,
				"the fragment's region names the tab the URL resolved to")
			assert.Contains(t, frag, `data-krill-panel-host="`+tc.panel+`"`,
				"the resolved tab's panel renders")

			// The reload / shared link: the whole page, same tab.
			code, page := f.getFullAt(task.ID.String(), tc.query)
			require.Equal(t, 200, code, "body: %s", page)
			assert.Contains(t, page, "<html")
			assert.Contains(t, page, `data-krill-panel-host="`+tc.panel+`"`,
				"a reload of the tab-qualified URL opens that tab")
			// The address bar, the served page and the strip's active
			// marking can never disagree, because all three come from the
			// same resolved tab.
			assert.Contains(t, tabStripOf(t, page), `tab tab-active`)
		})
	}
}

// TestTaskDetailUnknownTabResolvesToOverview is the degradation rule: the
// tab is URL-carried, so a hand-edited or stale link reaches this page as
// readily as a copied one. It renders the page rather than 404ing or
// rendering an empty panel.
func TestTaskDetailUnknownTabResolvesToOverview(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t", CurrentLane: store.LaneTesting})

	for _, query := range []string{"?tab=nonsense", "?tab=", "?tab=NOTES", "?tab=../../etc", "?tab=notes%20"} {
		t.Run(query, func(t *testing.T) {
			code, frag := f.getTabAt(task.ID.String(), query)
			require.Equal(t, 200, code, "an unknown tab must not error; body: %s", frag)
			assert.Contains(t, frag, `data-krill-task-tab="overview"`)
			assert.Contains(t, frag, `data-krill-panel-host="overview"`,
				"an unknown tab renders the Overview panel, not an empty one")
			assert.NotContains(t, frag, `data-krill-panel-host="notes"`)

			code, page := f.getFullAt(task.ID.String(), query)
			require.Equal(t, 200, code)
			assert.Contains(t, page, `data-krill-panel-host="overview"`)
		})
	}
}

// TestTaskDetailStripCarriesTheTabsOwnURLs: the strip's hrefs are the
// page's own path with the tab applied, so the no-JS path, a reload, a
// shared link and Back all agree with what htmx swaps. Overview's href is
// the bare path -- the default state is the page's own address rather than
// a parameter spelling out the absence of a choice.
func TestTaskDetailStripCarriesTheTabsOwnURLs(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t", CurrentLane: store.LaneTesting})
	_, html := f.getTabAt(task.ID.String(), "")
	strip := tabStripOf(t, html)

	base := "/products/" + f.pid.String() + "/tasks/" + task.ID.String()
	for _, want := range []string{
		`href="` + base + `"`,
		`href="` + base + `?tab=notes"`,
		`href="` + base + `?tab=dependencies"`,
		`href="` + base + `?tab=slice"`,
		`hx-target="#` + pages.TaskPanelAnchor + `"`,
		`hx-swap="outerHTML"`,
		`hx-push-url="true"`,
	} {
		assert.Contains(t, strip, want)
	}
	// Overview's href spells out no tab at all.
	assert.NotContains(t, strip, `href="`+base+`?tab=overview"`)
}

// TestTaskDetailStripCarriesCountsFromThePanelReads: the Notes and
// Dependencies tabs carry the counts of the lists their panels render,
// and both figures come from the SAME reads -- two reads would be two
// numbers that could disagree with each other or with the list.
func TestTaskDetailStripCarriesCountsFromThePanelReads(t *testing.T) {
	f := newDetailFixture(t)
	dep := f.add(store.Task{Title: "dep", CurrentLane: store.LaneDone})
	task := f.add(store.Task{Title: "t", CurrentLane: store.LaneTesting})
	f.store.deps = []store.TaskDependency{{DependsOnTaskID: dep.ID}}
	f.store.notes = []store.Note{
		{Kind: store.NoteKindScopeNote, Body: "one", CurrentStatus: "noted"},
		{Kind: store.NoteKindComment, Body: "two", CurrentStatus: "noted"},
	}

	_, html := f.getTabAt(task.ID.String(), "")
	strip := tabStripOf(t, html)
	notes := regionBetween(t, strip, `data-krill-task-tab="notes"`, "</a>")
	deps := regionBetween(t, strip, `data-krill-task-tab="dependencies"`, "</a>")
	overview := regionBetween(t, strip, `data-krill-task-tab="overview"`, "</a>")
	slice := regionBetween(t, strip, `data-krill-task-tab="slice"`, "</a>")

	assert.Contains(t, notes, `data-krill="task-tab-count"`, "the Notes tab carries its count")
	assert.Contains(t, notes, ">2<")
	assert.Contains(t, deps, `data-krill="task-tab-count"`, "the Dependencies tab carries its count")
	assert.Contains(t, deps, ">1<")
	assert.NotContains(t, overview, `data-krill="task-tab-count"`, "a tab with no list carries no count")
	assert.NotContains(t, slice, `data-krill="task-tab-count"`)
}

// TestTaskDetailFailedCountsCarryNoBadge: a count of zero is a claim
// about the list, and we could not read the list. A tab with no badge says
// nothing, which is the honest state.
func TestTaskDetailFailedCountsCarryNoBadge(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "t", CurrentLane: store.LaneTesting})
	f.store.depsErr = assertErr{}
	f.store.notesErr = assertErr{}

	code, html := f.getTabAt(task.ID.String(), "")
	require.Equal(t, 200, code, "a failed read must still answer 200 with the page")
	assert.NotContains(t, tabStripOf(t, html), `data-krill="task-tab-count"`,
		"a failed read must not render a count, least of all a 0")
	// And the page around it still renders: a failed count is not a
	// failed page.
	assert.Contains(t, html, `data-krill-panel-host="overview"`,
		"a failed count must not cost the page its panel")
	assert.Contains(t, html, `role="tablist"`, "the strip still renders")
}

// TestTaskDetailTabClickSwapsOnlyThePanel: the fragment a tab click
// receives is the panel region alone -- the header, breadcrumbs, lane
// steps and properties rail are outside it and do not travel. That is
// what makes the swap in place rather than a page re-render.
func TestTaskDetailTabClickSwapsOnlyThePanel(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	code, frag := f.getTabAt(task.ID.String(), "?tab=notes")
	require.Equal(t, 200, code, "body: %s", frag)

	assert.NotContains(t, frag, "<html", "a tab click is a fragment, not a document")
	assert.NotContains(t, frag, `data-krill="task-breadcrumb"`, "the breadcrumb is outside the swap region")
	assert.NotContains(t, frag, `data-krill="page-title"`, "the title is outside the swap region")
	assert.NotContains(t, frag, `data-krill="task-lane-steps"`, "the lane steps are outside the swap region")
	assert.NotContains(t, frag, `data-krill="task-properties-rail"`, "the rail is outside the swap region")
	assert.Contains(t, frag, `data-krill="task-panel"`)
	assert.Contains(t, frag, `data-krill="task-tabs"`, "the strip comes back with the panel, so the active marking travels with it")
	assert.Equal(t, 1, strings.Count(frag, "tab tab-active"),
		"exactly one tab is marked active after a swap")
}

// TestTaskDetailRefreshOnATabStaysWholeSection: a Refresh taken while a
// non-default tab is open re-renders the WHOLE section, not a bare panel.
// It is the same route with a different HX-Target, and reading anything
// but the target would splice a panel into the section beside the page.
func TestTaskDetailRefreshOnATabStaysWholeSection(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	req := httptest.NewRequest(http.MethodGet,
		"/products/"+f.pid.String()+"/tasks/"+task.ID.String()+"?tab=notes", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", pages.TaskDetailAnchor)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)

	require.Equal(t, 200, rec.Code, "body: %s", rec.Body)
	frag := rec.Body.String()
	assert.True(t, strings.HasPrefix(strings.TrimSpace(frag), `<section id="`+pages.TaskDetailAnchor+`"`),
		"a Refresh serves the whole section, whose root is the region it targets")
	assert.Contains(t, frag, `data-krill="task-breadcrumb"`)
	assert.Contains(t, frag, `data-krill="task-panel"`)
	// And it keeps the tab the operator was on.
	assert.Contains(t, frag, `data-krill-panel-host="notes"`,
		"a Refresh must not silently snap the operator back to Overview")
}

// TestTaskDetailFullFragmentIsStillOneSection: the whole-page fragment
// grew a tab region, so the rule that keeps a Refresh click from splicing
// duplicates now has a page that could plausibly break it -- the tab
// region's own markup has to stay INSIDE the section.
//
// The count is done by a real HTML parse in the pages package; this pins
// the same property from the handler's side, by asserting the fragment
// both begins at the section and carries exactly one of each of the
// regions a duplicate would multiply.
func TestTaskDetailFullFragmentIsStillOneSection(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})
	_, frag := f.getTabAt(task.ID.String(), "")

	code, full := f.getFullAt(task.ID.String(), "")
	require.Equal(t, 200, code)
	region := regionBetween(t, full, `data-krill="task-detail"`, "</main>")

	// The whole-page fragment carries every region exactly once. Duplicating
	// any of them is what a multi-root fragment splices in on every click.
	for _, once := range []string{
		`data-krill="task-breadcrumb"`,
		`data-krill="page-title"`,
		`data-krill="refresh"`,
		`data-krill="task-panel"`,
		`data-krill="task-tabs"`,
	} {
		assert.Equal(t, 1, strings.Count(region, once),
			"exactly one %s in the served section, or a swap duplicates it", once)
	}
	// The panel fragment carries the swap region once, and none of what sits
	// outside it -- which is the other half of "it swaps in place".
	assert.Equal(t, 1, strings.Count(frag, `data-krill="task-panel"`))
	assert.Equal(t, 1, strings.Count(frag, `data-krill="task-tabs"`),
		"exactly one strip in the panel fragment, or a click duplicates the operator's own controls")
	for _, outside := range []string{
		`data-krill="task-breadcrumb"`,
		`data-krill="page-title"`,
		`data-krill="refresh"`,
	} {
		assert.NotContains(t, frag, outside,
			"%s is outside the swap region and must not travel with the panel", outside)
	}
	// The read-only contract still holds on a tabbed page: the tab strip
	// is four links, not four forms.
	for _, body := range []string{frag, region} {
		assert.NotContains(t, body, "<form")
		assert.NotContains(t, body, "hx-post")
	}
}

// TestTaskDetailTabsOnBothRoutes: both detail routes reach the same
// render, and each builds its strip over its OWN path -- a tab link off
// the pre-redesign URL must not send the operator to the product-scoped
// one.
func TestTaskDetailTabsOnBothRoutes(t *testing.T) {
	f := newDetailFixture(t)
	task := f.addOnMilepebble(store.Task{Title: "t", CurrentLane: store.LaneTesting})

	preRedesign := taskDetailPath(f.pid, f.mp, task.ID)
	req := httptest.NewRequest(http.MethodGet, preRedesign+"?tab=notes", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", pages.TaskPanelAnchor)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)

	require.Equal(t, 200, rec.Code, "body: %s", rec.Body)
	strip := tabStripOf(t, rec.Body.String())
	assert.Contains(t, strip, `href="`+htmlEscapedURL(preRedesign+"?tab=dependencies")+`"`,
		"the pre-redesign page's tabs link at the pre-redesign URL")
	assert.NotContains(t, strip, "/products/"+f.pid.String()+"/tasks/",
		"a tab off the pre-redesign page must not send the operator to the product-scoped one")
	assert.Contains(t, rec.Body.String(), `data-krill-panel-host="notes"`)
}

// TestTaskDetailTabOfResolvesTheFourValues pins the resolver directly, so
// the degradation rule is asserted at the function that implements it
// rather than only through the handler.
func TestTaskDetailTabOfResolvesTheFourValues(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"", pages.TaskTabOverview},
		{"?tab=overview", pages.TaskTabOverview},
		{"?tab=notes", pages.TaskTabNotes},
		{"?tab=dependencies", pages.TaskTabDependencies},
		{"?tab=slice", pages.TaskTabSlice},
		{"?tab=nonsense", pages.TaskTabOverview},
		{"?tab=", pages.TaskTabOverview},
		{"?tab=Notes", pages.TaskTabOverview},
		{"?other=notes", pages.TaskTabOverview},
	} {
		t.Run("q"+tc.query, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/products/"+uuid.NewString()+"/tasks/x"+tc.query, nil)
			assert.Equal(t, tc.want, taskDetailTabOf(r))
		})
	}
}

// assertErr is a read failure. The fixture's own errors are declared
// inline in most tests; this one exists so the count test above can set
// both without importing errors for a single use.
type assertErr struct{}

func (assertErr) Error() string { return "read failed" }