// Wire-driven coverage for the product-wide task read layer in krill/ui
// (product_task_scope.go, product_task_page.go): the scope a Tasks/Board
// URL resolves to, the container check that refuses another product's, and
// the one parameter set the page and its count are read with.
//
// Every case mounts the real registrations through mountShellRoutes rather
// than calling a handler directly, because "which routes serve this" and
// "what the shell renders around it" are part of what is being pinned here.
package main

import (
	"context"
	"errors"
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

// The fixtures are literals rather than uuid.New() so a failure names the
// same ids every run and the expected paths below can be written out.
var (
	// productTaskProduct is the product the Tasks/Board URLs name.
	productTaskProduct = uuid.MustParse("77777777-7777-7777-7777-777777777777")

	// The product's three milestones, named for the position order they
	// occupy rather than for when they happen to be read. slice.
	// ListProductDelivery returns them position-ASCENDING, so this is
	// oldest-first -- which is the opposite end from the "highest
	// position" a no-id mode defaults to (FR 7191dba1), and the reason a
	// fallback that took the listing's head would be caught here.
	productTaskOldestMilestone = uuid.MustParse("88888888-8888-8888-8888-888888888888")
	productTaskMilestone       = uuid.MustParse("bbbb3333-3333-3333-3333-333333333333")
	productTaskNewestMilestone = uuid.MustParse("cccc4444-4444-4444-4444-444444444444")

	// A cut milepebble under each of the two cut milestones, so the
	// milepebble default is pinned to the highest-position milestone that
	// has one rather than to the first milepebble the listing happens to
	// carry.
	productTaskMilepebble       = uuid.MustParse("99999999-9999-9999-9999-999999999999")
	productTaskNewestMilepebble = uuid.MustParse("99998888-8888-8888-8888-888888888888")

	// productTaskShippedMilestone is outside the product-wide all-incomplete
	// scope by its own status, and is the fixture for the rule that it is
	// still offered and still readable when picked explicitly.
	productTaskShippedMilestone = uuid.MustParse("dddd5555-5555-5555-5555-555555555555")

	// productTaskOtherProduct is a second product, whose milestone is the
	// id a cross-product link would carry.
	productTaskOtherProduct   = uuid.MustParse("aaaa1111-1111-1111-1111-111111111111")
	productTaskOtherMilestone = uuid.MustParse("aaaa2222-2222-2222-2222-222222222222")
)

// productTaskListing is the delivery listing the scope resolver reads, in
// the position-ASCENDING order slice.ListProductDelivery produces: an
// uncut oldest milestone, then a cut middle one, then the cut newest one
// that carries the highest position.
func productTaskListing() slice.DeliveryListing {
	return slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{
			{ID: productTaskOldestMilestone, Name: "Oldest milestone", Status: store.MilestoneStatusInProgress},
			{
				ID:   productTaskMilestone,
				Name: "Middle milestone",
				Milepebbles: []slice.MilepebbleListingEntry{
					{ID: productTaskMilepebble, Name: "Middle milepebble", Status: store.MilestoneStatusInDesign},
				},
			},
			{
				ID:   productTaskNewestMilestone,
				Name: "Newest milestone",
				Milepebbles: []slice.MilepebbleListingEntry{
					{ID: productTaskNewestMilepebble, Name: "Newest milepebble", Status: store.MilestoneStatusInDesign},
				},
			},
		},
	}
}

// emptyDeliveryListing is a product with no containers at all, which is
// what makes every mode an ordinary empty result rather than a 404.
func emptyDeliveryListing() slice.DeliveryListing {
	return slice.DeliveryListing{}
}

