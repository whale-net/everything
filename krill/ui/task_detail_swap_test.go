package main

// FR 7e463e31's swap mechanics, probed the way htmx actually drives them:
// by the shape of the bytes the handler serves, parsed with a real HTML
// parser rather than counted as substrings.
//
// The invariant that decides whether the feature works at all is that a
// fragment is exactly its swap target -- one top-level node, carrying the
// id hx-target names. Get it wrong and nothing errors: htmx inserts every
// top-level node of the response into the target, so a second root is a
// duplicate strip spliced in on every click, and a missing id is the
// element deleted and every later click a no-op that never reaches the
// server. Both are invisible to a status check, which is why they are
// asserted against a parse here.

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// topLevelTagsOf is the tag name of every top-level ELEMENT in a served
// fragment.
//
// The fragment is parsed as a document rather than scanned for tags,
// because the count is the whole point: a body with two element children
// is a response that splices itself in twice.
func topLevelTagsOf(t *testing.T, markup string) []string {
	t.Helper()
	body := parsedBody(t, markup)
	var tags []string
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			tags = append(tags, c.Data)
		}
	}
	return tags
}

// parsedBody is the fragment's parsed <body>, which is where a fragment of
// top-level elements lands once the parser wraps it in a document.
func parsedBody(t *testing.T, markup string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	require.NoError(t, err, "the served fragment must be parseable HTML:\n%s", markup)
	var body *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if body != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "body" {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	require.NotNil(t, body, "the served fragment must parse to a body:\n%s", markup)
	return body
}

// topLevelElement is the fragment's single top-level element, asserted to
// be single. This is the README's swap-target rule expressed as a parse:
// what htmx inserts into the target is every top-level node of the
// response, so "exactly one" is the requirement, not "at least one".
func topLevelElement(t *testing.T, markup string) *html.Node {
	t.Helper()
	tags := topLevelTagsOf(t, markup)
	require.Len(t, tags, 1,
		"the served fragment must be exactly its swap target; %d top-level elements means htmx inserts %d of them on every click", len(tags), len(tags))
	for c := parsedBody(t, markup).FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			return c
		}
	}
	t.Fatal("unreachable: topLevelTagsOf already proved one element")
	return nil
}

