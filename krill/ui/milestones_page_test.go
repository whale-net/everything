// Coverage for the Milestones table (FR 5aec68f6): the view model the
// handler builds and the markup it renders, with no database.
//
// The expectations here are spelled as literals -- the eight statuses,
// the positions, the figures -- rather than derived from the code under
// test. A table that read its own rows to decide what order they should be
// in would shrink to match a shrinking implementation, which is the one
// bug this page could plausibly ship: the FR asks for highest position
// first, and a builder that simply passed the listing through would render
// the reverse of that while every other assertion still passed.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// milestonesProductID is the product every fixture below is scoped to. A
// literal, so a failure names the same ids every run and the expected
// paths in the tables can be written out and read.
var milestonesProductID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

// milestoneFixture is one milestone in a listing: the wire entry the
// delivery read produces, plus the figures the progress read returns for
// it.
type milestoneFixture struct {
	entry slice.MilestoneListingEntry
	done  int
	total int
}

// noProgress is the figure set for a milestone the progress read did not
// return a row for.
func milestoneFixtureOf(t *testing.T, name string, position int, status store.MilestoneStatus, done, total int) milestoneFixture {
	t.Helper()
	return milestoneFixture{
		entry: slice.MilestoneListingEntry{
			ID:       uuid.New(),
			Name:     name,
			Status:   status,
			Position: position,
		},
		done:  done,
		total: total,
	}
}

// listingOf is the delivery read's own shape for a set of fixtures: the
// entries in the order the read returns them, which is position ASCENDING.
// Handing that straight to the table is the mistake this page must not
// make, so the fixture builds it the way the real read does.
func listingOf(fixtures ...milestoneFixture) slice.DeliveryListing {
	ascending := make([]slice.MilestoneListingEntry, len(fixtures))
	for i, f := range fixtures {
		ascending[i] = f.entry
	}
	for i := 1; i < len(ascending); i++ {
		for j := i; j > 0 && ascending[j-1].Position > ascending[j].Position; j-- {
			ascending[j-1], ascending[j] = ascending[j], ascending[j-1]
		}
	}
	return slice.DeliveryListing{Milestones: ascending}
}

// progressOf is the progress read's answer for the same fixtures, indexed
// the way milestoneProgressByID indexes it.
func progressOf(fixtures ...milestoneFixture) map[uuid.UUID]store.ContainerTaskProgress {
	byID := make(map[uuid.UUID]store.ContainerTaskProgress, len(fixtures))
	for _, f := range fixtures {
		counts := store.TaskLaneCounts{}
		for i := 0; i < f.done; i++ {
			counts.Done++
		}
		for i := f.done; i < f.total; i++ {
			counts.Implementation++
		}
		byID[f.entry.ID] = store.ContainerTaskProgress{
			Milestone: store.ProductTaskMilestoneRef{
				ID:     f.entry.ID,
				Name:   f.entry.Name,
				Status: f.entry.Status,
			},
			PerLane: counts,
		}
	}
	return byID
}

// rowsOf runs the view-model builder over a listing and returns the rows
// it produced, in the order it produced them.
func rowsOf(t *testing.T, listing slice.DeliveryListing, progress map[uuid.UUID]store.ContainerTaskProgress, status store.MilestoneStatus) []pages.MilestoneRow {
	t.Helper()
	return milestoneRowsOf(milestonesProductID, listing, progress, status)
}

// rowNames is the rendered order, as the names an operator reads down the
// Milestone column.
func rowNames(rows []pages.MilestoneRow) []string {
	names := make([]string, len(rows))
	for i, r := range rows {
		names[i] = r.Name
	}
	return names
}

// ---------------------------------------------------------------------------
// 1. row order
// ---------------------------------------------------------------------------

