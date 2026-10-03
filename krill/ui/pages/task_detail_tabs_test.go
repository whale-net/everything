package pages

// FR 7e463e31's tab mechanism at the component level: the strip, the panel
// region, and the fragment shape that makes a tab click repeatable.
//
// The shape assertions here parse the rendered markup with a real HTML
// parser rather than counting tags by hand, because that is the only way
// to be sure the served fragment's ROOT is the region hx-target names --
// the invariant whose absence makes every subsequent click splice a
// duplicate into the page.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// topLevelOf returns the tag names of every top-level ELEMENT in body,
// using a real HTML parser.
//
// htmx's outerHTML swap inserts every top-level node of the response into
// the target, so this count is not a style assertion: a fragment with two
// roots splices the second one in on every click, without bound.
func topLevelOf(t *testing.T, markup string) []string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	require.NoError(t, err, "the fragment must be parseable HTML")
	var body *html.Node
	var findBody func(*html.Node)
	findBody = func(n *html.Node) {
		if body != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findBody(c)
		}
	}
	findBody(doc)
	require.NotNil(t, body, "the fragment must parse to a body")

	var tops []string
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			tops = append(tops, c.Data)
		}
	}
	return tops
}

// attrOf is one attribute of the first element carrying hook, so a test
// can assert against the parsed attribute rather than a substring of the
// serialised tag.
func attrOf(t *testing.T, markup, hook, attr string) (string, bool) {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	require.NoError(t, err)
	var found string
	var ok bool
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != "" {
			return
		}
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "data-krill" && a.Val == hook {
					for _, b := range n.Attr {
						if b.Key == attr {
							found, ok = b.Val, true
						}
					}
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return found, ok
}

// tabsFor is the strip the builder produces: counts on the two tabs whose
// panels are lists, and none on the other two.
func tabsFor(active string) []TaskTab {
	return []TaskTab{
		{Key: TaskTabOverview, Label: "Overview", Href: "/products/p/tasks/t", Active: active == TaskTabOverview},
		{Key: TaskTabNotes, Label: "Notes", Href: "/products/p/tasks/t?tab=notes", Count: 2, HasCount: true, Active: active == TaskTabNotes},
		{Key: TaskTabDependencies, Label: "Dependencies", Href: "/products/p/tasks/t?tab=dependencies", Count: 1, HasCount: true, Active: active == TaskTabDependencies},
		{Key: TaskTabSlice, Label: "Spec slice", Href: "/products/p/tasks/t?tab=slice", Active: active == TaskTabSlice},
	}
}

// TestTaskDetailPanelFragmentIsExactlyItsSwapTarget is the invariant that
// decides whether a tab click works at all, and it is checked against a
// real parse of the fragment rather than a hand-rolled tag scan.
//
// The tabs do hx-target="#krill-task-panel" hx-swap="outerHTML", so what
// htmx inserts is every top-level node of the response. A second top-level
// node therefore splices itself in beside the panel on every click -- the
// strip duplicating once per click is the visible symptom, but nothing
// about it is recoverable by the operator.
func TestTaskDetailPanelFragmentIsExactlyItsSwapTarget(t *testing.T) {
	for _, tab := range []string{TaskTabOverview, TaskTabNotes, TaskTabDependencies, TaskTabSlice} {
		t.Run(tab, func(t *testing.T) {
			fragment := renderBody(t, TaskDetailTabs(TaskDetailPage{Tab: tab, Title: "A task", Tabs: tabsFor(tab)}))

			top := topLevelOf(t, fragment)
			require.Len(t, top, 1,
				"the served panel fragment must be exactly its swap target, or every tab click splices the extra top-level nodes into the page")
			assert.Equal(t, "div", top[0])

			id, ok := attrOf(t, fragment, "task-panel", "id")
			require.True(t, ok, "the fragment must carry a data-krill=\"task-panel\" region")
			assert.Equal(t, TaskPanelAnchor, id,
				"the fragment's root must carry the id the tabs' hx-target names; without it htmx deletes the element it was asked to replace and every later click is a no-op that never reaches the server")

			// The strip is inside the region, so it comes back with the
			// panel -- exactly once, or a click duplicates the operator's
			// own controls.
			assert.Equal(t, 1, strings.Count(fragment, `data-krill="task-tabs"`),
				"exactly one tab strip in the fragment, or a click duplicates it")
			assert.Equal(t, 1, strings.Count(fragment, `data-krill="task-panel-body"`),
				"exactly one panel body in the fragment, or a click duplicates it")
		})
	}
}

// TestTaskDetailStripIsFourRealLinksCarryingTheSwap pins the four
// mechanisms the FR names at once: the strip is a daisyUI tabs/tab
// tablist, each tab is a real href (so no-JS, a reload, a shared link and
// Back all agree), and each carries the htmx attributes that swap the
// panel in place and push the tab's own URL.
func TestTaskDetailStripIsFourRealLinksCarryingTheSwap(t *testing.T) {
	fragment := renderBody(t, TaskDetailTabs(TaskDetailPage{Tab: TaskTabNotes, Tabs: tabsFor(TaskTabNotes)}))

	assert.Contains(t, fragment, `role="tablist"`)
	assert.Contains(t, fragment, `class="tabs tabs-border"`, "daisyUI's own tabs component, not a hand-rolled one")
	assert.Contains(t, fragment, `class="tab tab-active"`, "the active tab is marked with daisyUI's tab-active")
	assert.Contains(t, fragment, `class="tab"`, "the inactive tabs are not")
	assert.Contains(t, fragment, `aria-selected="true"`)
	assert.Contains(t, fragment, `aria-selected="false"`)
	assert.Contains(t, fragment, `role="tabpanel"`)

	for _, want := range []string{
		// A real href per tab -- Overview's is the bare path, because the
		// default tab is the page's own address rather than a parameter
		// spelling out the absence of a choice.
		`href="/products/p/tasks/t"`,
		`href="/products/p/tasks/t?tab=notes"`,
		`href="/products/p/tasks/t?tab=dependencies"`,
		`href="/products/p/tasks/t?tab=slice"`,
		`hx-get="/products/p/tasks/t?tab=notes"`,
		`hx-target="#` + TaskPanelAnchor + `"`,
		`hx-swap="outerHTML"`,
		`hx-push-url="true"`,
	} {
		assert.Contains(t, fragment, want)
	}

	// Exactly one active marking: a strip that marked two tabs would tell
	// the operator the page is showing two facets at once.
	assert.Equal(t, 1, strings.Count(fragment, "tab tab-active"))
	// The panel body is labelled by the tab the URL resolved to.
	labelled, ok := attrOf(t, fragment, "task-panel-body", "aria-labelledby")
	require.True(t, ok)
	assert.Equal(t, TaskTabID(TaskTabNotes), labelled)
}

// TestTaskDetailTabsCarryTheirCounts: the Notes and Dependencies tabs
// carry the counts of the lists their panels render, and the two tabs
// whose panels have no list carry none.
func TestTaskDetailTabsCarryTheirCounts(t *testing.T) {
	fragment := renderBody(t, TaskDetailTabs(TaskDetailPage{Tab: TaskTabOverview, Tabs: tabsFor(TaskTabOverview)}))

	assert.Equal(t, 2, strings.Count(fragment, `data-krill="task-tab-count"`),
		"exactly the two counted tabs carry a count badge")
	assert.Contains(t, fragment, "Notes <span")
}

// TestTaskDetailPanelsRenderOnlyTheSelectedTab: the strip marks one tab
// active and the panel body renders that tab's host, so a fragment never
// answers with two facets' content at once.
func TestTaskDetailPanelsRenderOnlyTheSelectedTab(t *testing.T) {
	for _, tc := range []struct{ tab, host string }{
		{TaskTabOverview, "overview"},
		{TaskTabNotes, "notes"},
		{TaskTabDependencies, "dependencies"},
		{TaskTabSlice, "slice"},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			fragment := renderBody(t, TaskDetailTabs(TaskDetailPage{Tab: tc.tab, Tabs: tabsFor(tc.tab)}))
			for _, other := range []string{"overview", "notes", "dependencies", "slice"} {
				marker := `data-krill-panel-host="` + other + `"`
				if other == tc.host {
					assert.Contains(t, fragment, marker, "the selected tab's panel must render")
					continue
				}
				assert.NotContains(t, fragment, marker, "an unselected tab's panel must not render")
			}
			active, ok := attrOf(t, fragment, "task-panel", "data-krill-task-tab")
			require.True(t, ok)
			assert.Equal(t, tc.tab, active)
		})
	}
}