// recordingProductTasks records every ListProductTasks / CountProductTasks
// call's parameters verbatim, so a test can compare the two byte for byte.
//
// It embeds store.TaskStore rather than implementing it: a method the read
// layer grew and did not record would nil-panic instead of passing
// unnoticed, which is what keeps this fixture honest as the read changes.
type recordingProductTasks struct {
	store.TaskStore

	listed  []store.ListProductTasksParams
	counted []store.ListProductTasksParams

	rows  []store.ProductTaskRow
	total int
	err   error

	// tasks backs the detail route's own reads. Nil for the table cases,
	// which never reach a task detail.
	tasks map[uuid.UUID]store.Task
}

func (s *recordingProductTasks) GetTaskByID(_ context.Context, id uuid.UUID) (store.Task, error) {
	if t, ok := s.tasks[id]; ok {
		return t, nil
	}
	return store.Task{}, store.ErrNotFound
}

func (s *recordingProductTasks) ListDependencies(context.Context, uuid.UUID, uuid.UUID) ([]store.TaskDependency, error) {
	return nil, nil
}

func (s *recordingProductTasks) ListNotesForTask(context.Context, uuid.UUID, uuid.UUID) ([]store.Note, error) {
	return nil, nil
}

func (s *recordingProductTasks) GetClaimByID(context.Context, uuid.UUID) (store.Claim, error) {
	return store.Claim{}, store.ErrNotFound
}

// GetEscalationEventByID is the detail's read behind a task's own
// current_escalation_id. None of this fixture's tasks is escalated, so the
// read is never made; it is declared rather than left to the embedded nil
// interface, which panics rather than refusing.
func (s *recordingProductTasks) GetEscalationEventByID(context.Context, uuid.UUID) (store.EscalationEvent, error) {
	return store.EscalationEvent{}, store.ErrNotFound
}

func (s *recordingProductTasks) ListProductTasks(_ context.Context, params store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	s.listed = append(s.listed, params)
	if s.err != nil {
		return store.Page[store.ProductTaskRow]{}, s.err
	}
	return store.Page[store.ProductTaskRow]{Items: s.rows}, nil
}

func (s *recordingProductTasks) CountProductTasks(_ context.Context, params store.ListProductTasksParams) (int, error) {
	s.counted = append(s.counted, params)
	if s.err != nil {
		return 0, s.err
	}
	return s.total, nil
}

// The two reads the shell's chrome makes on every page it renders, so a
// full-page render does not nil-panic before reaching the region.
func (recordingProductTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

func (recordingProductTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

func (recordingProductTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

func (recordingProductTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}

func (recordingProductTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}

func (recordingProductTasks) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}

func (recordingProductTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID}, nil
}

// productTaskMux mounts the real shell routes against a task store that
// records what it was asked for. listing is what the scope resolver's
// delivery read answers; the other products' milestone is deliberately
// absent from it, which is what makes a cross-product id unresolvable.
func productTaskMux(t *testing.T, tasks *recordingProductTasks, listing slice.DeliveryListing, listingErr error) *http.ServeMux {
	t.Helper()

	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products: []store.Product{
			{ID: productTaskProduct, Name: "krill"},
			{ID: productTaskOtherProduct, Name: "another product"},
		},
		listing:    listing,
		listingErr: listingErr,
	}
	app.tasks = tasks
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// productTaskTasksURL is the Tasks URL for one product with the given raw
// query, written out rather than built from the parameter constants so the
// tests are independent of the names they exercise.
func productTaskTasksURL(query string) string {
	url := "/products/" + productTaskProduct.String() + "/tasks"
	if query != "" {
		url += "?" + query
	}
	return url
}

// htmxGet issues the htmx half of a request -- the one with the header the
// region's own swap control would send.
func htmxGet(mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestProductTaskScopeRefusesAnotherProductsContainer is the LB1 the
// resolver exists for: a milestone id belonging to a different product is
// a 404, never that product's rows and never a plausible empty page.
//
// The case is driven through the real route with a task store that would
// happily return rows, so "no rows appeared" could only come from the
// resolver refusing the id rather than from a fixture that had none.
func TestProductTaskScopeRefusesAnotherProductsContainer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		{
			name:   "milestone scope naming another product's milestone",
			target: productTaskTasksURL("scope=milestone&container_id=" + productTaskOtherMilestone.String()),
		},
		{
			name:   "milepebble scope naming another product's milestone",
			target: productTaskTasksURL("scope=milepebble&container_id=" + productTaskOtherMilestone.String()),
		},
		{
			name:   "milestone scope naming an id no product owns",
			target: productTaskTasksURL("scope=milestone&container_id=cccccccc-0000-0000-0000-000000000000"),
		},
		{
			// The same id under the other mode is as wrong: the mode names
			// what it wants, and answering with the other kind's rows would
			// show tasks the URL did not ask for.
			name:   "milestone scope naming a milepebble",
			target: productTaskTasksURL("scope=milestone&container_id=" + productTaskMilepebble.String()),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &recordingProductTasks{
				rows:  []store.ProductTaskRow{{TaskID: uuid.New(), Title: "a task that must never render"}},
				total: 1,
			}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, tc.target)

			assert.Equal(t, http.StatusNotFound, rec.Code)
			assert.NotContains(t, rec.Body.String(), "a task that must never render")
			assert.Empty(t, tasks.listed, "the store must never be asked for another product's tasks")
			assert.Contains(t, rec.Body.String(), "belongs to this product")
		})
	}
}