// TestMilestonesRowsAreOrderedHighestPositionFirst is the FR's ordering
// rule stated directly: "highest position first (then id)".
//
// The listing is handed over in the order the delivery read really returns
// it -- position ASCENDING -- so a builder that passed the slice straight
// through would render exactly the reverse and pass every other assertion
// in this file.
func TestMilestonesRowsAreOrderedHighestPositionFirst(t *testing.T) {
	listing := listingOf(
		milestoneFixtureOf(t, "M1 oldest", 0, store.MilestoneStatusShipped, 3, 3),
		milestoneFixtureOf(t, "M2 middle", 1, store.MilestoneStatusInProgress, 1, 4),
		milestoneFixtureOf(t, "M3 newest", 2, store.MilestoneStatusInDesign, 0, 0),
	)

	rows := rowsOf(t, listing, progressOf(
		milestoneFixtureOf(t, "M1 oldest", 0, store.MilestoneStatusShipped, 3, 3),
		milestoneFixtureOf(t, "M2 middle", 1, store.MilestoneStatusInProgress, 1, 4),
		milestoneFixtureOf(t, "M3 newest", 2, store.MilestoneStatusInDesign, 0, 0),
	), "")

	assert.Equal(t, []string{"M3 newest", "M2 middle", "M1 oldest"}, rowNames(rows),
		"highest position first, whatever order the listing arrived in")
}

// TestMilestonesRowsTieBreakOnMilestoneID is the FR's "(then id)": two
// milestones sharing a position are ordered by id ascending.
//
// The two ids below are chosen so that id-ascending and id-descending
// disagree, so a builder that reversed the listing (rather than sorting)
// fails here rather than passing by luck on the position case alone.
func TestMilestonesRowsTieBreakOnMilestoneID(t *testing.T) {
	low := milestoneFixtureOf(t, "Tied low id", 7, store.MilestoneStatusPlanned, 0, 0)
	low.entry.ID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	high := milestoneFixtureOf(t, "Tied high id", 7, store.MilestoneStatusPlanned, 0, 0)
	high.entry.ID = uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")

	listing := listingOf(low, high)
	rows := rowsOf(t, listing, progressOf(), "")

	require.Len(t, rows, 2)
	assert.Equal(t, []string{"Tied low id", "Tied high id"}, rowNames(rows),
		"equal positions fall back to the milestone id, ascending")
}

// TestMilestonesRowOrderDoesNotMutateTheCallersListing is the side-effect
// guard: the builder sorts a filtered copy, because the delivery page may
// still be holding the same slice. Sorting it in place would reorder
// another page's rows under this one.
func TestMilestonesRowOrderDoesNotMutateTheCallersListing(t *testing.T) {
	listing := listingOf(
		milestoneFixtureOf(t, "M1 oldest", 0, store.MilestoneStatusShipped, 0, 0),
		milestoneFixtureOf(t, "M2 newest", 1, store.MilestoneStatusInProgress, 0, 0),
	)
	before := make([]string, 0, len(listing.Milestones))
	for _, m := range listing.Milestones {
		before = append(before, m.Name)
	}

	rowsOf(t, listing, progressOf(), "")

	after := make([]string, 0, len(listing.Milestones))
	for _, m := range listing.Milestones {
		after = append(after, m.Name)
	}
	assert.Equal(t, before, after, "the caller's listing slice must come back unchanged")
}

// ---------------------------------------------------------------------------
// 2. the progress column
// ---------------------------------------------------------------------------

