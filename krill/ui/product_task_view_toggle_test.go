// Coverage for the List/Board toggle (FR ab5f4936): that switching view
// keeps the scope and the filters, that the current view is marked on each
// page, and that the sidebar's two items reach the product-wide pages
// rather than the delivery page.
//
// The toggle is read out of the rendered markup by walking the anchor
// carrying its own marker, and each destination is then REQUESTED and
// checked for the scope and filters the operator was looking at. Asserting
// on the href string alone would pass for a link whose destination
// resolved to something else -- and a toggle whose href drops a filter is
// exactly the failure this exists to prevent.
package main

import (
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// viewToggleDestinations reads the toggle's own links out of a rendered
// page: each link's label, href, and whether it is marked the current
// view.
//
// The href is unescaped, because templ HTML-escapes an attribute value
// and a query carrying more than one parameter arrives as "a&amp;b" --
// handing that to url.Parse would read a parameter literally named
// "amp;b", and the destination would look wrong for a reason that has
// nothing to do with the toggle.
func viewToggleDestinations(t *testing.T, body string) []viewToggleDestination {
	t.Helper()
	start := strings.Index(body, `data-krill="view-toggle"`)
	require.NotEqual(t, -1, start, "the page rendered no List/Board toggle: %s", body)
	// Only the toggle's own subtree: a page carries many anchors, and the
	// sidebar's Tasks and Board links are not the toggle's.
	rest := body[start:]

	var out []viewToggleDestination
	for {
		open := strings.Index(rest, "<a ")
		if open == -1 {
			return out
		}
		rest = rest[open:]
		close := strings.Index(rest, "</a>")
		require.NotEqual(t, -1, close, "the toggle link never closes")
		link := rest[:close]
		rest = rest[close+len("</a>"):]

		if !strings.Contains(link, `data-krill="view-toggle-link"`) {
			continue
		}
		out = append(out, viewToggleDestination{
			Label:  toggleAttrOf(link, "data-krill-view"),
			Href:   html.UnescapeString(toggleAttrOf(link, "href")),
			Active: strings.Contains(link, "aria-current="),
		})
	}
}

type viewToggleDestination struct {
	Label  string
	Href   string
	Active bool
}

// toggleAttrOf is one attribute's value out of a link's opening tag.
func toggleAttrOf(link, name string) string {
	marker := name + `="`
	at := strings.Index(link, marker)
	if at == -1 {
		return ""
	}
	rest := link[at+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

// viewToggleLinkOf is one destination by label, so a case can assert
// about "the Board link" without depending on render order.
func viewToggleLinkOf(t *testing.T, body, label string) viewToggleDestination {
	t.Helper()
	for _, d := range viewToggleDestinations(t, body) {
		if d.Label == label {
			return d
		}
	}
	t.Fatalf("the toggle has no %q link: %s", label, body)
	return viewToggleDestination{}
}

// TestViewToggleCarriesTheWholeQuery is FR ab5f4936's core claim: the
// operator lands on the other view at the SAME scope and the SAME lane
// and only-stuck filters.
//
// It starts from a deep-linked Board URL, because that is the harder
// direction -- a Board URL is one an operator arrives at by following a
// link, so every parameter on it was chosen deliberately. Each destination
// is then REQUESTED and its resolved scope read back, so a toggle that
// spelled the right-looking href but produced a page scoped to something
// else would fail here rather than pass on a string comparison.
func TestViewToggleCarriesTheWholeQuery(t *testing.T) {
	const lane = "Testing"
	query := "scope=milestone&container_id=" + productTaskMilestone.String() +
		"&milestone=" + productTaskMilestone.String() +
		"&lane=" + lane + "&only_stuck=true&page_size=5"

	for _, from := range []struct {
		view string
		url  string
	}{
		{view: productBoardView, url: "/products/" + productTaskProduct.String() + "/board?" + query},
		{view: productTasksView, url: productTaskTasksURL(query)},
	} {
		t.Run("from "+from.view, func(t *testing.T) {
			mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

			rec := fetch(t, mux, from.url)
			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			body := rec.Body.String()

			other := productTasksView
			if from.view == productTasksView {
				other = productBoardView
			}

			link := viewToggleLinkOf(t, body, viewToggleLabels[other])

			// The destination is a URL on the OTHER view's own path. A
			// toggle that left the operator on the view they were already
			// on would satisfy every other assertion here.
			wantPath := "/products/" + productTaskProduct.String() +
				map[string]string{productTasksView: tasksSuffix, productBoardView: boardSuffix}[other]
			assert.Equal(t, wantPath, link.Href[:strings.Index(link.Href, "?")],
				"the %s link must address the %s view's own route", other, other)
			assert.False(t, link.Active,
				"the link to the OTHER view cannot also be the one marked as the current view")

			// And it carries the query across whole. Compared as parsed
			// values rather than as a string, so a different parameter
			// order cannot fail a case that is actually correct.
			got, err := url.Parse(link.Href)
			require.NoError(t, err)
			want, err := url.Parse(from.url)
			require.NoError(t, err)
			assert.Equal(t, want.Query(), got.Query(),
				"the destination must ask the same question of the other view")

			// Finally: request it, and read the scope back off the page
			// that answers. This is what makes the href assertion above
			// more than a string comparison.
			rec = fetch(t, mux, link.Href)
			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			dest := rec.Body.String()

			// The scope survived, read off the destination's own control.
			assert.Equal(t, productTaskMilestone.String(),
				selectedOption(t, dest, `data-krill="scope-milestone-select"`),
				"the destination is scoped to the milestone the operator was reading")

			// The filters survived too -- but not in the same SHAPE on
			// both views, and the difference is the Board half of
			// TestScopeControlCarriesTheSiblingFilters: the Tasks view
			// offers the lane select and the only-stuck checkbox, the
			// Board carries both as hidden fields. So each direction is
			// checked the way its destination actually carries the
			// filter, rather than by demanding a control the Board
			// deliberately does not have.
			form := scopeControlFormOfFrom(t, mux, link.Href)
			if other == productTasksView {
				assert.Equal(t, lane, selectedOption(t, form, `data-krill="lane-filter-select"`),
					"the lane filter survived the view switch")
				assert.Contains(t, checkedBoxOf(form), "checked",
					"the only-stuck filter survived the view switch")
			} else {
				assert.Contains(t, form, `name="lane" value="`+lane+`"`,
					"the Board carries the lane forward as a hidden field")
				assert.Contains(t, form, `name="only_stuck" value="true"`,
					"the Board carries only-stuck forward as a hidden field")
			}
		})
	}
}

// viewToggleLabels are the two views' toggle labels. They are spelled out
// rather than reused from the view constants because they are NOT the same
// words: the Tasks view's link reads "List", as the wireframes have it,
// while the view is named "Tasks" everywhere else. A case that used the
// view constant to look the link up would be testing a label the toggle
// never renders.
var viewToggleLabels = map[string]string{
	productTasksView: "List",
	productBoardView: "Board",
}

// TestViewToggleMarksTheCurrentViewOnEachPage is the other half of the
// FR: the operator can tell which of the two views they are on.
//
// Read from the markup rather than from the builder, and it holds for
// BOTH pages, because a toggle that marked the current view on one of them
// is a toggle whose marking means nothing.
func TestViewToggleMarksTheCurrentViewOnEachPage(t *testing.T) {
	query := "scope=milestone&container_id=" + productTaskMilestone.String() + "&lane=Testing"

	for _, tc := range []struct {
		view string
		url  string
	}{
		{view: productTasksView, url: productTaskTasksURL(query)},
		{view: productBoardView, url: "/products/" + productTaskProduct.String() + "/board?" + query},
	} {
		t.Run(tc.view, func(t *testing.T) {
			mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)
			body := fetch(t, mux, tc.url).Body.String()

			links := viewToggleDestinations(t, body)
			require.Len(t, links, 2, "the toggle offers exactly two views")

			var active []string
			for _, l := range links {
				if l.Active {
					active = append(active, l.Label)
				}
			}
			// Exactly one, and it is this page's own view. Marking none
			// leaves the operator unable to tell where they are; marking
			// both is a toggle that marks nothing.
			assert.Equal(t, []string{viewToggleLabels[tc.view]}, active,
				"exactly the view being served is marked active")

			// The marked one is also the link back to THIS page, so it is
			// the current view's own URL -- asserted so a toggle that
			// marked the wrong link cannot pass by marking one.
			marked := viewToggleLinkOf(t, body, viewToggleLabels[tc.view])
			assert.True(t, strings.HasPrefix(marked.Href, tc.url[:strings.Index(tc.url, "?")]),
				"the marked link addresses the page being served: %q", marked.Href)
		})
	}
}

// TestViewToggleOnAnUnfilteredURLCarriesNoQuery: a bare URL has nothing
// to carry, so the toggle's destinations are bare paths. An empty "?" on
// the end of a link is a URL no route serves cleanly, and it is what a
// toggle that appended a query unconditionally would produce.
func TestViewToggleOnAnUnfilteredURLCarriesNoQuery(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 1}, productTaskListing(), nil)
	body := fetch(t, mux, productTaskTasksURL("")).Body.String()

	for _, l := range viewToggleDestinations(t, body) {
		assert.NotContains(t, l.Href, "?", "an unfiltered URL has no query to carry: %q", l.Href)
	}
}

