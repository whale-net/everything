// Wire-driven coverage for the Tasks page's own two filters -- the lane
// select and the "Only stuck" checkbox -- and for the three states that
// must never be confused when they match nothing (FR 61d7fb7b).
//
// Every case mounts the real registrations through productTaskFilterMux and
// asserts against served markup or against the parameters the store was
// actually asked for, never against the view model: a filter that is built
// correctly and never reaches the read passes a view-model test and fails
// an operator's.
package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// filteringProductTasks is the read layer's own behaviour, stood in for:
// it applies the same membership the real store applies for
// ListProductTasksParams.Lane and .OnlyStuck, rather than the UI filtering
// rows itself.
//
// It exists so a filter change can be asserted END TO END -- the swapped
// region shows only matching rows -- rather than only asserting that the
// right parameters were passed. A UI that passed them and then re-filtered
// the rows would also get the rows right here, so the complementary case
// (TestTasksOnlyStuckIsNotReDerivedInTheUI) pins that the UI does NOT
// filter: given a row the store would have excluded, it still renders.
type filteringProductTasks struct {
	store.TaskStore

	rows  []store.ProductTaskRow
	total int
	err   error

	listed  []store.ListProductTasksParams
	counted []store.ListProductTasksParams
}

func (s *filteringProductTasks) ListProductTasks(_ context.Context, params store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	s.listed = append(s.listed, params)
	if s.err != nil {
		return store.Page[store.ProductTaskRow]{}, s.err
	}
	return store.Page[store.ProductTaskRow]{Items: filterRows(s.rows, params)}, nil
}

// filterRows is the fixture's membership for one filter set. It is a
// function rather than inline so the count can use it WITHOUT going back
// through ListProductTasks -- a count that re-recorded the list would make
// "exactly one list read per request" see two, and hide which read it was.
func filterRows(rows []store.ProductTaskRow, params store.ListProductTasksParams) []store.ProductTaskRow {
	kept := make([]store.ProductTaskRow, 0, len(rows))
	for _, row := range rows {
		if params.Lane != nil && row.CurrentLane != *params.Lane {
			continue
		}
		if params.OnlyStuck && !isStuckRow(row) {
			continue
		}
		kept = append(kept, row)
	}
	return kept
}

func (s *filteringProductTasks) CountProductTasks(_ context.Context, params store.ListProductTasksParams) (int, error) {
	s.counted = append(s.counted, params)
	if s.err != nil {
		return 0, s.err
	}
	return len(filterRows(s.rows, params)), nil
}

// isStuckRow is the store's own only-stuck membership, restated for the
// fixture: an expired lease, at the attempt cap, escalated, or cancelled.
// The real predicate is store's and is pinned there; this only has to agree
// with it well enough to give the filter something to exclude.
func isStuckRow(row store.ProductTaskRow) bool {
	now := time.Now()
	if row.ClaimID != nil && row.LeaseExpiresAt != nil && !row.LeaseExpiresAt.After(now) {
		return true
	}
	if row.AttemptCount > 0 && row.AttemptCount >= row.AttemptCap {
		return true
	}
	return row.State == store.TaskStateEscalated || row.CancelledAt != nil
}

// The chrome reads the shell makes on every page it renders.
func (filteringProductTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}
func (filteringProductTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}
func (filteringProductTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}
func (filteringProductTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}
func (filteringProductTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}
func (filteringProductTasks) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}
func (filteringProductTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID}, nil
}

// filterRowFixture is one task per canonical lane. Exactly one of them is
// stuck -- the Scaffold one, escalated -- so "only stuck" and "lane
// Scaffold" narrow to different answers and a filter pair cannot be
// satisfied by the same row in both halves.
func filterRowFixture() []store.ProductTaskRow {
	out := make([]store.ProductTaskRow, 0, len(store.CanonicalLaneOrder))
	for _, lane := range store.CanonicalLaneOrder {
		out = append(out, store.ProductTaskRow{
			TaskID:      uuid.New(),
			Title:       "A task in " + string(lane),
			CurrentLane: lane,
			State:       store.TaskStateActive,
			AttemptCap:  store.DefaultAttemptCap,
		})
	}
	out[0].State = store.TaskStateEscalated
	return out
}

