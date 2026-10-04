// Wire-driven coverage for the product-wide Tasks TABLE (FR f41a352d):
// the rows it renders, the order it renders them in, the milepebble
// aggregation, the claim identity each row carries, and its read-onlyness.
//
// Every case mounts the real registrations through productTaskMux and
// asserts against the served HTML rather than against the view model, so a
// row that is built correctly and never rendered fails here -- as does a
// row rendered from a value the read did not supply.
package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The tasks a multi-milestone fixture returns, in the order the store's own
// read hands them over. The order is deliberately NOT alphabetical by
// milestone, by creation or by title, so a table that re-sorts its rows
// cannot accidentally agree with the read.
//
// The literal ids are what the assertions below compare against, so a
// failure names the same ids every run.
var (
	// A task on the uncut oldest milestone, and two on the cut middle one --
	// one directly, one on a milepebble. The middle milestone's tasks
	// arriving interleaved is the aggregation case: the read returns them
	// together under the milestone, and the table must not separate them.
	productTaskRowUncut    = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	productTaskRowOnMiddle = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	productTaskRowOnMilep  = uuid.MustParse("33333333-3333-3333-3333-333333333333")

	// A task on the newest milestone, last in the read's order because the
	// store sorts milestone position DESCENDING.
	productTaskRowNewest = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

// productTaskRowFixture is the read's page for the multi-milestone case, in
// the order the store returns it.
func productTaskRowFixture() []store.ProductTaskRow {
	milepebble := store.ProductTaskMilepebbleRef{
		ID:     productTaskMilepebble,
		Name:   "Middle milepebble",
		Status: store.MilestoneStatusInDesign,
	}
	return []store.ProductTaskRow{
		{
			TaskID:       productTaskRowOnMiddle,
			Title:        "A task on the cut milestone itself",
			CreatedAt:    time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
			Milestone:    store.ProductTaskMilestoneRef{ID: productTaskMilestone, Name: "Middle milestone", Status: store.MilestoneStatusInProgress},
			CurrentLane:  store.LaneImplementation,
			State:        store.TaskStateEscalated,
			AttemptCount: 2,
			AttemptCap:   store.DefaultAttemptCap,
		},
		{
			TaskID:       productTaskRowOnMilep,
			Title:        "A task on a milepebble of the cut milestone",
			CreatedAt:    time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
			Milestone:    store.ProductTaskMilestoneRef{ID: productTaskMilestone, Name: "Middle milestone", Status: store.MilestoneStatusInProgress},
			Milepebble:   &milepebble,
			CurrentLane:  store.LaneTesting,
			State:        store.TaskStateActive,
			AttemptCount: 1,
			AttemptCap:   store.DefaultAttemptCap,
		},
		{
			TaskID:       productTaskRowNewest,
			Title:        "A task on the newest milestone",
			CreatedAt:    time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
			Milestone:    store.ProductTaskMilestoneRef{ID: productTaskNewestMilestone, Name: "Newest milestone", Status: store.MilestoneStatusInProgress},
			CurrentLane:  store.LaneDone,
			State:        store.TaskStateActive,
			AttemptCount: 0,
			AttemptCap:   store.DefaultAttemptCap,
		},
		{
			TaskID:       productTaskRowUncut,
			Title:        "A task on the uncut oldest milestone",
			CreatedAt:    time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
			Milestone:    store.ProductTaskMilestoneRef{ID: productTaskOldestMilestone, Name: "Oldest milestone", Status: store.MilestoneStatusInProgress},
			CurrentLane:  store.LaneScaffold,
			State:        store.TaskStateActive,
			AttemptCount: 0,
			AttemptCap:   store.DefaultAttemptCap,
		},
	}
}

// taskRowSpans is every rendered row's data-krill-task-id, in the order they
// appear in the markup. Reading the attribute rather than the visible text
// is what makes this the ROW order: a table can sort its cells within a row
// for readability without that being a re-sort of the rows.
var taskRowIDPattern = regexp.MustCompile(`data-krill-task-id="([0-9a-f-]{36})"`)

func taskRowIDsIn(body string) []string {
	matches := taskRowIDPattern.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// regionHTML is the rendered product-tasks region, cut out of the whole
// page. Assertions about what the TABLE offers have to be made here rather
// than page-wide: the sidebar carries its own controls, and a page-wide
// count would make an assertion about the table quietly an assertion about
// the chrome instead.
func regionHTML(t *testing.T, body string) string {
	t.Helper()
	const marker = `<section id="` + pages.ProductTasksAnchor + `"`
	start := strings.Index(body, marker)
	require.NotEqual(t, -1, start, "the region never rendered: %s", body)
	end := strings.Index(body[start:], "</section>")
	require.NotEqual(t, -1, end, "the region never closes: %s", body)
	return body[start : start+end]
}

// taskRowHTML returns the single rendered row carrying this task id, or the
// whole body when the id is not there -- so a failing assertion shows the
// row that was wrong, not just that one was missing.
func taskRowHTML(t *testing.T, body, taskID string) string {
	t.Helper()
	at := strings.Index(body, `data-krill-task-id="`+taskID+`"`)
	require.NotEqual(t, -1, at, "no row for task %s: %s", taskID, body)
	// Walk back to the row's own opening tag, so the extracted span is the
	// whole <tr> rather than a fragment of it.
	start := strings.LastIndex(body[:at], "<tr")
	require.NotEqual(t, -1, start, "the task id is not inside a row: %s", body)
	end := strings.Index(body[at:], "</tr>")
	require.NotEqual(t, -1, end, "the row never closes: %s", body)
	return body[start : at+end]
}

// TestTasksTableRendersRowsInTheReadsOrder is FR f41a352d's ordering rule,
// asserted the only way it can fail visibly: the fixture's rows are handed
// to the read in an order that is NOT alphabetical by milestone, creation
// or title, and the markup must come back in exactly that order.
//
// Re-sorting here is not a cosmetic choice. The store's sort is what a
// continuation token is bound to, so a table that ordered its rows by
// anything else would leave the next page starting from a row this one did
// not end on -- and the operator paging through the table would see a task
// twice or not at all.
func TestTasksTableRendersRowsInTheReadsOrder(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL(""))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	want := []string{
		productTaskRowOnMiddle.String(),
		productTaskRowOnMilep.String(),
		productTaskRowNewest.String(),
		productTaskRowUncut.String(),
	}
	assert.Equal(t, want, taskRowIDsIn(rec.Body.String()),
		"the table must render the read's own order, never a re-sort of it")
}

// TestTasksTableRendersTheStoresSortNotAFriendlyOne documents WHY the order
// above is the one asserted: the fixture's rows are deliberately not in the
// order a reader would expect, and the test would still pass if the table
// agreed with the read. Pinning the store's own sort (milestone position
// DESC, then id, then creation) means the assertion above is checking
// pass-through rather than coincidence.
func TestTasksTableRendersTheStoresSortNotAFriendlyOne(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL(""))
	body := rec.Body.String()

	// The fixture's own order is what the store's sort produces: the middle
	// milestone's two tasks (position 2) before the newest (position 3)
	// before the oldest (position 1), and within the middle milestone the
	// earlier-created task first. Neither grouping is what a name- or
	// date-ordered table would produce.
	middle := strings.Index(body, `data-krill-task-id="`+productTaskRowOnMiddle.String()+`"`)
	newest := strings.Index(body, `data-krill-task-id="`+productTaskRowNewest.String()+`"`)
	oldest := strings.Index(body, `data-krill-task-id="`+productTaskRowUncut.String()+`"`)
	require.NotEqual(t, -1, middle)
	require.NotEqual(t, -1, newest)
	require.NotEqual(t, -1, oldest)
	assert.Less(t, middle, newest, "position 2 precedes position 3 (position DESC)")
	assert.Less(t, newest, oldest, "position 3 precedes position 1 (position DESC)")
	assert.Less(t, productTaskRowFixture()[0].CreatedAt, rows[1].CreatedAt)
}