// TestProductTaskScopeRefusalIsInlineForHTMX is the same refusal as an
// htmx request gets: a 200 carrying the sentence, because htmx does not
// swap on a non-2xx and a status-coded body would leave the operator
// looking at an unchanged page with no explanation.
func TestProductTaskScopeRefusalIsInlineForHTMX(t *testing.T) {
	tasks := &recordingProductTasks{}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := htmxGet(mux, productTaskTasksURL("scope=milestone&container_id="+productTaskOtherMilestone.String()))

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "belongs to this product")
	// The fragment has to keep the region's own id, or htmx's outerHTML
	// swap removes the swap target and every later refresh finds none.
	assert.Contains(t, body, `id="`+pages.ProductTasksAnchor+`"`)
	assert.Empty(t, tasks.listed)
}

// TestProductTaskScopeDefaultsAModeWithNoID pins the fallback rule: a
// single-container mode with no id selects the product's own highest-
// position milestone (FR 7191dba1: "the first milestone in the Milestones
// table order (highest position)"), and a milepebble mode the highest-
// position milepebble of those.
//
// The fixture lists three milestones oldest-first, because
// slice.ListProductDelivery returns position-ASC while both the spec's
// default and the store's own read (ORDER BY m.position DESC) and the
// board's swimlane order take the highest position first. So the expected
// default here is the listing's LAST milestone, not its first -- which is
// exactly the confusion this test exists to prevent.
func TestProductTaskScopeDefaultsAModeWithNoID(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  store.ProductTaskScope
	}{
		{
			name:  "milestone mode with no id takes the highest position",
			query: "scope=milestone",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: productTaskNewestMilestone},
		},
		{
			name:  "milepebble mode with no id takes the highest-position milepebble",
			query: "scope=milepebble",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: productTaskNewestMilepebble},
		},
		{
			name:  "no mode at all is the product-wide incomplete scope",
			query: "",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		},
		{
			name:  "the incomplete scope named explicitly",
			query: "scope=incomplete",
			want:  store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &recordingProductTasks{total: 3}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(tc.query))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			require.Len(t, tasks.listed, 1, "exactly one list read per request")
			assert.Equal(t, tc.want, tasks.listed[0].Scope)
			// The product-wide scope reads no listing, so a product with no
			// containers at all still answers there rather than 404ing.
			assert.Equal(t, productTaskProduct, tasks.listed[0].ProductID)
			assert.Equal(t, chromeScopeID, tasks.listed[0].ScopeID)
		})
	}
}