// TestMilestoneRowCarriesTheProgressReadsOwnFigures is the FR's Progress
// column: the bar's "done/total" figures are the read's own Done() and
// Total(), and a milestone with no tasks says "No tasks yet" rather than
// "0 of 0".
//
// The figures are built by summing lanes into a store.ContainerTaskProgress
// and reading them back off that type, so a builder that re-derived the
// arithmetic itself -- summing, say, only the non-Done lanes -- would
// disagree with the read and fail here.
func TestMilestoneRowCarriesTheProgressReadsOwnFigures(t *testing.T) {
	shipped := milestoneFixtureOf(t, "Shipped", 2, store.MilestoneStatusShipped, 14, 14)
	inflight := milestoneFixtureOf(t, "In flight", 1, store.MilestoneStatusInProgress, 6, 9)
	empty := milestoneFixtureOf(t, "Unstarted", 0, store.MilestoneStatusNotStarted, 0, 0)

	rows := rowsOf(t, listingOf(shipped, inflight, empty), progressOf(shipped, inflight, empty), "")
	byName := map[string]pages.MilestoneRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	shippedRow := byName["Shipped"]
	assert.Equal(t, 14, shippedRow.Done)
	assert.Equal(t, 14, shippedRow.Total)
	assert.True(t, shippedRow.IsComplete(), "a shipped milestone's bar is full")
	assert.Equal(t, "14/14 tasks done", shippedRow.ProgressLabel())

	inflightRow := byName["In flight"]
	assert.Equal(t, 6, inflightRow.Done)
	assert.Equal(t, 9, inflightRow.Total)
	assert.False(t, inflightRow.IsComplete())
	assert.Equal(t, "6/9 tasks done", inflightRow.ProgressLabel())

	emptyRow := byName["Unstarted"]
	assert.Equal(t, 0, emptyRow.Total)
	assert.False(t, emptyRow.HasTasks())
	assert.Equal(t, "No tasks yet", emptyRow.ProgressLabel(),
		"a milestone with no tasks says so, never '0 of 0'")
	assert.Empty(t, emptyRow.ProgressError,
		"a genuinely empty milestone is an answer, not a failed read")
}

// TestMilestoneRowWithoutAProgressRowSaysSoRatherThanReadingAsEmpty is the
// distinction between "no tasks" and "not checked". If the progress read
// returns no row for a milestone, a row that rendered "No tasks yet" would
// be claiming an answer the read never gave.
func TestMilestoneRowWithoutAProgressRowSaysSoRatherThanReadingAsEmpty(t *testing.T) {
	orphan := milestoneFixtureOf(t, "Unaccounted for", 0, store.MilestoneStatusPlanned, 5, 5)

	rows := rowsOf(t, listingOf(orphan), map[uuid.UUID]store.ContainerTaskProgress{}, "")

	require.Len(t, rows, 1, "a milestone the read did not account for is still a row")
	assert.Equal(t, milestonesProgressError, rows[0].ProgressError,
		"the cell says the figures were not read")
	assert.Equal(t, 0, rows[0].Total, "and reports no figure rather than inventing one")
}

// ---------------------------------------------------------------------------
// 3. the status filter
// ---------------------------------------------------------------------------

// TestMilestonesStatusFilterKeepsOnlyMilestonesInThatStatus is the select's
// contract, and the reason the builder re-filters rather than trusting the
// read: list_product_delivery deliberately returns a milestone whose
// MILEPEBBLE matched the filter even when the milestone itself did not.
//
// Without the re-filter a "shipped" filter would show a row badged "in
// progress" -- the one obvious way the select could lie to an operator.
func TestMilestonesStatusFilterKeepsOnlyMilestonesInThatStatus(t *testing.T) {
	shipped := milestoneFixtureOf(t, "Shipped", 2, store.MilestoneStatusShipped, 3, 3)
	// The trap: this milestone does NOT match, but the read would return
	// it anyway because its milepebble does.
	inflight := milestoneFixtureOf(t, "In flight", 1, store.MilestoneStatusInProgress, 1, 2)
	inflight.entry.Milepebbles = []slice.MilepebbleListingEntry{{
		ID:     uuid.New(),
		Name:   "A shipped milepebble under an in-flight milestone",
		Status: store.MilestoneStatusShipped,
	}}

	listing := listingOf(shipped, inflight)
	rows := rowsOf(t, listing, progressOf(shipped, inflight), store.MilestoneStatusShipped)

	assert.Equal(t, []string{"Shipped"}, rowNames(rows),
		"a milepebble's match must not put its parent in a milestone-status filter")

	all := rowsOf(t, listing, progressOf(shipped, inflight), "")
	assert.Equal(t, []string{"Shipped", "In flight"}, rowNames(all),
		"an empty filter is every milestone")
}