// TestTasksTableAggregatesACutMilestoneUnderIt pins FR f41a352d's
// aggregation half: a cut milestone's tasks appear under the milestone in
// the same table, each row naming the milepebble it came from -- and the
// table never shows an empty state while the read returned rows.
//
// The empty state is the half that matters most. The pre-redesign list
// answered a cut milestone with "Tasks live on this milestone's
// milepebbles" and no rows at all, which reads as a milestone with no work
// while its milepebbles were full of it.
func TestTasksTableAggregatesACutMilestoneUnderIt(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	// Milestone scope is the mode the store aggregates in (its
	// ProductTaskScopeMilestone means the milestone AND its milepebbles).
	rec := fetch(t, mux, productTaskTasksURL("scope=milestone&container_id="+productTaskMilestone.String()))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	assert.NotContains(t, body, `data-krill="product-tasks-empty"`,
		"a cut milestone with tasks must never show the empty state")
	assert.NotContains(t, body, `data-krill="product-tasks-error"`)

	// Both of the cut milestone's rows are here, one naming the milepebble
	// and one not -- an uncut milestone's task has no milepebble cell to
	// fill, and inventing one would misreport where the work came from.
	onMilepebble := taskRowHTML(t, body, productTaskRowOnMilep.String())
	assert.Contains(t, onMilepebble, "Middle milepebble",
		"a milepebble's task names the milepebble it came from")
	assert.Contains(t, onMilepebble, "Middle milestone",
		"and the milestone it was aggregated under")

	onMilestone := taskRowHTML(t, body, productTaskRowOnMiddle.String())
	assert.Contains(t, onMilestone, "Middle milestone")
	assert.NotContains(t, onMilestone, "Middle milepebble",
		"a task scoped to the milestone itself has no milepebble to name")

	// The same aggregation has to hold in the all-incomplete scope, which
	// is the DEFAULT an operator lands on. Asserting it only for an
	// explicitly-picked milestone would leave the default view free to
	// answer a cut milestone with the empty state.
	defaultRec := fetch(t, mux, productTaskTasksURL(""))
	require.Equal(t, http.StatusOK, defaultRec.Code)
	defaultBody := defaultRec.Body.String()

	assert.NotContains(t, defaultBody, `data-krill="product-tasks-empty"`,
		"a cut milestone with tasks must never show the empty state, in the default scope either")
	defaultOnMilepebble := taskRowHTML(t, defaultBody, productTaskRowOnMilep.String())
	assert.Contains(t, defaultOnMilepebble, "Middle milepebble",
		"the default scope names the milepebble too")
	assert.Contains(t, defaultOnMilepebble, "Middle milestone",
		"under its parent milestone")
}