// productTaskFilterMux mounts the real shell routes against a task store
// that applies the filters itself. It mirrors productTaskMux rather than
// widening it, so the fixture it takes is stated here and the dependency's
// own helper keeps its narrower type.
func productTaskFilterMux(t *testing.T, tasks *filteringProductTasks) *http.ServeMux {
	t.Helper()

	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products: []store.Product{
			{ID: productTaskProduct, Name: "krill"},
			{ID: productTaskOtherProduct, Name: "another product"},
		},
		listing: productTaskListing(),
	}
	app.tasks = tasks
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// laneRowIDs is the lane each fixture row sits in, so an assertion about
// which rows a filter kept can say "the Testing one" rather than hard-coding
// a uuid it would have to re-derive.
func laneRowIDs(rows []store.ProductTaskRow, lane store.Lane) []string {
	out := []string{}
	for _, row := range rows {
		if row.CurrentLane == lane {
			out = append(out, row.TaskID.String())
		}
	}
	return out
}

// TestTasksFilterControlOffersEveryCanonicalLane pins the lane select's
// options against the STORE's set rather than a UI copy of it: "Any lane"
// first, then the five canonical lanes in store.CanonicalLaneOrder's own
// order.
//
// The "Any lane" option submitting an empty value is load-bearing. It has
// to read as "no lane filter" rather than as a lane the parser refuses, or
// clearing the filter would 400 the page -- and it is the only way an
// operator can return to every lane without hand-editing the URL.
func TestTasksFilterControlOffersEveryCanonicalLane(t *testing.T) {
	tasks := &filteringProductTasks{rows: filterRowFixture()}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL(""))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()

	any := strings.Index(body, `data-krill-lane=""`)
	require.NotEqual(t, -1, any, "the select offers no lane-free option: %s", body)
	previous := any
	for _, lane := range store.CanonicalLaneOrder {
		at := strings.Index(body, `data-krill-lane="`+string(lane)+`"`)
		require.NotEqual(t, -1, at, "the select does not offer lane %q", lane)
		assert.Less(t, previous, at, "lane %q is offered out of the store's canonical order", lane)
		previous = at
	}

	// And the lane-free option is what an unfiltered URL actually submits:
	// parseProductTaskScope must read it as no lane, never as a bad one.
	tasks2 := &recordingProductTasks{}
	mux2 := productTaskMux(t, tasks2, productTaskListing(), nil)
	require.Equal(t, http.StatusOK, fetch(t, mux2, productTaskTasksURL("lane=")).Code,
		"an empty lane value is a cleared filter, not a malformed one")
}

// TestTasksLaneFilterReachesTheStore pins the filter's whole path: the URL's
// lane arrives at the LIST and the COUNT, and only that lane's rows render.
//
// Both halves matter. A lane that reached the list but not the count would
// make "Showing X of Y" describe a different set than the rows above it;
// one that reached neither would leave the control a decoration that swaps
// the region and changes nothing.
func TestTasksLaneFilterReachesTheStore(t *testing.T) {
	fixture := filterRowFixture()
	tasks := &filteringProductTasks{rows: fixture}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("lane=Testing"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, tasks.listed, 1)
	require.NotNil(t, tasks.listed[0].Lane, "the URL's lane reached the read")
	assert.Equal(t, store.LaneTesting, *tasks.listed[0].Lane)
	require.Len(t, tasks.counted, 1)
	assert.Equal(t, tasks.listed[0], tasks.counted[0],
		"the count is read with the identical params the rows were")

	assert.Equal(t, laneRowIDs(fixture, store.LaneTesting), taskRowIDsIn(rec.Body.String()),
		"only the filtered lane's rows render")
}