// TestViewToggleDoesNotCarryThePageToken is the one parameter that must
// NOT survive the switch, and the reason is worth pinning because the
// tempting implementation -- carry the whole query -- gets it wrong.
//
// A continuation token is a position in the keyset-paged read: it names
// "resume after the row whose id is this". The two views do not page the
// same way -- the Board's own contract is to show every task in scope, or
// to state "Showing X of Y" -- so a token issued for the Tasks table's
// third page lands the operator on a board showing an arbitrary third of
// the work with nothing on the page saying so.
func TestViewToggleDoesNotCarryThePageToken(t *testing.T) {
	const pageSize = 2
	mux := pagedTaskMux(t, &pagedProductTasks{rows: pagedRowFixture(6)})

	// Walk to a page beyond the first, so the token is a real one and
	// not something a bare URL could have spelled.
	first := fetch(t, mux, productTaskTasksURL("page_size="+strconv.Itoa(pageSize))).Body.String()
	next := pagingLinkHref(first, `data-krill="paging-next"`)
	require.Contains(t, next, "page_token=", "the fixture must page for this case to mean anything")

	tasks := fetch(t, mux, next).Body.String()
	list := viewToggleLinkOf(t, tasks, "List")
	require.NotContains(t, list.Href, "page_token=",
		"this is the current view; the marked link carries no token")

	board := viewToggleLinkOf(t, tasks, "Board")
	assert.NotContains(t, board.Href, "page_token=",
		"a page position in the paged list means nothing on the board of swimlanes")

	// Everything else about the paging request survives.
	assert.Contains(t, board.Href, "page_size="+strconv.Itoa(pageSize),
		"the page SIZE is a filter and does survive: %q", board.Href)
}