// TestTasksTableRowsCarryTheObservedClaimIdentity pins FR f41a352d's claim
// identity: the row carries the claim id and lease expiry the READ observed,
// as data attributes a later write can be guarded against.
//
// A row carrying one without the other is the failure this asserts against.
// A claimed row whose lease is missing claims a lease it never observed; a
// lease with no claim id names a claim no write could match against.
func TestTasksTableRowsCarryTheObservedClaimIdentity(t *testing.T) {
	claimID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	live := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)

	for _, tc := range []struct {
		name         string
		mutate       func(*store.ProductTaskRow)
		wantClaim    string
		wantLease    string
		wantBadges   []string
		absentBadges []string
	}{
		{
			name: "a live claim carries both halves and the claimed badge",
			mutate: func(r *store.ProductTaskRow) {
				r.ClaimID = &claimID
				r.LeaseExpiresAt = &live
			},
			wantClaim:  claimID.String(),
			wantLease:  live.Format(time.RFC3339),
			wantBadges: []string{`data-krill="task-badge-claimed"`},
			// The rule the one state mapper exists to hold: a claim that has
			// not lapsed is not lease-expired.
			absentBadges: []string{`data-krill="task-badge-lease-expired"`},
		},
		{
			name: "a lapsed lease carries both halves and never the claimed badge",
			mutate: func(r *store.ProductTaskRow) {
				expired := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
				r.ClaimID = &claimID
				r.LeaseExpiresAt = &expired
			},
			wantClaim:  claimID.String(),
			wantLease:  time.Now().Add(-time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339),
			wantBadges: []string{`data-krill="task-badge-lease-expired"`},
			// A task whose worker has gone is not being worked on.
			absentBadges: []string{`data-krill="task-badge-claimed"`},
		},
		{
			name:         "an unclaimed row carries neither and claims no lease",
			wantClaim:    "",
			wantLease:    "",
			absentBadges: []string{`data-krill="task-badge-claimed"`, `data-krill="task-badge-lease-expired"`},
		},
		{
			// The store guarantees the two agree, so this shape never
			// arrives from the real read. It is here because the row is the
			// thing a later claim-guarded write reads, and a half-present
			// claim would be worse than none: a claim id with no lease says
			// "claimed, expiry unknown", and a lease with no claim id names a
			// claim no write could match against. Rendering neither until
			// both are present is what keeps a row honest if that guarantee
			// is ever broken.
			name: "a half-present claim carries neither half",
			mutate: func(r *store.ProductTaskRow) {
				r.ClaimID = &claimID
			},
			wantClaim:    "",
			wantLease:    "",
			absentBadges: []string{`data-krill="task-badge-claimed"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := productTaskRowFixture()[2]
			if tc.mutate != nil {
				tc.mutate(&row)
			}
			tasks := &recordingProductTasks{rows: []store.ProductTaskRow{row}, total: 1}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(""))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			rendered := taskRowHTML(t, rec.Body.String(), row.TaskID.String())

			if tc.wantClaim == "" {
				assert.Contains(t, rendered, `data-krill-claim-id=""`,
					"an unclaimed row must not carry a claim id")
			} else {
				assert.Contains(t, rendered, `data-krill-claim-id="`+tc.wantClaim+`"`)
			}
			if tc.wantLease == "" {
				assert.Contains(t, rendered, `data-krill-lease-expires-at=""`,
					"a row holding no claim must not name a lease expiry")
			} else {
				assert.Contains(t, rendered, `data-krill-lease-expires-at="`+tc.wantLease+`"`,
					"the expiry is the absolute instant the read observed, not a relative one")
			}
			for _, badge := range tc.wantBadges {
				assert.Contains(t, rendered, badge)
			}
			for _, badge := range tc.absentBadges {
				assert.NotContains(t, rendered, badge)
			}
		})
	}
}

// TestTasksTableIssuesNoWrite pins FR f41a352d's read-only rule.
//
// It asserts on the REGION's own markup rather than by driving a write,
// because what could go wrong is a control APPEARING -- a lane-move menu, a
// cancel button, a form -- and driving a write would not notice a control
// that was never pressed. The region is cut out of the page first: the
// sidebar's own Product switcher is a form too, and counting page-wide
// forms would make this assert about chrome rather than about the table.
func TestTasksTableIssuesNoWrite(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL(""))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	region := regionHTML(t, body)
	assert.Equal(t, 1, strings.Count(region, "<form"),
		"the region's only form is the scope control's plain GET; a second would be a control that changes something")
	assert.Contains(t, region, `<form method="get"`)
	assert.NotContains(t, strings.ToLower(region), `method="post"`,
		"the table offers no control that issues a write request")
	assert.NotContains(t, region, "hx-post")
	assert.NotContains(t, region, "hx-delete")
	assert.NotContains(t, region, "hx-put")
	// The only htmx request the region makes is its own Refresh, which is a
	// GET back to this same URL.
	assert.Contains(t, region, `hx-get="/products/`+productTaskProduct.String()+`/tasks"`)
	// The lane-move affordance the read-only rule rules out by name.
	assert.NotContains(t, strings.ToLower(region), "drag")
}

// TestBoardViewIsAlsoReadOnly pins the same rule on the OTHER view of the
// scope. FR f41a352d's read-only bullet names the Tasks table AND the
// Board, and both are served by one handler -- so a read-only assertion on
// only the Tasks URL would be satisfied by a Board that offered a
// drag-and-drop card for free.
//
// The Board here renders the Tasks region rather than lanes (the swimlane
// layout is a later task's), which is exactly why this has to be pinned
// now: the row markup is shared, and the day the Board grows its own
// controls this is the test that stops them being write-capable.
func TestBoardViewIsAlsoReadOnly(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/board")
	require.Equal(t, http.StatusOK, rec.Code)

	region := regionHTML(t, rec.Body.String())
	assert.Contains(t, region, `data-krill-view="Board"`, "the Board view rendered")
	assert.Equal(t, 1, strings.Count(region, "<form"),
		"the Board's only form is the scope control's plain GET")
	for _, forbidden := range []string{"hx-post", "hx-put", "hx-delete", `method="post"`, "drag"} {
		assert.NotContains(t, strings.ToLower(region), strings.ToLower(forbidden),
			"the Board must offer no control that changes task state")
	}
}

// taskTableHTML returns the rendered Tasks table, cut out of the page.
//
// The lane filter's select is a legitimate place for a lane NAME to appear as
// plain text -- it is the operator naming the value they are filtering by,
// not a row rendering its lane -- so the "lane reaches the page as bare text
// rather than a badge" assertion has to be made about the TABLE. Scoping it
// to the whole page would forbid the control FR 61d7fb7b requires.
func taskTableHTML(t *testing.T, body string) string {
	t.Helper()
	const marker = `data-krill="task-table"`
	start := strings.Index(body, marker)
	require.NotEqual(t, -1, start, "the table never rendered: %s", body)
	end := strings.Index(body[start:], "</table>")
	require.NotEqual(t, -1, end, "the table never closes: %s", body)
	return body[start : start+end]
}

// TestTasksTableRendersLaneAndStateAsBadges is FR de4d0e42's rule on this
// page: lane and state are badges from the shared mappers, and NEITHER
// reaches the table as bare text. The last part is asserted by stripping
// every badge span and looking for the value left behind -- a label that
// happens to match is not a badge.
func TestTasksTableRendersLaneAndStateAsBadges(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL(""))
	body := rec.Body.String()

	assert.Contains(t, body, `data-krill="task-lane"`, "the lane is a badge")
	assert.Contains(t, body, `data-krill="task-badge-escalated"`,
		"the escalated task's state is a badge")
	assert.Contains(t, body, ">"+string(store.LaneImplementation)+"<",
		"the badge carries the lane's own name")

	stripped := stripBadges(taskTableHTML(t, body))
	for _, value := range []string{
		string(store.LaneImplementation), string(store.LaneTesting),
		string(store.LaneDone), string(store.LaneScaffold),
	} {
		assert.NotContains(t, stripped, ">"+value+"<",
			"lane %q must not survive as bare text once the badges are stripped", value)
	}
}

// TestTasksRegionCarriesTheFreshnessInstantNotARelativeString pins NFR
// 7b497d92's rule as it applies to this region: the markup carries the read
// instant and NOTHING derived from it.
//
// A server-rendered "8 seconds ago" is wrong the moment htmx swaps the
// region in, and nothing inside the region would ever reveal it -- the
// fragment carries no clock. So the relative text has to be derived
// client-side, from the absolute instant the server did emit.
func TestTasksRegionCarriesTheFreshnessInstantNotARelativeString(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL(""))
	body := rec.Body.String()

	instant := instantPattern.FindStringSubmatch(body)
	require.NotNil(t, instant,
		"the region carries no Updated element: %s", body)

	// One element, and it is the region's own -- a page with two of them is
	// two claims about when it was read, and they would disagree.
	assert.Equal(t, 1, strings.Count(body, `data-krill="updated-at"`))

	// The instant is absolute RFC3339 and parses as one.
	parsed, err := time.Parse(time.RFC3339, instant[1])
	require.NoError(t, err, "the freshness instant is not RFC3339: %s", instant[1])
	assert.WithinDuration(t, time.Now(), parsed, time.Minute,
		"the instant is the read time, not a fixed fixture value")

	// No derived-relative text anywhere in the region.
	region := regionHTML(t, body)
	assert.NotContains(t, region, "ago",
		"the server never emits a now-derived relative string inside a fragment htmx may re-swap")

	// The script that derives it lives in the document, not in the region,
	// so it is not re-shipped (and re-run) on every swap.
	assert.NotContains(t, region, "<script")
	assert.Contains(t, body, "data-krill-updated-at",
		"the head script keys off the attribute the region renders")
	assert.Contains(t, body, "htmx:after:swap",
		"a Refresh's new instant is picked up without a reload")
}

// instantPattern is the read instant the freshness element carries.
var instantPattern = regexp.MustCompile(`data-krill-updated-at="([^"]+)"`)

// TestTasksTableLinksToTheProductScopedDetail pins the row's detail link:
// it is the PRODUCT-scoped path, not the per-container one.
//
// The per-container form would name a milestone this table is not scoped to.
// A row belonging to any other milestone would then link to that other
// milestone's page, where the task is not the answer -- a link that looks
// right and 404s.
func TestTasksTableLinksToTheProductScopedDetail(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL(""))
	body := rec.Body.String()

	for _, id := range taskRowIDsIn(body) {
		rendered := taskRowHTML(t, body, id)
		want := `href="/products/` + productTaskProduct.String() + `/tasks/` + id + `"`
		assert.Contains(t, rendered, want,
			"a row's title links to the product-scoped detail for its own task id")
		assert.NotContains(t, rendered, "/milestones/",
			"no row links through the per-container detail path")
	}
}