// TestTasksOnlyStuckReachesTheStore is the same path for the checkbox.
func TestTasksOnlyStuckReachesTheStore(t *testing.T) {
	fixture := filterRowFixture()
	tasks := &filteringProductTasks{rows: fixture}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("only_stuck=true"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, tasks.listed, 1)
	assert.True(t, tasks.listed[0].OnlyStuck, "only_stuck reached the read")
	require.Len(t, tasks.counted, 1)
	assert.Equal(t, tasks.listed[0], tasks.counted[0])

	assert.Equal(t, laneRowIDs(fixture, store.LaneScaffold), taskRowIDsIn(rec.Body.String()),
		"only the stuck task renders")
}

// TestTasksOnlyStuckIsNotReDerivedInTheUI is the negative half: the UI does
// NOT filter rows itself.
//
// The store owns the only-stuck predicate, and a UI that re-derived it
// would disagree with the store the moment the store's definition grew a
// case -- and the count, which the store answers for its own predicate,
// would then describe a different set than the rows. So the UI is handed a
// row the store would have EXCLUDED, with only_stuck set, and it still
// renders: the UI passed the filter and stopped there.
func TestTasksOnlyStuckIsNotReDerivedInTheUI(t *testing.T) {
	notStuck := filterRowFixture()[3] // Validation, active, uncapped, unclaimed
	require.False(t, isStuckRow(notStuck), "the fixture row must be one the store would exclude")

	// The store here returns every row regardless of the filter -- the whole
// point is that the UI must not second-guess what it handed back.
	tasks := &recordingProductTasks{rows: []store.ProductTaskRow{notStuck}, total: 1}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("only_stuck=true"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, tasks.listed, 1)
	assert.True(t, tasks.listed[0].OnlyStuck, "the filter reached the store")
	assert.Equal(t, []string{notStuck.TaskID.String()}, taskRowIDsIn(rec.Body.String()),
		"a row the store returned renders even though the UI could have guessed it was not stuck")
}

// TestTasksFiltersAndScopeAreOneFormWithOneApply is the structural claim
// FR 61d7fb7b's filters rest on: the lane select and the only-stuck checkbox
// live INSIDE the shared scope form, so scope + lane + only-stuck are a
// single plain GET submitted by a single button.
//
// The alternative -- a second form beside the scope control -- is what the
// markup would have to drift away from, because each form would then have
// to re-submit the other's state as hidden fields, and a field one of them
// forgot was exactly the bug this shape exists to prevent. So this asserts
// the shape rather than the behaviour: one form in the region, one Apply,
// and both filter controls inside that form rather than beside it.
//
// A page-wide count would be wrong here -- the shell's own chrome renders
// its forms (the product switcher is one), so the count is scoped to the
// tasks region.
func TestTasksFiltersAndScopeAreOneFormWithOneApply(t *testing.T) {
	tasks := &filteringProductTasks{rows: filterRowFixture()}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL(""))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	region := regionHTML(t, rec.Body.String())

	assert.Equal(t, 1, strings.Count(region, "<form"),
		"the scope and both filters are one form; a second one would have to copy the other's state")
	assert.Equal(t, 1, strings.Count(region, `data-krill="scope-apply"`),
		"one Apply submits every filter at once")

	// And both controls are inside that one form, not beside it: the form's
	// own markup is the submission, so anything outside it is state the
	// operator can set and the server never sees.
	form := region[strings.Index(region, "<form"):]
	form = form[:strings.Index(form, "</form>")]
	for _, control := range []string{
		`data-krill="lane-filter-select"`,
		`data-krill="only-stuck-filter"`,
		`data-krill="scope-mode"`,
	} {
		assert.Contains(t, form, control,
			"%s must be a control the scope form submits, not a decoration beside it", control)
	}

	// Nothing is carried as a hidden field on the Tasks page: with the
	// controls real, a hidden carrier would be a second source of truth for
	// the same filter, and the two could disagree.
	assert.NotContains(t, region, `data-krill="scope-carried-lane"`,
		"the Tasks page has a real lane control, so it has no hidden lane carrier")
	assert.NotContains(t, region, `data-krill="scope-carried-only-stuck"`,
		"the Tasks page has a real only-stuck control, so it has no hidden carrier")
}

