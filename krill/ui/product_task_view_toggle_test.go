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

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
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

// TestViewToggleCarriesAMilepebbleScope is the per-mode case the other
// cases only covered structurally: milepebble mode names TWO containers --
// the parent milestone whose milepebbles are on offer, and the chosen
// milepebble itself -- and the parent is the parameter a re-derivation is
// most likely to drop.
//
// The default-mode and milestone-mode destinations are a single container
// either way, so a toggle that carried the read's own scope would pass
// every other case here while losing the `milestone` parameter and leaving
// the destination's second select empty. Asserted by requesting the
// destination and reading the scope back off the control it renders, which
// is also what proves the destination resolved rather than merely spelled
// the right-looking href.
func TestViewToggleCarriesAMilepebbleScope(t *testing.T) {
	const lane = "Validation"
	query := "scope=milepebble&milestone=" + productTaskMilestone.String() +
		"&container_id=" + productTaskMilepebble.String() +
		"&lane=" + lane + "&only_stuck=true&page_size=4"

	for _, from := range []struct {
		view string
		url  string
	}{
		{view: productTasksView, url: productTaskTasksURL(query)},
		{view: productBoardView, url: "/products/" + productTaskProduct.String() + "/board?" + query},
	} {
		t.Run("from "+from.view, func(t *testing.T) {
			tasks := &recordingProductTasks{total: 2}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			body := fetch(t, mux, from.url).Body.String()
			other := productTasksView
			if from.view == productTasksView {
				other = productBoardView
			}

			link := viewToggleLinkOf(t, body, viewToggleLabels[other])
			want, err := url.Parse(from.url)
			require.NoError(t, err)
			got, err := url.Parse(link.Href)
			require.NoError(t, err)
			assert.Equal(t, want.Query(), got.Query(),
				"milepebble mode names a parent milestone as well as a container; both travel")

			// Request it: the destination's own control has to show the same
			// pair, which is what "the parent survived" means from where the
			// operator is standing.
			dest := fetch(t, mux, link.Href)
			require.Equal(t, http.StatusOK, dest.Code, "body: %s", dest.Body.String())
			assert.Equal(t, productTaskMilestone.String(),
				selectedOption(t, dest.Body.String(), `data-krill="scope-milestone-select"`),
				"the parent milestone is still the one the second select's options come from")
			assert.Equal(t, productTaskMilepebble.String(),
				selectedOption(t, dest.Body.String(), `data-krill="scope-milepebble-select"`),
				"the milepebble is still the one chosen")

			// And the read behind it was scoped to that milepebble, not to
			// the parent: the two parameters have different meanings and a
			// toggle that swapped them would still render a plausible page.
			require.Len(t, tasks.listed, 2)
			assert.Equal(t, store.ProductTaskScope{
				Kind:        store.ProductTaskScopeMilepebble,
				ContainerID: productTaskMilepebble,
			}, tasks.listed[1].Scope)
		})
	}
}