// productTaskDetailMux mounts the real registrations for the product-scoped
// task detail, with two tasks in the store: one under this product's cut
// milestone, and one under a milestone the product's own listing does not
// carry. The second is the cross-product case -- a task that EXISTS and is
// readable by id, which is exactly the shape that a route skipping the
// membership check would render.
func productTaskDetailMux(t *testing.T) *http.ServeMux {
	t.Helper()

	tasks := &recordingProductTasks{}
	tasks.tasks = map[uuid.UUID]store.Task{
		productTaskRowOnMilep: {
			ID:          productTaskRowOnMilep,
			Title:       "A task on a milepebble of the cut milestone",
			MilestoneID: productTaskMilepebble,
			CurrentLane: store.LaneTesting,
		},
		productTaskOtherMilestone: {
			ID:          productTaskOtherMilestone,
			Title:       "A task on another product's milestone",
			MilestoneID: productTaskOtherMilestone,
			CurrentLane: store.LaneDone,
		},
	}

	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products: []store.Product{{ID: productTaskProduct, Name: "krill"}},
		listing:  productTaskListing(),
	}
	app.tasks = tasks
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// TestProductScopedTaskDetailServesTheTask is the other half of the row
// link: the path a row points at actually resolves, for a task under a
// milepebble of this product's cut milestone.
func TestProductScopedTaskDetailServesTheTask(t *testing.T) {
	mux := productTaskDetailMux(t)

	rec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/tasks/"+productTaskRowOnMilep.String())

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "A task on a milepebble of the cut milestone")
}