// TestTaskDetailPanelsAreSiblingFillableHosts: each panel is one clearly
// marked region, so the task that owns a panel's contents fills that
// region and touches nothing else. This is what makes the split into
// three sibling tasks possible at all.
func TestTaskDetailPanelsAreSiblingFillableHosts(t *testing.T) {
	for _, tc := range []struct{ tab, host string }{
		{TaskTabOverview, "overview"},
		{TaskTabNotes, "notes"},
		{TaskTabDependencies, "dependencies"},
		{TaskTabSlice, "slice"},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			fragment := renderBody(t, TaskDetailTabs(TaskDetailPage{Tab: tc.tab, Tabs: tabsFor(tc.tab)}))
			host, ok := attrOf(t, fragment, "task-panel-"+tc.tab, "data-krill-panel-host")
			require.True(t, ok,
				"panel %q must be one marked host region a sibling can fill without restructuring the page", tc.tab)
			assert.Equal(t, tc.host, host)
		})
	}
}

// TestTaskDetailPanelsCarryTheirOwnReadFailures: a failed notes or
// dependencies read alerts INSIDE that panel and nowhere else. An empty
// Notes renders a confident "No notes." that is indistinguishable from
// success, so the error branch must be the one that renders.
func TestTaskDetailPanelsCarryTheirOwnReadFailures(t *testing.T) {
	page := func(tab string) TaskDetailPage {
		return TaskDetailPage{
			Tab: tab, Tabs: tabsFor(tab),
			Notes: []TaskNoteRow{{Kind: "scope-note", Status: "noted", Body: "note body"}},
			NotesError: "The notes could not be read. See the logs.",
			Deps:       []TaskDepLink{{Title: "dep task", Lane: "Done", DetailPath: "/products/p/tasks/d"}},
			DepsError:  "The dependencies could not be read. See the logs.",
		}
	}

	notes := renderBody(t, TaskDetailTabs(page(TaskTabNotes)))
	assert.Contains(t, notes, "The notes could not be read")
	assert.NotContains(t, notes, "No notes", "a failed read must never render as an empty list")
	assert.NotContains(t, notes, "note body", "a failed read must not render rows it could not read")
	assert.NotContains(t, notes, "The dependencies could not be read",
		"the dependencies alert belongs to the dependencies panel alone")
	assert.NotContains(t, notes, "dep task")

	deps := renderBody(t, TaskDetailTabs(page(TaskTabDependencies)))
	assert.Contains(t, deps, "The dependencies could not be read")
	assert.NotContains(t, deps, "No dependencies")
	assert.NotContains(t, deps, "dep task")
	assert.NotContains(t, deps, "The notes could not be read")

	// The counts came from the reads that failed, so they are absent
	// rather than zero -- zero would be a claim about a list nobody read.
	overview := renderBody(t, TaskDetailTabs(page(TaskTabOverview)))
	assert.Equal(t, 1, strings.Count(overview, `data-krill="task-tabs"`))
	assert.Contains(t, overview, `data-krill-task-tab="notes"`)
}