// TestViewToggleIsOnBothViewsAndInBothFragments: the toggle lives inside
// the region, so an htmx swap brings the marking with it. A toggle
// rendered outside the swapped region would keep pointing at the scope the
// operator has just changed away from, which is the drift the shared scope
// control was built to prevent -- and it would happen silently, because
// the swap succeeded.
func TestViewToggleIsOnBothViewsAndInBothFragments(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 2}, productTaskListing(), nil)

	for _, target := range []string{
		productTaskTasksURL("scope=milestone&container_id=" + productTaskMilestone.String()),
		"/products/" + productTaskProduct.String() + "/board?scope=milestone&container_id=" + productTaskMilestone.String(),
	} {
		t.Run(target, func(t *testing.T) {
			rec := htmxGet(mux, target)
			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			body := rec.Body.String()

			require.Contains(t, body, `data-krill="view-toggle"`,
				"the fragment a swap brings back carries no toggle: %s", body)
			// The fragment is the region, so it also keeps the control the
			// toggle sits beside -- one assertion that the two travel
			// together rather than one being left behind by the swap.
			assert.Contains(t, body, `data-krill="scope-control"`)
		})
	}
}

// TestViewToggleFollowsAScopeChangeOnTheSwapPath is the case the toggle
// living inside the region exists for. The scope form swaps the region in
// place; the toggle is part of what comes back, so after a scope change
// its destinations address the scope just chosen rather than the one the
// page was opened on.
//
// Read through the htmx path on purpose: a toggle rendered once in the
// shell chrome would render here too and still be stale, so this asserts
// the fragment's own hrefs.
func TestViewToggleFollowsAScopeChangeOnTheSwapPath(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 2}, productTaskListing(), nil)

	rec := htmxGet(mux, productTaskTasksURL(
		"scope=milestone&container_id="+productTaskOldestMilestone.String()))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Contains(t, rec.Body.String(), productTaskOldestMilestone.String())

	// Now change the scope the way the operator would, and read the
	// toggle back off the fragment that comes back.
	swapped := htmxGet(mux, productTaskTasksURL(
		"scope=milestone&container_id="+productTaskNewestMilestone.String()))
	require.Equal(t, http.StatusOK, swapped.Code, "body: %s", swapped.Body.String())
	body := swapped.Body.String()

	board := viewToggleLinkOf(t, body, "Board")
	assert.Contains(t, board.Href, productTaskNewestMilestone.String(),
		"after a scope change the toggle addresses the scope just chosen: %q", board.Href)
	assert.NotContains(t, board.Href, productTaskOldestMilestone.String(),
		"the toggle must not still be pointing at the scope the page was opened on")
}