// TestTasksFilterChangeSwapsTheRegionInPlace drives the half of FR 61d7fb7b
// that only htmx can show: changing a filter issues an hx-get answered with
// a fragment that replaces the whole region.
//
// htmx does not swap on a non-2xx, so a filter change answered 400 or 404
// would leave the operator clicking a control that appears to do nothing.
// Asserting the fragment is what pins the two halves together -- the
// request the control makes, and the region it gets back.
func TestTasksFilterChangeSwapsTheRegionInPlace(t *testing.T) {
	fixture := filterRowFixture()
	tasks := &filteringProductTasks{rows: fixture}
	mux := productTaskFilterMux(t, tasks)

	rec := htmxGet(mux, productTaskTasksURL("lane=Done"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	// The fragment keeps the region's own anchor, or the swap deletes the
	// very target the next filter change has to hit.
	assert.Contains(t, body, `id="krill-product-tasks"`,
		"the swapped fragment keeps the region's own id")
	assert.NotContains(t, body, "<html", "an htmx swap gets a fragment, not a whole page")
	assert.Equal(t, laneRowIDs(fixture, store.LaneDone), taskRowIDsIn(body),
		"the swapped region shows only the newly-filtered rows")
	// The control comes back marked for the NEW filter, so the select the
	// operator is about to touch again shows what the URL now names.
	assert.Equal(t, "Done", selectedOption(t, body, `data-krill="lane-filter-select"`))
}

// TestTasksFilterSurvivesAReload pins the URL rule: a lane filter and a
// only-stuck filter survive a reload, together with the scope they were
// applied within.
//
// This is the plain-GET half. The control could submit `lane` and drop
// `scope` or `container_id`, and the htmx path would still look right
// because it carries the same query -- only a reload would reveal it, by
// landing on a different set of rows than the operator was looking at.
func TestTasksFilterSurvivesAReload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  store.ProductTaskScope
		lane  *store.Lane
		stuck bool
	}{
		{
			name:  "a lane filter over a milestone",
			query: "scope=milestone&container_id=" + productTaskMilestone.String() + "&lane=Done",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: productTaskMilestone},
			lane:  lanePtr(store.LaneDone),
		},
		{
			name:  "only-stuck over a milepebble",
			query: "scope=milepebble&container_id=" + productTaskMilepebble.String() + "&only_stuck=true",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: productTaskMilepebble},
			stuck: true,
		},
		{
			name:  "both over the product-wide default",
			query: "lane=Testing&only_stuck=true",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
			lane:  lanePtr(store.LaneTesting),
			stuck: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &filteringProductTasks{rows: filterRowFixture()}
			mux := productTaskFilterMux(t, tasks)

			// The reload: the same URL, served again as a fresh full page
			// with no browser state behind it at all.
			rec := fetch(t, mux, productTaskTasksURL(tc.query))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			require.Len(t, tasks.listed, 1, "a reload issues the same read")
			assert.Equal(t, tc.want, tasks.listed[0].Scope, "the scope survived the reload")
			assert.Equal(t, tc.lane, tasks.listed[0].Lane, "the lane survived the reload")
			assert.Equal(t, tc.stuck, tasks.listed[0].OnlyStuck, "only-stuck survived the reload")

			// And the markup agrees with the URL, so a shared link shows the
			// same rows AND the same controls.
			body := rec.Body.String()
			if tc.lane != nil {
				assert.Equal(t, string(*tc.lane), selectedOption(t, body, `data-krill="lane-filter-select"`))
			}
			if tc.stuck {
				assert.Contains(t, checkedBoxOf(body), "checked")
			}
		})
	}
}

func lanePtr(lane store.Lane) *store.Lane { return &lane }