// TestProductScopedTaskDetailRefusesATaskOutsideTheProduct: the product-
// scoped URL names no container, so the only membership check available is
// the product's own listing. The task EXISTS and reads fine by id, so only
// the listing check can refuse it -- and refusing it is what keeps another
// product's task out of this product's chrome.
func TestProductScopedTaskDetailRefusesATaskOutsideTheProduct(t *testing.T) {
	mux := productTaskDetailMux(t)

	rec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/tasks/"+
		productTaskOtherMilestone.String())

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), "A task on another product's milestone")
}

// TestPerContainerTaskDetailRedirectsToTheProductScopedDetail pins the
// other half of the route split: the per-container URL is retired (FR
// 0c03eac1) into the product-scoped detail the rows themselves link to.
//
// This is driven through mountShellRoutes rather than a hand-built mux so
// the assertion covers the real registration as well as the handler -- a
// URL that redirected correctly but was no longer mounted would pass a
// handler-level test. The old URL is spelled by taskDetailPath, the same
// helper the row link test says a row must NOT use, so the two URLs stay
// distinguishable by construction rather than by a literal typed here.
//
// And the redirect preserves what the operator came for: the container
// named is the MILEPEBBLE the task belongs to, and the target carries only
// the tid, so it lands on the task's own page.
func TestPerContainerTaskDetailRedirectsToTheProductScopedDetail(t *testing.T) {
	mux := productTaskDetailMux(t)

	legacy := taskDetailPath(productTaskProduct, productTaskMilepebble, productTaskRowOnMilep)
	rec := fetch(t, mux, legacy)

	require.Equal(t, http.StatusFound, rec.Code, "body: %s", rec.Body.String())
	want := "/products/" + productTaskProduct.String() + "/tasks/" + productTaskRowOnMilep.String()
	assert.Equal(t, want, rec.Header().Get("Location"))

	followed := fetch(t, mux, rec.Header().Get("Location"))
	require.Equal(t, http.StatusOK, followed.Code, "body: %s", followed.Body.String())
	assert.Contains(t, followed.Body.String(), "A task on a milepebble of the cut milestone",
		"the redirect must land on the task, not merely on a page that answers")
}