// TestSidebarTasksAndBoardReachTheProductWidePages is item 3 of the task:
// the sidebar stops falling back to the delivery page.
//
// Asserted through the served markup rather than against the nav table,
// so what it checks is the link an operator is actually offered -- and it
// requests each href, because a sidebar item pointing at a route nobody
// mounts is the failure this change was most likely to introduce.
func TestSidebarTasksAndBoardReachTheProductWidePages(t *testing.T) {
	mux := navMux(t)

	for _, tc := range []struct {
		label string
		want  string
	}{
		{label: "Tasks", want: "/products/" + navProductID.String() + "/tasks"},
		{label: "Board", want: "/products/" + navProductID.String() + "/board"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			body := fetch(t, mux, "/").Body.String()
			item := navItemByLabel(t, workspaceNav(navTargets{Product: navProductID}, "/"), tc.label)
			assert.Equal(t, tc.want, item.Href,
				"%s must link at the product-wide page, not the delivery page", tc.label)
			assert.Contains(t, body, `href="`+tc.want+`"`,
				"the rendered sidebar does not carry the %s href", tc.label)

			rec := fetch(t, mux, tc.want)
			assert.Equal(t, http.StatusOK, rec.Code,
				"the sidebar's %s link is a dead link", tc.label)
		})
	}
}

// TestSidebarMarksTheProductScopedPathActive is the acceptance clause
// that the sidebar marks Tasks or Board active on the corresponding
// product-scoped path. Driven through the real routes, so it is the
// marking a served page carries.
func TestSidebarMarksTheProductScopedPathActive(t *testing.T) {
	mux := navMux(t)

	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/products/" + navProductID.String() + "/tasks", want: "Tasks"},
		{path: "/products/" + navProductID.String() + "/board", want: "Board"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			rec := fetch(t, mux, tc.path)
			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

			marked := activeLabels(rec.Body.String())
			require.Len(t, marked, 1, "expected exactly one active nav item, got %v", marked)
			assert.Equal(t, tc.want, marked[0])
		})
	}
}

// TestSidebarTasksAndBoardAreTwoDistinctLinks guards the shape the
// previous contract got wrong: with no container in scope, both items used
// to link at the delivery page. Two items pointing at one URL is not a
// toggle, and an operator clicking "Board" and arriving somewhere that is
// not the board has been misled.
func TestSidebarTasksAndBoardAreTwoDistinctLinks(t *testing.T) {
	hrefs := sidebarHrefsByLabel(workspaceNav(navTargets{Product: navProductID}, "/"))

	assert.NotEqual(t, hrefs["Tasks"], hrefs["Board"],
		"the two views have two URLs")
	for _, label := range []string{"Tasks", "Board"} {
		assert.NotContains(t, hrefs[label], "/delivery",
			"%s still falls back to the delivery page", label)
		assert.NotContains(t, hrefs[label], "/milestones/",
			"%s is still scoped to a milestone", label)
		assert.NotContains(t, hrefs[label], uuid.Nil.String(),
			"%s href carries uuid.Nil", label)
	}
}

// scopeControlFormOfFrom is the existing scopeControlFormOf helper against
// a caller-supplied mux, so a case that already built its own fixture does
// not build a second one with different data behind it.
func scopeControlFormOfFrom(t *testing.T, mux *http.ServeMux, target string) string {
	t.Helper()
	rec := fetch(t, mux, target)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	at := strings.Index(body, `data-krill="scope-control"`)
	require.NotEqual(t, -1, at, "no scope control rendered: %s", body)
	rest := body[at:]
	end := strings.Index(rest, "</form>")
	require.NotEqual(t, -1, end, "the scope control never closes: %s", body)
	return rest[:end]
}