// TestProductTaskReadUsesOneFilterSetForPageAndCount is the rule behind
// "Showing X of Y tasks": the count is the store's own answer for the same
// filters, read from the same parameter value. Two separately-built
// parameter sets are exactly how the total and the rows come to describe
// different scopes.
//
// The comparison is over every field of the params struct, not just the
// scope, so a filter that reached the list and not the count -- or the
// reverse -- fails here too.
func TestProductTaskReadUsesOneFilterSetForPageAndCount(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		lane  store.Lane // zero means the URL names no lane
	}{
		{name: "product-wide scope, unfiltered", query: ""},
		{name: "with a lane filter", query: "lane=Testing", lane: store.LaneTesting},
		{name: "with only-stuck", query: "only_stuck=true"},
		{name: "milestone scope", query: "scope=milestone&container_id=" + productTaskMilestone.String()},
		{name: "milepebble scope, lane and only-stuck", query: "scope=milepebble&container_id=" + productTaskMilepebble.String() + "&lane=Done&only_stuck=1", lane: store.LaneDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &recordingProductTasks{total: 7}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(tc.query))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			require.Len(t, tasks.listed, 1)
			require.Len(t, tasks.counted, 1)
			assert.Equal(t, tasks.listed[0], tasks.counted[0],
				"the count must be read with the identical params the rows were")
			// The store's own filter set is what a continuation token is
			// bound to, so this is the equality that keeps paging honest.
			assert.Equal(t, tasks.listed[0].FilterSet(), tasks.counted[0].FilterSet())

			if tc.lane == "" {
				assert.Nil(t, tasks.listed[0].Lane, "an absent lane is every lane, never none")
			} else {
				require.NotNil(t, tasks.listed[0].Lane)
				assert.Equal(t, tc.lane, *tasks.listed[0].Lane)
			}
		})
	}
}

// TestProductTaskScopeOffersContainersHighestPositionFirst pins the
// order the region's container options are offered in, which is the same
// order the no-id default resolves within: highest position first.
//
// The two have to agree, or a mode opened with no id would resolve to one
// milestone while the control lists another first -- so a shared link
// landing on the newest milestone would show the newest milestone selected
// somewhere other than the top of the list. The listing arrives
// position-ASC, so the options are the reverse of it.
func TestProductTaskScopeOffersContainersHighestPositionFirst(t *testing.T) {
	tasks := &recordingProductTasks{}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milestone"))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	body := rec.Body.String()
	newest := strings.Index(body, `data-krill-container-id="`+productTaskNewestMilestone.String()+`"`)
	middle := strings.Index(body, `data-krill-container-id="`+productTaskMilestone.String()+`"`)
	oldest := strings.Index(body, `data-krill-container-id="`+productTaskOldestMilestone.String()+`"`)
	require.NotEqual(t, -1, newest, "every milestone is offered: %s", body)
	require.NotEqual(t, -1, middle)
	require.NotEqual(t, -1, oldest)
	assert.Less(t, newest, middle, "the highest position is offered first")
	assert.Less(t, middle, oldest)
}

// TestProductTaskRegionReportsScopeAndTotal checks the region says what it
// read: the resolved scope by name, the total from the count read, and the
// two views marking themselves as different views of the same scope.
func TestProductTaskRegionReportsScopeAndTotal(t *testing.T) {
	tasks := &recordingProductTasks{total: 9}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	tasksRec := fetch(t, mux, productTaskTasksURL("scope=milestone&container_id="+productTaskMilestone.String()))
	boardRec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/board?scope=milestone&container_id="+productTaskMilestone.String())

	for _, tc := range []struct {
		view string
		rec  *httptest.ResponseRecorder
	}{
		{view: "Tasks", rec: tasksRec},
		{view: "Board", rec: boardRec},
	} {
		t.Run(tc.view, func(t *testing.T) {
			require.Equal(t, http.StatusOK, tc.rec.Code)
			body := tc.rec.Body.String()
			assert.Contains(t, body, `data-krill-view="`+tc.view+`"`)
			assert.Contains(t, body, "Middle milestone", "the resolved scope is named")
			assert.Contains(t, body, ">9<", "the total from the count read is shown")
		})
	}
}