// TestTasksEmptyStateNamesTheActiveFilters pins FR 61d7fb7b's first of the
// three states: no matching row renders an empty state that says WHICH
// filters excluded everything.
//
// "No tasks" alone cannot tell an operator whether the scope holds no work
// or a filter they just set is hiding work that is there. Naming the lane
// and the flag is what tells them which control to change -- and it is the
// only thing on the page that does, because the rows are gone.
func TestTasksEmptyStateNamesTheActiveFilters(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		// rows is what the store holds: chosen so THIS query excludes all of
		// it, which is what makes the case genuinely empty rather than
		// incidentally.
		rows []store.ProductTaskRow
		want []string
	}{
		{
			// The stuck task sits in Scaffold, so only-stuck alone would keep
			// it and the pair does not.
			name:  "a filter pair that matches nothing names both halves",
			query: "lane=Validation&only_stuck=true",
			rows:  filterRowFixture()[:1],
			want:  []string{"lane Validation", "only stuck"},
		},
		{
			name:  "a lane filter alone is named on its own",
			query: "lane=Done",
			rows:  filterRowFixture()[:1],
			want:  []string{"lane Done"},
		},
		{
			name:  "only-stuck alone is named on its own",
			query: "only_stuck=true",
			// A store whose only task is NOT stuck, so only-stuck
			// excludes it while an unfiltered read would have shown it.
			rows: filterRowFixture()[1:],
			want: []string{"only stuck"},
		},
		{
			// With no filter at all the scope is still named: the operator
			// has to know WHICH scope came back empty, or "no tasks" reads
			// as a statement about the whole product.
			name:  "no filter still names the scope",
			query: "scope=milestone&container_id=" + productTaskOldestMilestone.String(),
			rows:  nil,
			want:  []string{"Oldest milestone"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &filteringProductTasks{rows: tc.rows}
			mux := productTaskFilterMux(t, tasks)

			rec := fetch(t, mux, productTaskTasksURL(tc.query))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			body := rec.Body.String()
			require.Contains(t, body, `data-krill="product-tasks-empty"`,
				"the fixture was supposed to match nothing: %s", body)

			empty := emptyStateOf(t, body)
			for _, want := range tc.want {
				assert.Contains(t, empty, want,
					"the empty state names the active filters rather than saying only 'no tasks'")
			}
			// And it is not dressed as a failure.
			assert.NotContains(t, body, `data-krill="product-tasks-error"`)
			assert.NotContains(t, body, `data-krill="product-tasks-scope-error"`)
		})
	}
}

// TestTasksFailedReadIsNotTheEmptyState is the second of the three: a read
// that fails renders the read-failed alert, at 500 for a browser.
//
// This is the state most easily confused with an empty one, because both
// render no rows. The marker and the status are what keep them apart, and
// the status matters most -- an operator who saw a 500 knows to look at the
// logs, while one who saw a plausible empty page concludes the work is done.
func TestTasksFailedReadIsNotTheEmptyState(t *testing.T) {
	tasks := &filteringProductTasks{err: errors.New("the store is down")}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("lane=Done&only_stuck=true"))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-error"`)
	assert.NotContains(t, body, `data-krill="product-tasks-empty"`,
		"a failed read must never render as 'this scope has no tasks'")
	assert.NotContains(t, body, `data-krill="product-tasks-scope-error"`)
}

// TestTasksFailedReadIsInlineForHTMX is the same failure as an htmx swap
// gets: a 200 carrying the alert, because htmx does not swap on a 5xx and
// the operator would be left watching an unchanged page.
func TestTasksFailedReadIsInlineForHTMX(t *testing.T) {
	tasks := &filteringProductTasks{err: errors.New("the store is down")}
	mux := productTaskFilterMux(t, tasks)

	rec := htmxGet(mux, productTaskTasksURL("lane=Done"))

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-error"`)
	assert.NotContains(t, body, `data-krill="product-tasks-empty"`)
}

// TestTasksOutOfProductFilterIsARefusal is the third of the three: a scope
// id from another product is a 404 carrying the refusal, never an empty
// page and never that product's rows.
//
// It is asserted WITH a filter attached, because that is the combination
// this task introduces: the filter form re-submits the scope alongside the
// lane, so a stale container_id now travels with a filter the operator set.
// A refused scope rendering the empty state would read as "your filter
// matched nothing" -- pointing the operator at the wrong control.
func TestTasksOutOfProductFilterIsARefusal(t *testing.T) {
	tasks := &filteringProductTasks{rows: filterRowFixture()}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL(
		"scope=milestone&container_id="+productTaskOtherMilestone.String()+"&lane=Done&only_stuck=true"))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, `data-krill="product-tasks-empty"`,
		"a scope outside the product is a refusal, not an empty answer")
	assert.NotContains(t, body, `data-krill="product-tasks-error"`,
		"and not a read failure either")
	assert.Contains(t, body, "belongs to this product")
	assert.Empty(t, tasks.listed, "the store is never asked for another product's tasks")
}