// attrOfNode reads one attribute off a parsed element.
func attrOfNode(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// elementsWithHook is every element in the fragment carrying
// data-krill="<hook>", parsed -- so an assertion about a hook is about
// that hook's elements and not about a substring that happens to appear
// somewhere else in the markup.
func elementsWithHook(t *testing.T, markup, hook string) []*html.Node {
	t.Helper()
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if v, ok := attrOfNode(n, "data-krill"); ok && v == hook {
				found = append(found, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(parsedBody(t, markup))
	return found
}

// allAttrsIn is every href in the fragment, parsed and unescaped by the
// parser (so an assertion is about the address a browser would follow,
// not about its serialised form).
func allAttrsIn(t *testing.T, markup, key string) []string {
	t.Helper()
	var vals []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if v, ok := attrOfNode(n, key); ok {
				vals = append(vals, v)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(parsedBody(t, markup))
	return vals
}

// getDetailAt drives the handler at a raw path with the headers htmx
// sends, which is the only way to reach the handler's own dispatch: the
// three modes are told apart by HX-Target, so a request without it is the
// Refresh branch and a request with it is the tab branch.
func (f *detailFixture) getDetailAt(path, hxTarget string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	if hxTarget != "" {
		req.Header.Set("HX-Target", hxTarget)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// detailPathAt is the fixture's product-scoped detail address, the one the
// strip is built over.
func (f *detailFixture) detailPathAt(tid string) string {
	return "/products/" + f.pid.String() + "/tasks/" + tid
}

// TestTaskDetailPanelFragmentHasExactlyOneTopLevelNodeCarryingTheSwapID is
// the invariant a tab click lives or dies by, checked on EVERY tab through
// the handler that serves them.
//
// A real parse, not a tag count: a hand-rolled scanner that looks for the
// opening tag alone cannot tell one root from two, which is the exact
// distinction at stake.
func TestTaskDetailPanelFragmentHasExactlyOneTopLevelNodeCarryingTheSwapID(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	for _, tab := range []string{pages.TaskTabOverview, pages.TaskTabNotes, pages.TaskTabDependencies, pages.TaskTabSlice} {
		t.Run(tab, func(t *testing.T) {
			query := ""
			if tab != pages.TaskTabOverview {
				query = "?tab=" + tab
			}
			code, frag := f.getTabAt(task.ID.String(), query)
			require.Equal(t, 200, code, "body: %s", frag)

			root := topLevelElement(t, frag)
			assert.Equal(t, "div", root.Data, "the panel region's root is a div")
			id, ok := attrOfNode(root, "id")
			require.True(t, ok, "the fragment's root must carry an id:\n%s", frag)
			assert.Equal(t, pages.TaskPanelAnchor, id,
				"the root must carry the id hx-target names: without it htmx deletes the element it was asked to replace and every later click is a no-op that never reaches the server")
			active, ok := attrOfNode(root, "data-krill-task-tab")
			require.True(t, ok)
			assert.Equal(t, tab, active, "the region names the tab the URL resolved to, which is what re-marks the active tab on the next click")
		})
	}
}

// TestTaskDetailWholeSectionFragmentHasExactlyOneTopLevelNode is the same
// invariant for the Refresh branch: the section fragment's root is the
// section hx-target names. A second top-level node here would splice a
// copy of the header beside the header on every Refresh.
func TestTaskDetailWholeSectionFragmentHasExactlyOneTopLevelNode(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	for _, tab := range []string{pages.TaskTabOverview, pages.TaskTabNotes, pages.TaskTabDependencies, pages.TaskTabSlice} {
		t.Run(tab, func(t *testing.T) {
			query := ""
			if tab != pages.TaskTabOverview {
				query = "?tab=" + tab
			}
			code, frag := f.getDetailAt(f.detailPathAt(task.ID.String())+query, pages.TaskDetailAnchor)
			require.Equal(t, 200, code, "body: %s", frag)

			root := topLevelElement(t, frag)
			assert.Equal(t, "section", root.Data)
			id, ok := attrOfNode(root, "id")
			require.True(t, ok)
			assert.Equal(t, pages.TaskDetailAnchor, id,
				"the Refresh fragment's root must be the region the Refresh button targets")
		})
	}
}

// TestTaskDetailRefreshOnANonDefaultTabReturnsTheWholeSectionNotABarePanel
// is the bug the HX-Target dispatch exists to prevent.
//
// The Refresh button asks the same URL a tab does -- it carries no ?tab=
// of its own beyond the one in the address bar -- so a handler that
// decided from the URL would serve the panel to a swap whose target is the
// whole section, and htmx would splice a bare panel in beside the page
// with nothing left to close it.
func TestTaskDetailRefreshOnANonDefaultTabReturnsTheWholeSectionNotABarePanel(t *testing.T) {
	f := newDetailFixture(t)
	// A lane_sequence, so the step strip renders: a task created without
	// one has no steps to re-derive, and asserting they come back would
	// then be asserting nothing.
	task := f.add(store.Task{
		Title:        "tabbed",
		CurrentLane:  store.LaneTesting,
		LaneSequence: []store.Lane{store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
	})
	base := f.detailPathAt(task.ID.String())

	for _, tab := range []string{pages.TaskTabNotes, pages.TaskTabDependencies, pages.TaskTabSlice} {
		t.Run(tab, func(t *testing.T) {
			// Driven at the tab-qualified URL on purpose: this asserts the
			// HANDLER's answer, not the button's reachability. That the
			// operator's finger can get there is
			// TestTaskDetailTheServedRefreshButtonReRequestsTheTabItIsServing,
			// which reads the served button instead of assuming its URL.
			code, frag := f.getDetailAt(base+"?tab="+tab, pages.TaskDetailAnchor)
			require.Equal(t, 200, code, "body: %s", frag)

			root := topLevelElement(t, frag)
			assert.Equal(t, "section", root.Data,
				"a Refresh must return the whole section; a bare panel spliced into the section is unrecoverable")
			// Everything a Refresh is for is in it: the header and the
			// rail are what a Refresh re-derives, and what a panel
			// fragment has already thrown away.
			assert.NotEmpty(t, elementsWithHook(t, frag, "task-breadcrumb"), "a Refresh must re-derive the breadcrumb")
			assert.NotEmpty(t, elementsWithHook(t, frag, "page-title"), "a Refresh must re-derive the title")
			assert.NotEmpty(t, elementsWithHook(t, frag, "task-lane-steps"), "a Refresh must re-derive the lane steps")
			assert.NotEmpty(t, elementsWithHook(t, frag, "task-properties-rail"), "a Refresh must re-derive the properties rail")
			// And it keeps the tab, rather than snapping the operator
			// back to Overview behind an address bar still reading
			// ?tab=notes.
			hosts := elementsWithHook(t, frag, "task-panel-"+tab)
			require.NotEmpty(t, hosts, "a Refresh must keep the tab the operator was on")
		})
	}
}

// servedRefreshRequest is the request the SERVED Refresh button actually
// issues, read off the bytes the handler returned: its hx-get, plus the
// value of the input its hx-include names, as htmx would assemble them.
//
// The point of reading it rather than composing one is that a test which
// builds the URL it WISHES the button sent proves nothing about the
// button: it passes whether or not the control can ever issue that
// request, which is exactly how a button pinned to the bare path survived
// a whole pass of validation.
func servedRefreshRequest(t *testing.T, buttonMarkup, regionMarkup string) string {
	t.Helper()
	buttons := elementsWithHook(t, buttonMarkup, "refresh")
	require.Len(t, buttons, 1, "the served fragment must offer exactly one Refresh: %s", buttonMarkup)
	target, ok := attrOfNode(buttons[0], "hx-get")
	require.True(t, ok, "the Refresh button carries no hx-get: %s", buttonMarkup)

	include, hasInclude := attrOfNode(buttons[0], "hx-include")
	if !hasInclude {
		return target
	}
	require.NotEmpty(t, include, "hx-include names nothing, so the button cannot read the tab")
	// The include names the carried input; its value is the tab the panel
	// region was last rendered with, which is the tab the operator is on.
	// Overview carries none, and an include that matches nothing
	// contributes no parameter -- so the request is the bare path.
	carried := elementsWithHook(t, regionMarkup, "task-tab-state")
	if len(carried) == 0 {
		return target
	}
	require.Len(t, carried, 1,
		"the region must carry at most one tab state, or the button's include is ambiguous: %s", regionMarkup)
	value, _ := attrOfNode(carried[0], "value")
	return target + "?" + pages.TaskTabQueryParam + "=" + value
}

// TestTaskDetailTheServedRefreshButtonReRequestsTheTabItIsServing walks
// the operator's own path to the defect, and is the test whose premise is
// the served button rather than a hand-built URL.
//
// The sequence is the one an operator actually performs, and the order is
// the whole point: load the BARE page, click a tab, THEN press Refresh.
// The button is served by the full-page load and is not a descendant of
// the panel region, so the tab click's swap never re-renders it -- the
// button in the DOM is still the one the bare load produced. That is why
// asserting the handler's answer to ?tab=notes is not enough: it proves
// the handler would behave, not that the operator's finger can reach it.
func TestTaskDetailTheServedRefreshButtonReRequestsTheTabItIsServing(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{
		Title:        "tabbed",
		CurrentLane:  store.LaneTesting,
		LaneSequence: []store.Lane{store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone},
	})

	// Step 1: the operator loads the bare page, which is what serves the
	// Refresh button. A full request, not a fragment: this is the load
	// that puts the button in the DOM.
	code, page := f.getFullAt(task.ID.String(), "")
	require.Equal(t, 200, code, "body: %s", page)

	for _, tab := range []string{pages.TaskTabNotes, pages.TaskTabDependencies, pages.TaskTabSlice} {
		t.Run(tab, func(t *testing.T) {
			// Step 2: the operator clicks a tab. The address bar follows
			// and the panel swaps; the button is outside the swap region,
			// so it is untouched -- exactly as it is in the browser.
			code, swapped := f.getTabAt(task.ID.String(), "?tab="+tab)
			require.Equal(t, 200, code, "body: %s", swapped)
			require.NotEmpty(t, elementsWithHook(t, swapped, "task-panel-"+tab))

			// Step 3: the operator presses the button the page actually
			// served, which reads the tab from the region the click
			// actually re-rendered.
			request := servedRefreshRequest(t, page, swapped)
			assert.Contains(t, request, pages.TaskTabQueryParam+"="+tab,
				"a Refresh taken on a non-default tab must re-request that tab, or it re-renders Overview behind an address bar still reading ?tab=%s", tab)

			// And the request it does issue must keep the tab and return
			// the whole section.
			code, refreshed := f.getDetailAt(request, pages.TaskDetailAnchor)
			require.Equal(t, 200, code, "body: %s", refreshed)
			root := topLevelElement(t, refreshed)
			require.Equal(t, "section", root.Data,
				"a Refresh must return the whole section, not a bare panel spliced into it")
			assert.NotEmpty(t, elementsWithHook(t, refreshed, "task-panel-"+tab),
				"a Refresh must keep the tab the operator was on")
		})
	}

	// The default is the bare address, so Overview carries no tab at all:
	// a Refresh there must re-request the bare path rather than a URL
	// spelling out the absence of a choice.
	t.Run("overview carries no tab", func(t *testing.T) {
		code, swapped := f.getTabAt(task.ID.String(), "")
		require.Equal(t, 200, code, "body: %s", swapped)
		assert.Empty(t, elementsWithHook(t, swapped, "task-tab-state"),
			"Overview renders no carried tab: the default state is the bare address")
		assert.Equal(t, f.detailPathAt(task.ID.String()), servedRefreshRequest(t, page, swapped),
			"a Refresh taken on Overview re-requests the bare path")
	})
}

// TestTaskDetailRepeatedTabClicksDoNotSpliceDuplicates is the in-place
// property stated as an operator would hit it: click the same tab twice
// and the page carries one strip, not two.
//
// The strip lives inside the swap region, so every click replaces the
// region and the strip with it. Two clicks is the smallest sequence that
// distinguishes "replaced" from "appended", and it is the sequence the
// duplicate-strip failure produces.
func TestTaskDetailRepeatedTabClicksDoNotSpliceDuplicates(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	// Two clicks at the same tab, then one at another -- the round trip an
	// operator makes while deciding between two facets.
	for _, query := range []string{"?tab=notes", "?tab=notes", "?tab=slice", ""} {
		code, frag := f.getTabAt(task.ID.String(), query)
		require.Equal(t, 200, code, "body: %s", frag)

		assert.Len(t, elementsWithHook(t, frag, "task-tabs"), 1,
			"exactly one strip per response, or each click appends another copy of the operator's own controls")
		assert.Len(t, elementsWithHook(t, frag, "task-panel-body"), 1,
			"exactly one panel body per response")
		assert.Len(t, topLevelTagsOf(t, frag), 1,
			"exactly one top-level node per response: a second root is spliced into the page on every click, without bound")
		// And the region's id survives, which is what makes the NEXT
		// click find a target at all.
		root := topLevelElement(t, frag)
		id, _ := attrOfNode(root, "id")
		assert.Equal(t, pages.TaskPanelAnchor, id)
	}
}

// TestTaskDetailPushURLIsTheTabsOwnHref is what makes the address bar
// follow the swap, asserted at the only place it can be.
//
// htmx pushes the URL it requested, so the pushed address is the tab's
// hx-get -- and the tab's href is what a reload, a shared link, a no-JS
// click and Back all follow. If the two disagree, the address bar, the
// served page and the strip's active marking disagree, which is the one
// outcome the FR's "the URL carries the tab" exists to rule out.
//
// Back and forward need no separate assertion: they replay the browser's
// history, which holds exactly these URLs, and a navigation to any one of
// them is a plain GET that TestTaskDetailURLCarriesTheTab already drives.
func TestTaskDetailPushURLIsTheTabsOwnHref(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	_, frag := f.getTabAt(task.ID.String(), "")
	base := f.detailPathAt(task.ID.String())

	tabs := elementsWithHook(t, frag, "task-tab")
	require.Len(t, tabs, len(taskDetailTabKeys), "the strip renders all four tabs")

	for _, tab := range tabs {
		key, _ := attrOfNode(tab, "data-krill-task-tab")
		href, _ := attrOfNode(tab, "href")
		hxGet, _ := attrOfNode(tab, "hx-get")

		assert.Equal(t, href, hxGet,
			"tab %q: htmx pushes the URL it requested, so an hx-get that differs from the href puts an address in the bar that no reload reproduces", key)

		// And each is a real address on this page, differing only in the
		// tab it names.
		want := base
		if key != pages.TaskTabOverview {
			want += "?tab=" + key
		}
		assert.Equal(t, want, href, "tab %q's href is its own real address on this page", key)

		push, ok := attrOfNode(tab, "hx-push-url")
		require.True(t, ok, "tab %q carries hx-push-url", key)
		assert.Equal(t, "true", push, "tab %q: the address bar must follow the swap", key)

		target, _ := attrOfNode(tab, "hx-target")
		assert.Equal(t, "#"+pages.TaskPanelAnchor, target, "tab %q targets the panel region", key)
		swap, _ := attrOfNode(tab, "hx-swap")
		assert.Equal(t, "outerHTML", swap, "tab %q swaps the panel region, which is what makes the id on the fragment load-bearing", key)
	}
}

// TestTaskDetailTabHrefsCannotCarryAHostileQueryValue is the escaping
// check. The tab is URL-carried, so a hand-edited link reaches this page
// as readily as a copied one, and the href the strip builds is a real
// address the browser will follow.
//
// The value therefore has to be resolved against the four known keys
// BEFORE it is ever written into an address -- not escaped and carried
// through. An unrecognised value that reached an href could name another
// origin (a scheme-ful or protocol-relative value) or another path.
func TestTaskDetailTabHrefsCannotCarryAHostileQueryValue(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})
	base := f.detailPathAt(task.ID.String())

	allowed := map[string]bool{
		base:                                true,
		base + "?tab=" + pages.TaskTabNotes: true,
		base + "?tab=" + pages.TaskTabDependencies: true,
		base + "?tab=" + pages.TaskTabSlice:        true,
	}

	for _, hostile := range []string{
		"?tab=//evil.example/x",
		"?tab=https://evil.example/x",
		"?tab=javascript:alert(1)",
		"?tab=" + uuid.NewString(),
		"?tab=../../etc/passwd",
		"?tab=%22%3E%3Cscript%3E",
		"?tab=notes%20",
		"?tab=NOTES",
	} {
		t.Run(hostile, func(t *testing.T) {
			code, frag := f.getTabAt(task.ID.String(), hostile)
			require.Equal(t, 200, code, "an unrecognised tab must render the page, not error; body: %s", frag)

			for _, href := range allAttrsIn(t, frag, "href") {
				assert.True(t, allowed[href],
					"a rendered href %q is not one of this page's four tab addresses: the query value reached an address", href)
			}
			// The value is gone from the page rather than escaped into it:
			// it resolved to a tab, and only the tab is rendered.
			assert.NotContains(t, frag, "evil.example")
			assert.NotContains(t, frag, "<script")
			assert.Len(t, elementsWithHook(t, frag, "task-panel-overview"), 1,
				"an unrecognised tab renders Overview, the default, rather than an empty panel")
		})
	}
}

// TestTaskDetailTabIgnoresOtherQueryParameters: ?tab= is the tab's state
// and nothing else on the URL is, so a parameter this page does not own
// must not change the tab or leak into an address.
//
// The distinction from the hostile case above matters: a RECOGNISED value
// alongside an unrelated parameter has to keep working. An operator who
// arrives from a list that scoped its own ?scope= must still open the tab
// they asked for, and the strip's hrefs must still be the bare four -- the
// extra parameter belongs to whoever put it there, not to the tab strip.
func TestTaskDetailTabIgnoresOtherQueryParameters(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})
	base := f.detailPathAt(task.ID.String())

	code, frag := f.getTabAt(task.ID.String(), "?tab=notes&scope=milestone&container_id=x")
	require.Equal(t, 200, code, "body: %s", frag)

	assert.Len(t, elementsWithHook(t, frag, "task-panel-notes"), 1,
		"a recognised tab alongside an unrelated parameter still opens that tab")
	// The strip's own addresses are unaffected: a parameter the page does
	// not own must not be carried into the links a click follows, or the
	// address bar would accumulate one per navigation.
	for _, tab := range elementsWithHook(t, frag, "task-tab") {
		href, _ := attrOfNode(tab, "href")
		assert.NotContains(t, href, "scope=",
			"a parameter the page does not own must not be carried into a tab's address")
		assert.NotContains(t, href, "container_id=",
			"a parameter the page does not own must not be carried into a tab's address")
		assert.True(t, strings.HasPrefix(href, base+"?tab=") || href == base,
			"tab href %q is the page's own address with a tab applied, and nothing else", href)
	}
}

// TestTaskDetailTabCountsAgreeWithTheListsTheyCount: a badge is a claim
// about a list, and the only way it can be true is if it is derived from
// the SAME list the panel renders. A count read separately would be a
// second number that could disagree with the list an operator is about to
// click into, and there would be nothing to notice.
func TestTaskDetailTabCountsAgreeWithTheListsTheyCount(t *testing.T) {
	f := newDetailFixture(t)
	depA := f.add(store.Task{Title: "dep-a", CurrentLane: store.LaneDone})
	depB := f.add(store.Task{Title: "dep-b", CurrentLane: store.LaneTesting})
	task := f.add(store.Task{Title: "t", CurrentLane: store.LaneTesting})
	f.store.deps = []store.TaskDependency{{DependsOnTaskID: depA.ID}, {DependsOnTaskID: depB.ID}}
	f.store.notes = []store.Note{
		{Kind: store.NoteKindScopeNote, Body: "note-1", CurrentStatus: "noted"},
		{Kind: store.NoteKindComment, Body: "note-2", CurrentStatus: "carried-over"},
		{Kind: store.NoteKindCheapExpensiveLater, Body: "note-3", CurrentStatus: "closed"},
	}

	// Each tab's badge is read off the tab the URL resolved to, and each
	// panel's rows off the panel that same request rendered -- so the two
	// numbers are compared within one response, not across two.
	for _, tc := range []struct {
		tab      string
		hook     string
		listHook string
		want     int
	}{
		{pages.TaskTabNotes, "task-tab", "task-note", 3},
		{pages.TaskTabDependencies, "task-tab", "task-dep", 2},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			code, frag := f.getTabAt(task.ID.String(), "?tab="+tc.tab)
			require.Equal(t, 200, code, "body: %s", frag)

			tabs := elementsWithHook(t, frag, "task-tab")
			var badge string
			found := false
			for _, tab := range tabs {
				if k, _ := attrOfNode(tab, "data-krill-task-tab"); k != tc.tab {
					continue
				}
				found = true
				for c := tab.FirstChild; c != nil; c = c.NextSibling {
					if v, ok := attrOfNode(c, "data-krill"); ok && v == "task-tab-count" {
						badge = textOf(c)
					}
				}
			}
			require.True(t, found, "the strip must render the %q tab", tc.tab)
			require.NotEmpty(t, badge, "the %q tab must carry its count", tc.tab)
			assert.Equal(t, tc.want, atoiOrFail(t, badge),
				"the %q tab's badge must be the length of the list its panel renders", tc.tab)

			rows := elementsWithHook(t, frag, tc.listHook)
			assert.Len(t, rows, tc.want,
				"the panel must list every item, so the badge and the list cannot disagree")
		})
	}
}