// TestMilestoneStatusOptionsOfferAllStatusesPlusTheEight is the select's
// option set: "All statuses" first, then the store's eight, with the
// request's own choice marked.
func TestMilestoneStatusOptionsOfferAllStatusesPlusTheEight(t *testing.T) {
	options := milestoneStatusOptions(store.MilestoneStatusShipped)

	require.Len(t, options, len(store.MilestoneStatusOrder)+1,
		"'All statuses' plus exactly the eight")
	assert.Equal(t, "All statuses", options[0].Label)
	assert.False(t, options[0].Selected, "'All statuses' is selected only when no status was chosen")

	var labels []string
	for _, o := range options[1:] {
		labels = append(labels, o.Label)
	}
	assert.Equal(t, []string{
		"not started", "in design", "designed", "planned",
		"in progress", "partially complete", "shipped", "abandoned",
	}, labels, "the eight statuses, in the store's own order")

	var selected []string
	for _, o := range options {
		if o.Selected {
			selected = append(selected, o.Label)
		}
	}
	assert.Equal(t, []string{"shipped"}, selected, "exactly the requested status is marked")

	// The unfiltered case marks "All statuses" and nothing else.
	unfiltered := milestoneStatusOptions("")
	var marked []string
	for _, o := range unfiltered {
		if o.Selected {
			marked = append(marked, o.Label)
		}
	}
	assert.Equal(t, []string{"All statuses"}, marked)
}

// TestMilestoneStatusOptionsTrackAStatusTheStoreAdds is the anti-drift
// guard: the select offers store.MilestoneStatusOrder rather than a UI
// copy of the eight, so a ninth status appears in both the options and
// the parser without a second edit.
func TestMilestoneStatusOptionsTrackAStatusTheStoreAdds(t *testing.T) {
	offered := map[string]bool{}
	for _, o := range milestoneStatusOptions("") {
		offered[o.Value] = true
	}
	for _, status := range store.MilestoneStatusOrder {
		assert.True(t, offered[string(status)],
			"the select must offer %q, because the parser accepts it", status)
	}
	assert.Len(t, offered, len(store.MilestoneStatusOrder)+1,
		"and nothing beyond 'All statuses' plus the store's set")
}

// TestParseMilestoneStatusFilter is the parser behind the select: absent
// means every status, a known one resolves to itself, and anything else is
// refused rather than silently widened to "all" -- a mistyped URL showing
// the whole table would leave an operator believing their filter applied.
func TestParseMilestoneStatusFilter(t *testing.T) {
	app := &App{}

	for _, status := range store.MilestoneStatusOrder {
		r := httptest.NewRequest(http.MethodGet, "/products/x/milestones?status="+strings.ReplaceAll(string(status), " ", "%20"), nil)
		got, err := app.parseMilestoneStatusFilter(r, milestonesProductID)
		require.NoError(t, err, "status %q must parse", status)
		assert.Equal(t, status, got)
	}

	for _, query := range []string{"", "?status=", "?status=everything", "?status=Shipped", "?status=not%20started%20now"} {
		r := httptest.NewRequest(http.MethodGet, "/products/x/milestones"+query, nil)
		got, err := app.parseMilestoneStatusFilter(r, milestonesProductID)
		if query == "" || query == "?status=" {
			require.NoError(t, err, "%q must mean every status", query)
			assert.Equal(t, store.MilestoneStatus(""), got)
			continue
		}
		assert.ErrorIs(t, err, errUnknownMilestoneStatus, "%q is not a status krill has", query)
	}
}