// TestViewToggleIsOnTheEmptyOutcomesThatStillCarryTheControl: the two
// scope problems that are ORDINARY ANSWERS rather than refusals -- a
// product with no containers at all, and milepebble mode over a milestone
// with nothing cut -- still render the region, and with it the toggle.
//
// They matter because the operator who lands on one of them has reached a
// page that resolved to nothing and needs to get somewhere else. A toggle
// that lived beside the region body would be missing from exactly these
// two renderings if either of them built the region by a different route,
// and nothing on the page would say so.
func TestViewToggleIsOnTheEmptyOutcomesThatStillCarryTheControl(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		// listing is the delivery listing the scope resolver reads; an
		// empty one is what makes every mode an ordinary empty result.
		listing func() slice.DeliveryListing
	}{
		{
			name:    "a product with no containers to scope to",
			query:   "scope=milestone",
			listing: emptyDeliveryListing,
		},
		{
			name:  "milepebble mode over a milestone with nothing cut",
			query: "scope=milepebble&milestone=" + productTaskOldestMilestone.String() + "&lane=Testing",
			listing: func() slice.DeliveryListing {
				return productTaskListing()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, view := range []string{productTasksView, productBoardView} {
				t.Run(view, func(t *testing.T) {
					target := productTaskTasksURL(tc.query)
					if view == productBoardView {
						target = "/products/" + productTaskProduct.String() + "/board?" + tc.query
					}
					mux := productTaskMux(t, &recordingProductTasks{}, tc.listing(), nil)

					body := fetch(t, mux, target).Body.String()
					require.Contains(t, body, `data-krill="scope-control"`,
						"these outcomes keep the control: %s", body)

					// Both links render, the current view is marked, and the
					// sibling's href carries the query this page was opened
					// with -- so the operator can leave without losing what
					// they had asked for.
					links := viewToggleDestinations(t, body)
					require.Len(t, links, 2, "the toggle is offered on this outcome too")

					var active []string
					for _, l := range links {
						if l.Active {
							active = append(active, l.Label)
						}
					}
					assert.Equal(t, []string{viewToggleLabels[view]}, active,
						"exactly the view being served is marked")

					sibling := viewToggleLinkOf(t, body, viewToggleLabels[viewToggleSiblingOf(view)])
					want, err := url.Parse(target)
					require.NoError(t, err)
					got, err := url.Parse(sibling.Href)
					require.NoError(t, err)
					assert.Equal(t, want.Query(), got.Query(),
						"the sibling link carries the query this page was opened with")
				})
			}
		})
	}
}

// TestViewToggleIsAbsentOnlyWhereTheScopeControlIs: the boundary of the
// previous case. A container this product does not own is a REFUSAL, not
// an empty answer, and the refusal replaces the whole region -- scope
// control and toggle together -- with an in-shell status page.
//
// Pinning the absence is what stops the toggle being rendered somewhere
// else on the way: the toggle lives in the scope control, so "the control
// is gone" and "the toggle is gone" must be the same statement. An operator
// arriving by a stale link has the sidebar's Tasks and Board to recover
// with, which the next case confirms.
func TestViewToggleIsAbsentOnlyWhereTheScopeControlIs(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 1}, productTaskListing(), nil)

	for _, view := range []string{productTasksView, productBoardView} {
		t.Run(view, func(t *testing.T) {
			target := productTaskTasksURL("scope=milestone&container_id=" + productTaskOtherMilestone.String())
			if view == productBoardView {
				target = "/products/" + productTaskProduct.String() + "/board?scope=milestone&container_id=" +
					productTaskOtherMilestone.String()
			}

			rec := fetch(t, mux, target)
			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.NotContains(t, rec.Body.String(), `data-krill="view-toggle"`,
				"the refusal replaced the region, so the toggle goes with it")
			assert.NotContains(t, rec.Body.String(), `data-krill="scope-control"`)

			// The operator is not stranded: the sidebar still offers both
			// views, product-wide, from a page that has no container in its
			// own URL at all.
			hrefs := navHrefsByLabelInBody(rec.Body.String())
			assert.Equal(t, "/products/"+productTaskProduct.String()+"/tasks", hrefs["Tasks"])
			assert.Equal(t, "/products/"+productTaskProduct.String()+"/board", hrefs["Board"])
		})
	}
}