// TestPerContainerTaskDetailRedirectStillRefusesATaskOutsideTheProduct is
// the membership rule the redirect has to carry with it: retiring the
// per-container URL must not retire the check that keeps another product's
// task out of this product's chrome.
//
// The product-scoped URL names no container, so its only membership check
// is the listing lookup, and the redirect is safe only because that check
// travels with it. Driving it through the legacy URL rather than straight
// at the target is what makes this a test of the retirement.
func TestPerContainerTaskDetailRedirectStillRefusesATaskOutsideTheProduct(t *testing.T) {
	mux := productTaskDetailMux(t)

	legacy := taskDetailPath(productTaskProduct, productTaskMilepebble, productTaskOtherMilestone)
	rec := fetch(t, mux, legacy)
	require.Equal(t, http.StatusFound, rec.Code)

	followed := fetch(t, mux, rec.Header().Get("Location"))
	assert.Equal(t, http.StatusNotFound, followed.Code)
	assert.NotContains(t, followed.Body.String(), "A task on another product's milestone")
}

// TestTasksRegionRefreshReReadsTheCurrentScope pins the Refresh control's
// target: it carries THIS request's query, so pressing it re-reads the
// scope and filters the URL currently names.
//
// A Refresh pointing at the bare product Tasks path would answer with the
// product-wide default -- silently discarding a milestone scope, a lane
// filter and only-stuck, and leaving the operator looking at a different
// table than the one they pressed Refresh on with no indication it moved.
func TestTasksRegionRefreshReReadsTheCurrentScope(t *testing.T) {
	rows := productTaskRowFixture()
	tasks := &recordingProductTasks{rows: rows, total: len(rows)}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	query := "scope=milestone&container_id=" + productTaskMilestone.String() +
		"&lane=Done&only_stuck=1"
	rec := fetch(t, mux, productTaskTasksURL(query))
	require.Equal(t, http.StatusOK, rec.Code)

	region := regionHTML(t, rec.Body.String())
	at := strings.Index(region, `data-krill="refresh"`)
	require.NotEqual(t, -1, at, "the region offers no Refresh: %s", region)

	refresh := region[at:]
	if end := strings.Index(refresh, "</button>"); end != -1 {
		refresh = refresh[:end]
	}
	assert.Contains(t, refresh, "scope=milestone",
		"Refresh must re-read the scope the URL names")
	assert.Contains(t, refresh, "container_id="+productTaskMilestone.String())
	assert.Contains(t, refresh, "lane=Done")
	assert.Contains(t, refresh, "only_stuck=1")

	// And it is a GET back to this same page, not a write and not a
	// different one.
	assert.Contains(t, refresh, `hx-get="/products/`+productTaskProduct.String()+`/tasks?`)
}