// TestMilestonesEmptyDetailNamesTheFilter is the empty state's job: "no
// milestones in this status" and "this product has no milestones" are
// different answers, and only the first is a filtered table.
func TestMilestonesEmptyDetailNamesTheFilter(t *testing.T) {
	assert.Contains(t, milestonesEmptyDetail(""), "no milestones yet")
	filtered := milestonesEmptyDetail(store.MilestoneStatusShipped)
	assert.Contains(t, filtered, "shipped", "the empty state names the active filter")
	assert.Contains(t, filtered, "All statuses", "and says how to widen it")
}

// ---------------------------------------------------------------------------
// 4. rendered markup
// ---------------------------------------------------------------------------

// renderMilestonesTable renders the whole region and returns its markup,
// the shape the handler actually serves.
func renderMilestonesTable(t *testing.T, rows []pages.MilestoneRow) string {
	t.Helper()
	return mustRenderComponent(pages.Milestones(pages.MilestonesPage{
		Product:     pages.ProductHeader{Name: "krill"},
		Path:        milestonesPath(milestonesProductID),
		Statuses:    milestoneStatusOptions(""),
		Rows:        rows,
		EmptyDetail: milestonesEmptyDetail(""),
	}))
}

// TestMilestonesTableRendersTheColumnsTheFRNames: Milestone (linking to
// detail), Status (a badge), Progress, FR budget and Outcome -- checked
// against the rendered markup rather than the view model, because a field
// can sit on the row and still not reach the page.
func TestMilestonesTableRendersTheColumnsTheFRNames(t *testing.T) {
	shipped := milestoneFixtureOf(t, "M4 Work axis", 2, store.MilestoneStatusShipped, 14, 14)
	shipped.entry.FRBudget = budgetPtr(12)
	shipped.entry.Outcome = outcomePtr("Task, claim, lease, attempt and note.")
	fresh := milestoneFixtureOf(t, "M6 UI facelift", 3, store.MilestoneStatusInDesign, 0, 0)

	rows := rowsOf(t, listingOf(shipped, fresh), progressOf(shipped, fresh), "")
	html := renderMilestonesTable(t, rows)

	for _, header := range []string{"Milestone", "Status", "Progress", "FR budget", "Outcome"} {
		assert.Contains(t, html, ">"+header+"<", "the table has a %q column", header)
	}

	// The name links to the milestone's detail page, product-scoped.
	detail := milestoneDetailHref(milestonesProductID, shipped.entry.ID)
	assert.Contains(t, html, `href="`+detail+`"`, "the milestone name links to its detail")
	assert.Contains(t, html, "M4 Work axis")

	// The status renders as a badge carrying the status label, through
	// the same style the delivery page and the Board use.
	assert.Contains(t, html, "badge", "status renders as a badge")
	assert.Contains(t, html, "shipped")

	// Progress: a bar on the milestone that has tasks, its figures beside
	// it, and "No tasks yet" for the one that does not.
	assert.Contains(t, html, `data-krill="milestone-progress"`)
	assert.Contains(t, html, "14/14 tasks done")
	assert.Contains(t, html, `data-krill="milestone-no-tasks"`)
	assert.Contains(t, html, "No tasks yet")
	assert.NotContains(t, html, "0/0", "a milestone with no tasks never renders '0 of 0'")

	// FR budget right-aligned, and the outcome sentence.
	assert.Contains(t, html, "12")
	assert.Contains(t, html, "Task, claim, lease, attempt and note.")
}