// TestTasksOutOfProductFilterIsInlineForHTMX is the same refusal as a swap
// gets: 200 with the sentence inline, marked as a SCOPE error so it stays
// distinguishable from a read failure's alert.
func TestTasksOutOfProductFilterIsInlineForHTMX(t *testing.T) {
	tasks := &filteringProductTasks{rows: filterRowFixture()}
	mux := productTaskFilterMux(t, tasks)

	rec := htmxGet(mux, productTaskTasksURL(
		"scope=milestone&container_id="+productTaskOtherMilestone.String()+"&lane=Done"))

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "belongs to this product")
	assert.Contains(t, body, `data-krill="product-tasks-scope-error"`,
		"the refusal carries its own marker, distinct from a failed read")
	assert.NotContains(t, body, `data-krill="product-tasks-error"`)
	assert.NotContains(t, body, `data-krill="product-tasks-empty"`)
	assert.Empty(t, tasks.listed)
}

// TestTasksThreeStatesNeverShareMarkup is the property the three cases
// above each assert one third of: no state carries another's marker, so
// nothing in the markup can be read as two of them at once.
//
// It runs every state on BOTH paths, and the htmx half is the half that
// carries the property. A full-page 404 renders the shell's status page,
// which has no region marker of any kind -- so an absence assertion made
// against it passes for the wrong reason and would stay green if the scope
// error borrowed the read-failed marker. The swap is where all three states
// render as region fragments and could genuinely be confused, so that is
// where "never share markup" has to be proven.
func TestTasksThreeStatesNeverShareMarkup(t *testing.T) {
	const (
		emptyMark = `data-krill="product-tasks-empty"`
		readMark  = `data-krill="product-tasks-error"`
		scopeMark = `data-krill="product-tasks-scope-error"`
	)
	outOfProductQuery := "scope=milestone&container_id=" + productTaskOtherMilestone.String()

	emptyStore := func() *filteringProductTasks { return &filteringProductTasks{rows: filterRowFixture()[:1]} }
	downStore := func() *filteringProductTasks { return &filteringProductTasks{err: errors.New("down")} }
	rowsStore := func() *filteringProductTasks { return &filteringProductTasks{rows: filterRowFixture()} }

	for _, path := range []struct {
		name   string
		get    func(*testing.T, *http.ServeMux, string) *httptest.ResponseRecorder
		status map[string]int
	}{
		{
			name: "full page",
			get:  fetch,
			status: map[string]int{
				"empty": http.StatusOK, "read failed": http.StatusInternalServerError,
				"out of product": http.StatusNotFound,
			},
		},
		{
			// htmx gets 200 for all three -- it does not swap on a non-2xx,
			// so the status cannot be what tells them apart here. Only the
			// markup can, which is why this half is the load-bearing one.
			name: "htmx swap",
			get: func(_ *testing.T, mux *http.ServeMux, url string) *httptest.ResponseRecorder {
				return htmxGet(mux, url)
			},
			status: map[string]int{
				"empty": http.StatusOK, "read failed": http.StatusOK,
				"out of product": http.StatusOK,
			},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				query string
				store func() *filteringProductTasks
				// present is the marker this state DOES carry as a REGION.
				// Empty means this state is not a region on this path at
				// all -- the full-page 404 renders the shell's status page,
				// not the tasks region -- in which case only the absence
				// assertions below say anything, and the status is what
				// distinguishes it.
				present string
				absent  []string
			}{
				{
					name: "empty", query: "lane=Done", store: emptyStore,
					present: emptyMark, absent: []string{readMark, scopeMark},
				},
				{
					name: "read failed", query: "", store: downStore,
					present: readMark, absent: []string{emptyMark, scopeMark},
				},
				{
					// The full-page half of this one is a status page rather
					// than a region, so it is told apart by its 404. The
					// htmx half has no status to do it with -- that is the
					// half the marker exists for, and where the swap could
					// otherwise be handed the read-failed alert.
					name: "out of product", query: outOfProductQuery, store: rowsStore,
					present: scopeMark, absent: []string{emptyMark, readMark},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rec := path.get(t, productTaskFilterMux(t, tc.store()),
						productTaskTasksURL(tc.query))

					require.Equal(t, path.status[tc.name], rec.Code)
					body := rec.Body.String()
					// A full-page 404 is the shell's status page: it must
					// render no REGION marker of any kind, which is the
					// strongest form of "shares no markup" for that state.
					if tc.present != "" && path.name == "full page" && tc.name == "out of product" {
						assert.NotContains(t, body, `data-krill="product-tasks"`,
							"a refused scope renders the status page, not a stale region")
						return
					}
					assert.Contains(t, body, tc.present,
						"state %q must carry its own marker, or this case proves nothing", tc.name)
					for _, other := range tc.absent {
						assert.NotContains(t, body, other,
							"state %q must not carry another state's marker", tc.name)
					}
				})
			}
		})
	}
}