// textOf is an element's rendered text, whitespace-collapsed, which is
// what an operator reads off a badge.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// atoiOrFail parses a rendered integer, failing the test rather than
// silently comparing a string.
func atoiOrFail(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		require.True(t, r >= '0' && r <= '9', "a count badge must render a number, got %q", s)
		n = n*10 + int(r-'0')
	}
	return n
}

// TestTaskDetailStripOrderIsTheFacetOrder: the strip's order is part of
// the page, not an accident of map iteration, and it is the order the
// panel dispatch is written in.
func TestTaskDetailStripOrderIsTheFacetOrder(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})
	_, frag := f.getTabAt(task.ID.String(), "")

	var order []string
	for _, tab := range elementsWithHook(t, frag, "task-tab") {
		key, _ := attrOfNode(tab, "data-krill-task-tab")
		order = append(order, key)
	}
	assert.Equal(t, []string{
		pages.TaskTabOverview, pages.TaskTabNotes, pages.TaskTabDependencies, pages.TaskTabSlice,
	}, order)
}

// TestTaskDetailLegacyWalkReachesTheSlicePanel is the coverage the
// legacy-URL honesty walk can no longer provide for itself.
//
// TestPreRedesignURLsRenderNoReadFailure requests each URL's DEFAULT
// address, which is now the Overview panel, so the slice read -- which
// moved behind ?tab=slice with the rest of the facets -- is one that walk
// cannot see fail. A read this file has stopped watching is a read that
// can break silently.
//
// So the legacy detail URL is walked here at every tab, with the same
// markers, against the same fixture. It is here rather than in
// legacy_urls_test.go because the walk's own table is the pre-redesign
// URL set and this is a facet of one of them.
func TestTaskDetailLegacyWalkReachesTheSlicePanel(t *testing.T) {
	f := newLegacyFixture(t)
	detail := "/spec/products/" + f.pid.String() + "/milestones/" + f.mid.String() +
		"/tasks/" + f.tid.String()

	for _, query := range []string{"", "?tab=notes", "?tab=dependencies", "?tab=slice"} {
		t.Run("detail"+query, func(t *testing.T) {
			body := fetch(t, f.mux, detail+query).Body.String()
			for _, marker := range []string{`alert-error`, `role="alert"`} {
				assert.NotContains(t, body, marker,
					"the legacy detail URL at %s renders %q: a fixture read this file can no longer see fail", query, marker)
			}
			// And the walk is not vacuous here either: each address
			// really renders the panel it names.
			assert.Contains(t, body, `data-krill="task-tabs"`,
				"every tab of the legacy detail URL is a real page carrying the strip")
		})
	}
}