// TestViewToggleDropsAStalePageTokenFromTheBoardURLToo: the token is
// dropped whichever view the operator is on when they press it.
//
// The table is where a real token is ever issued, but a shared link or a
// hand-edited URL can carry one onto the Board, and the Board's own read
// does not page the way the table's does. A toggle that dropped the token
// on one leg and not the other would send the operator to a list showing a
// page of a board's scope with nothing saying so.
func TestViewToggleDropsAStalePageTokenFromTheBoardURLToo(t *testing.T) {
	// The paging fixture, because it REFUSES a token that is not one --
	// which is what puts the operator on a Board page carrying a stale
	// token in the first place, and what makes the page they are on the
	// page-error path the toggle has to survive.
	mux := pagedTaskMux(t, &pagedProductTasks{rows: pagedRowFixture(6)})

	body := fetch(t, mux, "/products/"+productTaskProduct.String()+
		"/board?scope=milestone&container_id="+productTaskMilestone.String()+
		"&page_size=2&page_token=not-a-real-token").Body.String()

	list := viewToggleLinkOf(t, body, "List")
	assert.NotContains(t, list.Href, "page_token=",
		"a stale token on the Board's own URL does not travel to the list: %q", list.Href)
	assert.Contains(t, list.Href, "page_size=2",
		"the page SIZE is a filter and does survive: %q", list.Href)

	// The page itself is unaffected: the read refuses the token as itself
	// rather than as a failure, which is the page-error path the toggle
	// then renders on top of -- and the recovery link it offers drops the
	// token the same way the toggle does.
	assert.Contains(t, body, `data-krill="product-tasks-page-error"`,
		"the stale token is answered as the stale link it is")
	assert.NotContains(t, pagingLinkHref(body, `data-krill="product-tasks-page-recovery"`), "page_token=",
		"the way out of a refused token is the first page, not the same bad token")
}

// TestViewToggleIsNotInsideTheScopeForm: the toggle is a SIBLING of the
// form, not a control in it.
//
// This is not cosmetic. Every assertion about the form elsewhere reads it
// by cutting the page from the scope control's marker to the first
// </form> -- so a toggle rendered inside the form would be swept into
// "the scope control" by TestScopeControlCarriesTheSiblingFilters and by
// the replay in TestScopeControlURLCarriesTheWholeScope, and the two
// components would have to agree about being one thing. They are two: a
// form is the scope's own value, and switching view is navigation.
func TestViewToggleIsNotInsideTheScopeForm(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)
	body := fetch(t, mux, productTaskTasksURL("scope=milestone&container_id="+
		productTaskMilestone.String())).Body.String()

	toggle := strings.Index(body, `data-krill="view-toggle"`)
	form := strings.Index(body, `data-krill="scope-control"`)
	require.NotEqual(t, -1, toggle, "no toggle rendered: %s", body)
	require.NotEqual(t, -1, form, "no scope control rendered: %s", body)
	assert.Less(t, toggle, form,
		"the toggle renders before the form opens, so cutting the form cannot capture it")

	formEnd := strings.Index(body[form:], "</form>")
	require.NotEqual(t, -1, formEnd)
	assert.NotContains(t, body[form:form+formEnd], `data-krill="view-toggle"`,
		"the toggle is outside the form's own markup")
}

// TestSidebarTasksAndBoardHrefsDoNotDependOnThePageContainer is the guard
// on the field this task removed: navTargets no longer carries a milestone
// id, so the sidebar an operator sees is the SAME sidebar whatever
// container the page they are on happens to be scoped to.
//
// Before, the Tasks and Board hrefs were rebuilt per request out of a
// milestone id the chrome could only sometimes be given -- which is why a
// page reached without a container in its URL sent them to the delivery
// page instead. Driven through two real pages of different scopes rather
// than against the nav table, so it is the link an operator is actually
// offered that is compared.
func TestSidebarTasksAndBoardHrefsDoNotDependOnThePageContainer(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{total: 3}, productTaskListing(), nil)

	bare := navHrefsByLabelInBody(fetch(t, mux, productTaskTasksURL("")).Body.String())
	scoped := navHrefsByLabelInBody(fetch(t, mux, productTaskTasksURL(
		"scope=milestone&container_id="+productTaskMilestone.String())).Body.String())
	board := navHrefsByLabelInBody(fetch(t, mux, "/products/"+productTaskProduct.String()+
		"/board?scope=milepebble&milestone="+productTaskMilestone.String()+
		"&container_id="+productTaskMilepebble.String()).Body.String())

	for _, label := range []string{"Tasks", "Board"} {
		require.NotEmpty(t, bare[label], "the sidebar offers %s", label)
		assert.Equal(t, bare[label], scoped[label],
			"%s href differs between an unscoped page and a milestone-scoped one", label)
		assert.Equal(t, bare[label], board[label],
			"%s href differs between the two views", label)
		assert.NotContains(t, bare[label], "/delivery",
			"%s still falls back to the delivery page", label)
	}
}

