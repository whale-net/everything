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
			TaskID:      productTaskRowOnMiddle,
			Title:       "A task on the cut milestone itself",
			CreatedAt:   time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
			Milestone:   store.ProductTaskMilestoneRef{ID: productTaskMilestone, Name: "Middle milestone", Status: store.MilestoneStatusInProgress},
			CurrentLane: store.LaneImplementation,
			State:       store.TaskStateEscalated,
			AttemptCount: 2,
			AttemptCap:   store.DefaultAttemptCap,
		},
		{
			TaskID:      productTaskRowOnMilep,
			Title:       "A task on a milepebble of the cut milestone",
			CreatedAt:   time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
			Milestone:   store.ProductTaskMilestoneRef{ID: productTaskMilestone, Name: "Middle milestone", Status: store.MilestoneStatusInProgress},
			Milepebble:  &milepebble,
			CurrentLane: store.LaneTesting,
			State:       store.TaskStateActive,
			AttemptCount: 1,
			AttemptCap:   store.DefaultAttemptCap,
		},
		{
			TaskID:      productTaskRowNewest,
			Title:       "A task on the newest milestone",
			CreatedAt:   time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
			Milestone:   store.ProductTaskMilestoneRef{ID: productTaskNewestMilestone, Name: "Newest milestone", Status: store.MilestoneStatusInProgress},
			CurrentLane: store.LaneDone,
			State:       store.TaskStateActive,
			AttemptCount: 0,
			AttemptCap:   store.DefaultAttemptCap,
		},
		{
			TaskID:      productTaskRowUncut,
			Title:       "A task on the uncut oldest milestone",
			CreatedAt:   time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
			Milestone:   store.ProductTaskMilestoneRef{ID: productTaskOldestMilestone, Name: "Oldest milestone", Status: store.MilestoneStatusInProgress},
			CurrentLane: store.LaneScaffold,
			State:       store.TaskStateActive,
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
		name          string
		mutate        func(*store.ProductTaskRow)
		wantClaim     string
		wantLease     string
		wantBadges    []string
		absentBadges  []string
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
			name:          "an unclaimed row carries neither and claims no lease",
			wantClaim:     "",
			wantLease:     "",
			absentBadges:  []string{`data-krill="task-badge-claimed"`, `data-krill="task-badge-lease-expired"`},
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

	stripped := stripBadges(body)
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
	assert.Contains(t, body, "htmx:afterSwap",
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