// TestTaskDetailStripHrefsAreSortableAndStable pins that the strip's four
// addresses are exactly four distinct real addresses -- the shape a shared
// link, a bookmark and a Back/forward step all depend on. A duplicate
// would make two history entries render the same page; a missing one
// would make a facet unreachable without JS.
func TestTaskDetailStripHrefsAreDistinctAndAbsolute(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})
	_, frag := f.getTabAt(task.ID.String(), "")

	var hrefs []string
	for _, tab := range elementsWithHook(t, frag, "task-tab") {
		href, _ := attrOfNode(tab, "href")
		hrefs = append(hrefs, href)
	}
	sorted := append([]string(nil), hrefs...)
	sort.Strings(sorted)

	assert.Len(t, hrefs, 4)
	for i := 1; i < len(sorted); i++ {
		assert.NotEqual(t, sorted[i-1], sorted[i],
			"two tabs share an address, so the URL cannot tell them apart and one facet is unreachable")
	}
	for _, href := range hrefs {
		assert.True(t, strings.HasPrefix(href, "/"),
			"a tab href %q is not a path on this origin", href)
		assert.False(t, strings.HasPrefix(href, "//"),
			"a tab href %q is protocol-relative, which is an off-origin address", href)
	}
}

// TestTaskDetailAFailedReadAlertsInItsPanelAndTheRestOfThePageStillRenders
// is the FR's failure clause end to end, at the level the clause is
// written at.
//
// "Only that tab shows an inline alert and the rest of the page renders"
// has two halves, and it is easy to test only the first. The alert has to
// be INSIDE the panel, and -- the half that is easy to lose -- the header,
// the lane steps, the rail and the other facets still have to be there.
// A handler that served an alert page instead of a tabbed one satisfies
// the first half and loses the page.
//
// The failure is driven on BOTH list reads at once, because a degraded
// detail is the case where a second read is likely to fail too, and a
// test that only ever fails one at a time cannot tell "each panel carries
// its own error" from "the page carries one error".
func TestTaskDetailAFailedReadAlertsInItsPanelAndTheRestOfThePageStillRenders(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{
		Title:        "degraded",
		CurrentLane:  store.LaneTesting,
		LaneSequence: []store.Lane{store.LaneImplementation, store.LaneTesting},
	})
	f.store.depsErr = assertErr{}
	f.store.notesErr = assertErr{}

	// The alert lands in the panel that owns the read, and in no other.
	for _, tc := range []struct{ tab, host, empty, alert string }{
		{pages.TaskTabNotes, "task-panel-notes", "No notes", "The notes could not be read"},
		{pages.TaskTabDependencies, "task-panel-dependencies", "No dependencies", "The dependencies could not be read"},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			code, frag := f.getTabAt(task.ID.String(), "?tab="+tc.tab)
			require.Equal(t, 200, code,
				"every path answers 200 and re-derives from observed state; a failed read is not a failed page")

			// Scoped to the panel that owns the read, so "the alert is in
			// the notes panel" is not satisfied by an alert anywhere.
			panels := elementsWithHook(t, frag, tc.host)
			require.Len(t, panels, 1, "the tab must render its own panel host")
			assert.Contains(t, renderedText(panels[0]), tc.alert,
				"a failed read alerts inside the panel that owns it")
			assert.NotContains(t, renderedText(panels[0]), tc.empty,
				"a failed read must never render as an empty list: \"No notes\" is indistinguishable from success")

			// And nowhere else in the response.
			assert.Equal(t, 1, strings.Count(frag, tc.alert),
				"the alert appears exactly once: a second copy is the same failure rendered somewhere the operator is not looking")
			// The raw error never reaches the page.
			assert.NotContains(t, frag, "read failed",
				"the read's own error text is a log detail, not operator-facing copy")
		})
	}

	// The rest of the page, on a request for the failing tab: the
	// breadcrumb, title, lane steps and rail are outside the swap region,
	// so a panel swap cannot take them -- and a FULL request has to carry
	// them too, or the no-JS path loses the page's frame.
	code, page := f.getFullAt(task.ID.String(), "?tab="+pages.TaskTabNotes)
	require.Equal(t, 200, code, "body: %s", page)
	assert.Contains(t, page, "<html", "a full request is a document, not a fragment")
	for _, region := range []string{"task-breadcrumb", "page-title", "task-lane-steps", "task-properties-rail", "task-tabs"} {
		assert.NotEmpty(t, elementsWithHook(t, page, region),
			"a failed read must cost the page its %s, not just its list", region)
	}
	assert.NotEmpty(t, elementsWithHook(t, page, "task-panel-notes"),
		"the failing tab's panel still renders on the full page")
}

// renderedText is an element's text content, which is what an operator
// reads and therefore what an assertion about "shows an alert" is about.
func renderedText(n *html.Node) string { return textOf(n) }