// TestTasksTableRendersAttemptsAgainstTheCap pins FR f41a352d's attempts
// column: "2 of 3" against the cap the READ reported.
//
// The product read supplies its own cap rather than this package assuming
// the default, so the cell has to be built from what the read said. A
// table that hardcoded store.DefaultAttemptCap would render "2 of 3" for
// every row and be right until a task's cap was ever anything else.
func TestTasksTableRendersAttemptsAgainstTheCap(t *testing.T) {
	for _, tc := range []struct {
		name       string
		count      int
		cap        int
		want       string
		absentCell string
	}{
		{
			name:  "the read's own cap is used",
			count: 2,
			cap:   5,
			want:  "2 of 5",
			// The default would read "2 of 3" -- so this assertion is what
			// distinguishes reading the cap from assuming it.
			absentCell: ">2 of 3<",
		},
		{
			name:  "a cap the read did not supply falls back to the default",
			count: 1,
			cap:   0,
			// "1 of 0" would be nonsense, and a cap of zero read as a
			// reached cap would put a "capped" badge on every row.
			want: "1 of 3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := productTaskRowFixture()[0]
			row.AttemptCount = tc.count
			row.AttemptCap = tc.cap
			tasks := &recordingProductTasks{rows: []store.ProductTaskRow{row}, total: 1}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(""))
			require.Equal(t, http.StatusOK, rec.Code)

			rendered := taskRowHTML(t, rec.Body.String(), row.TaskID.String())
			cell := rowCell(t, rendered, "task-attempts")
			assert.Contains(t, cell, tc.want)
			if tc.absentCell != "" {
				assert.NotContains(t, rendered, tc.absentCell)
			}
		})
	}
}

// rowCell returns the single cell of a rendered row carrying this
// data-krill marker, so an assertion about a column is made about that
// column rather than about whatever text the row happens to contain.
func rowCell(t *testing.T, row, marker string) string {
	t.Helper()
	needle := `data-krill="` + marker + `"`
	at := strings.Index(row, needle)
	require.NotEqual(t, -1, at, "no %s cell in the row: %s", marker, row)
	start := strings.LastIndex(row[:at], "<td")
	require.NotEqual(t, -1, start, "the marker is not inside a cell: %s", row)
	end := strings.Index(row[at:], "</td>")
	require.NotEqual(t, -1, end, "the cell never closes: %s", row)
	return row[start : at+end]
}

// cellText is a cell's text content with its tags stripped, so "this cell
// says nothing" can be asserted without depending on templ's whitespace.
func cellText(cell string) string {
	out := cell
	for {
		open := strings.Index(out, "<")
		if open == -1 {
			return out
		}
		closeAt := strings.Index(out[open:], ">")
		if closeAt == -1 {
			return out
		}
		out = out[:open] + " " + out[open+closeAt+1:]
	}
}