// TestProductTaskEmptyScopeIsNotAFailure: a product with no containers has
// nothing to scope to, which is an ordinary answer rather than a 404 and
// certainly rather than an error alert.
func TestProductTaskEmptyScopeIsNotAFailure(t *testing.T) {
	tasks := &recordingProductTasks{}
	mux := productTaskMux(t, tasks, slice.DeliveryListing{}, nil)

	rec := fetch(t, mux, productTaskTasksURL("scope=milestone"))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.NotContains(t, body, `data-krill="product-tasks-error"`)
	assert.Contains(t, body, `data-krill="product-tasks-empty"`)
	assert.Empty(t, tasks.listed, "there is no container to read tasks for")
}

// TestProductTaskInvalidScopeIsA400: a filter the URL spelled wrongly is
// the operator's to fix, refused before any read rather than read as
// "nothing matched".
func TestProductTaskInvalidScopeIsA400(t *testing.T) {
	for _, query := range []string{
		"scope=everything",
		"scope=milestone&container_id=not-a-uuid",
		"lane=Review",
		"only_stuck=maybe",
		"page_size=-1",
	} {
		t.Run(query, func(t *testing.T) {
			tasks := &recordingProductTasks{}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(query))

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Empty(t, tasks.listed)
		})
	}
}

// TestProductTaskFailedReadIsNotAnEmptyPage: a store that fails renders
// the failure, never a page whose "no tasks" would be read as an answer.
func TestProductTaskFailedReadIsNotAnEmptyPage(t *testing.T) {
	tasks := &recordingProductTasks{err: errors.New("the store is down")}
	mux := productTaskMux(t, tasks, productTaskListing(), nil)

	rec := fetch(t, mux, productTaskTasksURL(""))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-error"`)
	assert.NotContains(t, body, `data-krill="product-tasks-empty"`)
}

// TestProductTaskUnreadableListingIsA500: the resolver could not check the
// container, which is a failure to read rather than a refusal of the id.
func TestProductTaskUnreadableListingIsA500(t *testing.T) {
	tasks := &recordingProductTasks{}
	mux := productTaskMux(t, tasks, slice.DeliveryListing{}, errors.New("listing unavailable"))

	rec := fetch(t, mux, productTaskTasksURL("scope=milestone"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, tasks.listed)
}

// TestProductTaskIgnoresAContainerUnderTheProductWideScope: a container
// id the product-wide scope cannot honour is inert, exactly as the store,
// the api and the MCP tool all treat it.
//
// The store's contract is explicit that ContainerID is "Ignored for
// ProductTaskScopeIncomplete"; the api's parser returns before it reads
// the parameter, and count_product_tasks is pinned to the same. So the
// console answers the identical query string with the same product-wide
// read, rather than a 400 no other surface would give -- which is also
// what an operator switching back to "All incomplete milestones" from a
// link carrying a selected id actually needs.
func TestProductTaskIgnoresAContainerUnderTheProductWideScope(t *testing.T) {
	for _, query := range []string{
		"scope=incomplete&container_id=" + productTaskMilestone.String(),
		"container_id=" + productTaskMilestone.String(),
		// A mistyped id is equally inert: the product-wide scope never
		// reads it, so there is nothing to misread.
		"scope=incomplete&container_id=not-a-uuid",
	} {
		t.Run(query, func(t *testing.T) {
			tasks := &recordingProductTasks{total: 4}
			mux := productTaskMux(t, tasks, productTaskListing(), nil)

			rec := fetch(t, mux, productTaskTasksURL(query))

			require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
			require.Len(t, tasks.listed, 1)
			// The read carries no container at all, which is what makes
			// the id inert rather than a filter the store applied.
			assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.listed[0].Scope)
			assert.Equal(t, uuid.Nil, tasks.listed[0].Scope.ContainerID)
		})
	}
}