// TestTasksMilepebbleColumnHoldsUnderAFilter pins FR 61d7fb7b's last clause:
// a cut milestone's tasks stay aggregated under a Milepebble column with
// the filter applied to the whole set.
//
// The aggregation is the dependency's (FR f41a352d); what this task adds is
// that narrowing the lane must not split a milepebble's tasks out from under
// their milestone. A row that kept its Milestone cell but lost its
// Milepebble one would be a task that came from nowhere.
func TestTasksMilepebbleColumnHoldsUnderAFilter(t *testing.T) {
	tasks := &filteringProductTasks{rows: productTaskRowFixture()}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL(
		"scope=milestone&container_id="+productTaskMilestone.String()+"&lane=Testing"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()

	rendered := taskRowHTML(t, body, productTaskRowOnMilep.String())
	assert.Contains(t, rendered, "Middle milepebble",
		"a filtered milepebble task still names the milepebble it came from")
	assert.Contains(t, rendered, "Middle milestone",
		"under the milestone it was aggregated under")
	assert.NotContains(t, body, productTaskRowOnMiddle.String(),
		"the lane filter excluded the cut milestone's own task")
}

// TestTasksBoardOffersNoFilters pins FR 61d7fb7b's scope boundary: the
// Board reuses the same parsed scope but does not offer these controls.
//
// FR 61d7fb7b is about the Tasks page, and the Board's own FR (cf000440)
// gives it a different control set. Rendering the lane select here would
// put a Tasks-page control on a page whose spec does not have it, and every
// later Board task would inherit a filter whose markup it never asked for.
func TestTasksBoardOffersNoFilters(t *testing.T) {
	tasks := &filteringProductTasks{rows: filterRowFixture()}
	mux := productTaskFilterMux(t, tasks)

	rec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/board?lane=Testing&only_stuck=true")

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, `data-krill="lane-filter-select"`,
		"the Board reads the lane filter but offers no control to change it")
	assert.NotContains(t, body, `data-krill="only-stuck-filter"`)
	// It still READS them: the same query produces the same filtered rows on
	// both views, which is what makes them two views of one scope.
	require.Len(t, tasks.listed, 1)
	require.NotNil(t, tasks.listed[0].Lane)
	assert.Equal(t, store.LaneTesting, *tasks.listed[0].Lane)
}

// emptyStateOf is the rendered empty state's own markup, so an assertion
// about what it SAYS is made about that element rather than about whatever
// text the rest of the page happens to carry.
func emptyStateOf(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, `data-krill="product-tasks-empty"`)
	require.NotEqual(t, -1, at, "no empty state rendered: %s", body)
	rest := body[at:]
	end := strings.Index(rest, "</div>")
	require.NotEqual(t, -1, end, "the empty state never closes: %s", body)
	return rest[:end]
}