// TestSidebarTasksOwnBothSpellingsOfTheTasksURL: the Tasks item marks
// active on the product-wide page AND on the pre-redesign per-milestone
// subtree, while linking at the product-wide one.
//
// The two-spelling ownership is what survives the legacy-URL cutover. The
// sibling task makes /milestones/{mid}/tasks 302 into the product-wide
// Tasks scoped to that milestone, so the landing page's own path is the
// one Path already owns and this item lights from Path; but the per-
// container task DETAIL keeps serving at the legacy URL, and an operator
// reading one of those must still see the sidebar say Tasks. Asserting both
// here means the merge that redirects the list cannot quietly leave the
// detail page's sidebar unlit, and cannot light Tasks on a page that is
// neither.
func TestSidebarTasksOwnBothSpellingsOfTheTasksURL(t *testing.T) {
	pid := navProductID

	// Both spellings light the same single item.
	for _, path := range []string{
		productHref(pid, tasksSuffix),
		milestoneTasksPath(pid, navMilestoneID),
		taskDetailPath(pid, navMilestoneID, uuid.New()),
	} {
		assert.Equal(t, []string{"Tasks"}, activeLabelsAtPath(pid, path),
			"%s is a Tasks page and must say so", path)
	}

	// The href is the product-wide one either way, and the Board item owns
	// the board subtree rather than the tasks one: a wildcard pattern that
	// matched too much would light both items, and workspaceNav lights only
	// the first match in render order -- so a Board subtree the Tasks
	// pattern swallowed would show Tasks on a Board.
	hrefs := sidebarHrefsByLabel(workspaceNav(navTargets{Product: pid}, "/"))
	assert.Equal(t, productHref(pid, tasksSuffix), hrefs["Tasks"])
	assert.Equal(t, productHref(pid, boardSuffix), hrefs["Board"])

	assert.Equal(t, []string{"Board"},
		activeLabelsAtPath(pid, milestoneBoardPath(pid, navMilestoneID)),
		"the per-milestone board subtree belongs to Board, not to Tasks")
}

// activeLabelsAtPath is the sidebar's marked items when it is rendered for
// one page's path -- the marking a served page carries, read off the nav
// table rather than the markup so a case can name a path no route in this
// file mounts.
func activeLabelsAtPath(pid uuid.UUID, path string) []string {
	return activeGroupLabels(workspaceNav(navTargets{Product: pid}, path))
}

// viewToggleSiblingOf is the other view's name -- the one a toggle link
// on this view points at.
func viewToggleSiblingOf(view string) string {
	if view == productTasksView {
		return productBoardView
	}
	return productTasksView
}

// navHrefsByLabelInBody reads the sidebar's rendered hrefs out of a served
// page, keyed by the link's own label, so a case can compare what two
// different pages OFFER rather than what the nav table would build for
// them.
func navHrefsByLabelInBody(body string) map[string]string {
	out := map[string]string{}
	primary := primaryNavRegion(body)
	if primary == "" {
		return out
	}
	for _, tag := range anchorTags(primary) {
		label := strings.TrimSpace(tag[strings.Index(tag, ">")+1:])
		at := strings.Index(tag, `href="`)
		if at == -1 {
			continue
		}
		rest := tag[at+len(`href="`):]
		out[html.UnescapeString(label)] = html.UnescapeString(rest[:strings.Index(rest, `"`)])
	}
	return out
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