// TestTasksTableLeaseCellNamesTheExpiryACLaimedRowHolds pins the claim
// state's VISIBLE half. The data attributes are FR f41a352d's claim
// identity, but the row also has to show the operator the lease the read
// observed -- an expiry in the Lease cell is what tells a claimed task from
// an idle one at a glance.
//
// An unclaimed row must leave that cell empty rather than naming some
// default instant.
func TestTasksTableLeaseCellNamesTheExpiryACLaimedRowHolds(t *testing.T) {
	claimID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	lease := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)

	for _, tc := range []struct {
		name  string
		mutat func(*store.ProductTaskRow)
		want  string
	}{
		{
			name: "a claimed row names the expiry it observed",
			mutat: func(r *store.ProductTaskRow) {
				r.ClaimID = &claimID
				r.LeaseExpiresAt = &lease
			},
			want: lease.Format(time.RFC3339),
		},
		{
			name:  "an unclaimed row leaves the cell empty",
			mutat: func(*store.ProductTaskRow) {},
			want:  "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := productTaskRowFixture()[0]
			row.ClaimID, row.LeaseExpiresAt = nil, nil
			tc.mutat(&row)

			tasks := &recordingProductTasks{rows: []store.ProductTaskRow{row}, total: 1}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(""))
			require.Equal(t, http.StatusOK, rec.Code)

			cell := rowCell(t, taskRowHTML(t, rec.Body.String(), row.TaskID.String()), "task-lease")
			if tc.want == "" {
				// templ pads an empty cell with whitespace, so the assertion
				// is that the cell carries no TEXT -- not that it is byte-
				// empty. What would be wrong here is any instant at all.
				assert.Empty(t, strings.TrimSpace(cellText(cell)),
					"an unclaimed row must not name a lease expiry")
				return
			}
			assert.Contains(t, cell, tc.want,
				"the Lease cell shows the absolute expiry the read observed")
		})
	}
}

// TestScopedTaskAndBoardLinkBackToTheRoadmapAtTheirScope is FR 31cbd3eb's
// back-link: from a container-scoped Tasks or Board, the way back to the
// delivery roadmap keeps that same scope.
//
// The scope is the whole assertion. A link to the product's Milestones
// table from a page reading "Scoped to Middle milestone" resolves, renders,
// and lands the operator somewhere else -- so the scoped case is checked
// against the CONTAINER's detail and the unscoped one against the table, in
// both views, rather than the same href being accepted for both.
//
// Driven through the real registrations rather than the builder, because
// the failure this guards against is a field set on the view model and
// never rendered: the Tasks and Board toggle between each other already, so
// a page can carry that link and still have no way back to the roadmap.
func TestScopedTaskAndBoardLinkBackToTheRoadmapAtTheirScope(t *testing.T) {
	mux := productTaskMux(t, &recordingProductTasks{}, productTaskListing(), nil)
	detail := milestoneDetailHref(productTaskProduct, productTaskMilestone)
	table := productHref(productTaskProduct, milestonesSuffix)

	for _, view := range []struct {
		suffix   string
		dataAttr string
	}{
		{tasksSuffix, `data-krill="product-tasks-milestones-link"`},
		{boardSuffix, `data-krill="product-board-milestones-link"`},
	} {
		t.Run("scoped to a milestone", func(t *testing.T) {
			body := fetch(t, mux, productHref(productTaskProduct, view.suffix)+
				"?scope=milestone&container_id="+productTaskMilestone.String()).Body.String()

			assert.Contains(t, body, view.dataAttr, "the %s view offers the way back", view.suffix)
			assert.Contains(t, body, `href="`+detail+`"`,
				"a container-scoped view goes back to THAT container's detail, not the whole table")
		})
		t.Run("scoped to a milepebble", func(t *testing.T) {
			body := fetch(t, mux, productHref(productTaskProduct, view.suffix)+
				"?scope=milepebble&container_id="+productTaskMilepebble.String()).Body.String()

			assert.Contains(t, body, `href="`+milestoneDetailHref(productTaskProduct, productTaskMilepebble)+`"`,
				"a milepebble-scoped view goes back to the MILEPEBBLE's detail")
		})
		t.Run("product-wide scope", func(t *testing.T) {
			body := fetch(t, mux, productHref(productTaskProduct, view.suffix)).Body.String()

			assert.Contains(t, body, view.dataAttr)
			assert.Contains(t, body, `href="`+table+`"`,
				"with no container chosen there is no detail to go back to, so the whole table is the answer")
		})
	}
}