// TestMilestonesFragmentCarriesTheSwapTarget is the htmx contract: the
// status select swaps the region by id, and htmx's outerHTML removes the
// target it replaced. A fragment without that id would leave the next
// filter change with nothing to swap into, and the select would go inert
// after one use.
func TestMilestonesFragmentCarriesTheSwapTarget(t *testing.T) {
	shipped := milestoneFixtureOf(t, "M4", 2, store.MilestoneStatusShipped, 14, 14)
	rows := rowsOf(t, listingOf(shipped), progressOf(shipped), "")

	page := pages.MilestonesPage{
		Product:     pages.ProductHeader{Name: "krill"},
		Path:        milestonesPath(milestonesProductID),
		Statuses:    milestoneStatusOptions(""),
		Rows:        rows,
		EmptyDetail: milestonesEmptyDetail(""),
	}
	fragment := mustRenderComponent(pages.MilestonesRows(page))

	assert.Contains(t, fragment, `id="`+pages.MilestonesAnchor+`"`,
		"the swapped-in fragment must carry the id the next swap targets")
	assert.Contains(t, fragment, "M4")

	// The select targets that same anchor, so the swap has something to
	// replace -- and the whole region, not just the table, so the control
	// that fired the swap comes back marked with the new choice.
	assert.Contains(t, fragment, `hx-target="#`+pages.MilestonesAnchor+`"`)
	assert.Contains(t, fragment, `hx-swap="outerHTML"`)
	assert.Contains(t, fragment, "All statuses")
}

// TestMilestonesStatusSelectIsAGetFormOnThePagesOwnURL is the no-JS
// contract: the filter is a plain GET form whose action is the page's own
// URL, so a reload, a shared link and a JavaScript-free submit all land on
// the same filtered table.
func TestMilestonesStatusSelectIsAGetFormOnThePagesOwnURL(t *testing.T) {
	shipped := milestoneFixtureOf(t, "M4", 2, store.MilestoneStatusShipped, 14, 14)
	page := pages.MilestonesPage{
		Product:     pages.ProductHeader{Name: "krill"},
		Path:        milestonesPath(milestonesProductID),
		Statuses:    milestoneStatusOptions(store.MilestoneStatusShipped),
		Rows:        rowsOf(t, listingOf(shipped), progressOf(shipped), store.MilestoneStatusShipped),
		EmptyDetail: milestonesEmptyDetail(""),
	}
	html := mustRenderComponent(pages.Milestones(page))

	path := milestonesPath(milestonesProductID)
	assert.Contains(t, html, `action="`+path+`"`, "the form GETs the page's own URL")
	assert.Contains(t, html, `method="get"`)
	assert.Contains(t, html, `name="status"`, "the filter travels as ?status=")
	assert.Contains(t, html, `data-krill="milestone-status-select"`)
	assert.Contains(t, html, "selected", "the requested status comes back marked")

	// The path is the product-scoped milestones URL, which is the one the
	// mux serves.
	assert.True(t, strings.HasPrefix(path, "/products/"+milestonesProductID.String()+"/milestones"),
		"the form must address the route this binary serves, got %q", path)
}

// TestMilestonesProgressErrorKeepsTheTable is the failure-path contract: a
// progress read that fails costs the Progress column, never the roadmap.
// Every row still renders, and each Progress cell says what could not be
// read rather than showing a bar it does not have.
func TestMilestonesProgressErrorKeepsTheTable(t *testing.T) {
	a := milestoneFixtureOf(t, "M1", 1, store.MilestoneStatusInProgress, 1, 2)
	b := milestoneFixtureOf(t, "M2", 2, store.MilestoneStatusShipped, 3, 3)

	rows := milestoneRowsWithoutProgress(milestonesProductID, listingOf(a, b), "")
	html := mustRenderComponent(pages.Milestones(pages.MilestonesPage{
		Product:      pages.ProductHeader{Name: "krill"},
		Path:         milestonesPath(milestonesProductID),
		Statuses:     milestoneStatusOptions(""),
		Rows:         rows,
		ProgressError: milestonesProgressError,
		EmptyDetail:  milestonesEmptyDetail(""),
	}))

	assert.Contains(t, html, "M1")
	assert.Contains(t, html, "M2")
	assert.NotContains(t, html, `data-krill="milestone-progress"`,
		"no bar may render when the figures were never read")
	assert.NotContains(t, html, "No tasks yet",
		"an unread milestone is not an empty one")
	assert.Contains(t, html, milestonesProgressError)
}