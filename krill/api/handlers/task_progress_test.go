// Unit tests for GetProductTaskProgressHandler (task_progress.go, FR
// 59f664ff-3aa9-4d90-a861-d7758ecee040): GET /products/{id}/task-progress is
// the product-wide per-container task-progress read's HTTP surface. No
// Postgres -- fakeTaskStore (fake_task_store_test.go) and
// fakeProductTaskProductStore (task_product_list_test.go, the same
// package) stand in for store.TaskStore and store.ProductStore; the real
// aggregate query and its incomplete-container predicate are covered by
// krill/store/task_integration_test.go against Postgres.
//
// What this file pins: the wire shape of every ContainerTaskProgressWire
// field -- the five lane counts, the total/done pair a progress bar renders
// from, the cancelled count, and the has_tasks flag that separates "No
// tasks yet" from "0 of 0" -- the query-param validation shared with the
// task list above it, the 404 mapping of store.ErrMilestoneOutsideProduct
// and of an unknown product, and the 500-with-no-store-text rule for any
// other store failure.
package handlers_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// serveProductProgress routes through a real ServeMux so {id} is populated
// exactly the way routes.go's mount populates it.
func serveProductProgress(t *testing.T, tasks *fakeTaskStore, products store.ProductStore, productID, query string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/task-progress", handlers.GetProductTaskProgressHandler(tasks, products))
	rec := httptest.NewRecorder()
	target := "/products/" + productID + "/task-progress"
	if query != "" {
		target += "?" + query
	}
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// progressFixture is the aggregate both this file's tests and the MCP
// twin's parity test are driven with: one milestone row carrying a
// cancellation and a Done task, one milepebble row, and one empty
// container -- every branch the wire shape has.
func progressFixture(productID, milestoneID, milepebbleID, emptyID uuid.UUID) store.ProductTaskProgress {
	return store.ProductTaskProgress{
		ProductID: productID,
		Containers: []store.ContainerTaskProgress{
			{
				Milestone: store.ProductTaskMilestoneRef{
					ID: milestoneID, Name: "M2", Status: store.MilestoneStatusInProgress,
				},
				PerLane:   store.TaskLaneCounts{Scaffold: 1, Testing: 1, Done: 2},
				Cancelled: 1,
			},
			{
				Milestone: store.ProductTaskMilestoneRef{
					ID: milestoneID, Name: "M2", Status: store.MilestoneStatusInProgress,
				},
				Milepebble: &store.ProductTaskMilepebbleRef{
					ID: milepebbleID, Name: "MP1", Status: store.MilestoneStatusInDesign,
				},
				PerLane: store.TaskLaneCounts{Implementation: 1},
			},
			{
				Milestone: store.ProductTaskMilestoneRef{
					ID: emptyID, Name: "M3", Status: store.MilestoneStatusNotStarted,
				},
			},
		},
	}
}

// ── wire shape ──────────────────────────────────────────────────────────────

// TestGetProductTaskProgressHandler_WireShape is the per-container wire
// contract: every figure a progress bar or a swimlane header needs reaches
// the wire, and the milestone_ref keys are the lane names store.Lane uses
// verbatim rather than a free-form map.
func TestGetProductTaskProgressHandler_WireShape(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	milestoneID, milepebbleID, emptyID := uuid.New(), uuid.New(), uuid.New()
	fixture := progressFixture(productID, milestoneID, milepebbleID, emptyID)

	tasks := &fakeTaskStore{summarizeProductTaskProgressResult: fixture}
	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The raw body, not a decoded struct: the point is which JSON keys are
	// present at all, which a round-trip through Go structs cannot show.
	var envelope struct {
		ProductID  string           `json:"product_id"`
		Containers []map[string]any `json:"containers"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, productID.String(), envelope.ProductID)
	require.Len(t, envelope.Containers, 3)

	full := envelope.Containers[0]
	assert.Equal(t, map[string]any{"id": milestoneID.String(), "name": "M2", "status": string(store.MilestoneStatusInProgress)}, full["milestone"])
	assert.NotContains(t, full, "milepebble", "a milestone's own row omits milepebble entirely")
	assert.Equal(t, map[string]any{"scaffold": float64(1), "implementation": float64(0), "testing": float64(1), "validation": float64(0), "done": float64(2)}, full["per_lane"],
		"every lane key is present even at zero -- a stacked bar needs each segment's width")
	assert.Equal(t, float64(4), full["total"], "total is the per-lane sum")
	assert.Equal(t, float64(2), full["done"], "done is the Done lane's own count")
	assert.Equal(t, float64(1), full["cancelled"])
	assert.Equal(t, true, full["has_tasks"])

	pebble := envelope.Containers[1]
	assert.Equal(t, map[string]any{"id": milepebbleID.String(), "name": "MP1", "status": string(store.MilestoneStatusInDesign)}, pebble["milepebble"])
	assert.Equal(t, milestoneID.String(), pebble["milestone"].(map[string]any)["id"],
		"a milepebble row also names its parent milestone")

	empty := envelope.Containers[2]
	assert.Equal(t, float64(0), empty["total"])
	assert.Equal(t, float64(0), empty["done"])
	assert.Equal(t, float64(0), empty["cancelled"])
	assert.Equal(t, false, empty["has_tasks"], "a zero total is what a caller renders as \"No tasks yet\", never \"0 of 0\"")
}

// TestGetProductTaskProgressHandler_TotalIsThePerLaneSum proves the wire's
// total is the breakdown's own arithmetic rather than a second count that
// could drift from it -- the FR's "the per-lane counts and N of M agree"
// rule, on the surface a progress bar actually reads.
func TestGetProductTaskProgressHandler_TotalIsThePerLaneSum(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()

	tasks := &fakeTaskStore{summarizeProductTaskProgressResult: progressFixture(
		productID, uuid.New(), uuid.New(), uuid.New())}
	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp handlers.ProductTaskProgressWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	for _, row := range resp.Containers {
		sum := row.PerLane.Scaffold + row.PerLane.Implementation + row.PerLane.Testing +
			row.PerLane.Validation + row.PerLane.Done
		assert.Equal(t, sum, row.Total, "container %s", row.Milestone.Name)
		assert.Equal(t, row.PerLane.Done, row.Done, "done IS the Done lane's count")
		assert.Equal(t, row.Total > 0, row.HasTasks)
	}
}

// TestGetProductTaskProgressHandler_NoContainers_ReturnsEmptyArray proves a
// product with no in-scope container lists as `[]`, never `null` -- a
// client can index the array without a nil check.
func TestGetProductTaskProgressHandler_NoContainers_ReturnsEmptyArray(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{summarizeProductTaskProgressResult: store.ProductTaskProgress{
		ProductID:  productID,
		Containers: []store.ContainerTaskProgress{},
	}}

	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, fmt.Sprintf(`{"product_id": %q, "containers": []}`, productID.String()), rec.Body.String())
}

// ── param plumbing ───────────────────────────────────────────────────────────

// TestGetProductTaskProgressHandler_DefaultParams pins the default request:
// scope absent means every incomplete container of the product, and the
// product's own scope_id is what the rows are scoped by (LB2).
func TestGetProductTaskProgressHandler_DefaultParams(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{}

	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, productID, tasks.gotSummarizeProductTaskProgressParams.ProductID)
	assert.Equal(t, scopeID, tasks.gotSummarizeProductTaskProgressParams.ScopeID,
		"the read is scoped by the product's own scope_id, never by a caller's session")
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotSummarizeProductTaskProgressParams.Scope)
}

// TestGetProductTaskProgressHandler_ExplicitIncompleteScopeIsTheDefault
// proves scope=incomplete and no scope at all reach the store identically,
// so a progress header and the task list above it describe the same
// containers however each was spelled.
func TestGetProductTaskProgressHandler_ExplicitIncompleteScopeIsTheDefault(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	explicit := &fakeTaskStore{}
	serveProductProgress(t, explicit, existingProduct(productID, scopeID), productID.String(), "scope=incomplete")
	implicit := &fakeTaskStore{}
	serveProductProgress(t, implicit, existingProduct(productID, scopeID), productID.String(), "")

	assert.Equal(t, implicit.gotSummarizeProductTaskProgressParams, explicit.gotSummarizeProductTaskProgressParams)
}

// TestGetProductTaskProgressHandler_AllParamsPlumb proves both
// single-container scopes reach the store with their container_id intact.
func TestGetProductTaskProgressHandler_AllParamsPlumb(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	containerID := uuid.New()

	for _, kind := range []store.ProductTaskScopeKind{store.ProductTaskScopeMilestone, store.ProductTaskScopeMilepebble} {
		t.Run(string(kind), func(t *testing.T) {
			tasks := &fakeTaskStore{}
			query := fmt.Sprintf("scope=%s&container_id=%s", kind, containerID)

			rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(), query)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, store.ProductTaskScope{Kind: kind, ContainerID: containerID}, tasks.gotSummarizeProductTaskProgressParams.Scope)
		})
	}
}

// TestGetProductTaskProgressHandler_ContainerIDIgnoredForIncompleteScope
// proves container_id on the product-wide scope is inert rather than
// silently narrowing the read.
func TestGetProductTaskProgressHandler_ContainerIDIgnoredForIncompleteScope(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{}

	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(),
		"scope=incomplete&container_id="+uuid.New().String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotSummarizeProductTaskProgressParams.Scope)
	assert.Equal(t, uuid.Nil, tasks.gotSummarizeProductTaskProgressParams.Scope.ContainerID)
}

// ── query-param validation ───────────────────────────────────────────────────

// TestGetProductTaskProgressHandler_BadQueryParams_Return400 is the whole
// validation table. Every row is a caller error answered with the same 400
// the task list above it gives, naming the valid values, and never reaching
// the store -- a silent empty aggregate would read as "this product has no
// milestones".
func TestGetProductTaskProgressHandler_BadQueryParams_Return400(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()

	for _, tc := range []struct {
		name    string
		id      string
		query   string
		wantMsg string
	}{
		{name: "unparseable path id", id: "not-a-uuid", wantMsg: "invalid id: must be a UUID"},
		{name: "unknown scope", id: productID.String(), query: "scope=everything", wantMsg: "scope: must be one of incomplete, milestone, milepebble"},
		{name: "milestone scope without container_id", id: productID.String(), query: "scope=milestone", wantMsg: "container_id: required for scope milestone; invalid or missing UUID"},
		{name: "milepebble scope without container_id", id: productID.String(), query: "scope=milepebble", wantMsg: "container_id: required for scope milepebble; invalid or missing UUID"},
		{name: "non-UUID container_id", id: productID.String(), query: "scope=milestone&container_id=nope", wantMsg: "container_id: required for scope milestone; invalid or missing UUID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &fakeTaskStore{}
			rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), tc.id, tc.query)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.JSONEq(t, fmt.Sprintf(`{"error": %q}`, tc.wantMsg), rec.Body.String())
			assert.Equal(t, store.ProductTaskProgressParams{}, tasks.gotSummarizeProductTaskProgressParams,
				"a rejected request must never reach the store")
		})
	}
}

// TestGetProductTaskProgressHandler_BadQueryParamBeatsMissingProduct proves
// the query string is validated before the product is resolved, so a
// caller gets the fixable error first.
func TestGetProductTaskProgressHandler_BadQueryParamBeatsMissingProduct(t *testing.T) {
	tasks := &fakeTaskStore{}
	products := &fakeProductTaskProductStore{getErr: store.ErrNotFound}

	rec := serveProductProgress(t, tasks, products, uuid.New().String(), "scope=nope")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "scope: must be one of")
}

// ── product resolution and error mappings ────────────────────────────────────

// TestGetProductTaskProgressHandler_UnknownProduct_Returns404 proves an
// unknown product id is a distinct 404 rather than an empty aggregate that
// would read as "this product has no milestones".
func TestGetProductTaskProgressHandler_UnknownProduct_Returns404(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := serveProductProgress(t, tasks, &fakeProductTaskProductStore{getErr: store.ErrNotFound}, uuid.New().String(), "")

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "product not found"}`, rec.Body.String())
	assert.Equal(t, store.ProductTaskProgressParams{}, tasks.gotSummarizeProductTaskProgressParams)
}

// TestGetProductTaskProgressHandler_ProductStoreError_Returns500 proves a
// product-lookup failure is a bare 500 that leaks no store text.
func TestGetProductTaskProgressHandler_ProductStoreError_Returns500(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := serveProductProgress(t, tasks, &fakeProductTaskProductStore{getErr: errors.New("boom")}, uuid.New().String(), "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String())
}

// TestGetProductTaskProgressHandler_ContainerOutsideProduct_Returns404 is
// LB1 on this surface: a container the product does not own is a distinct
// 404, not an empty aggregate that would read as "this milestone has no
// work" and not another product's counts.
func TestGetProductTaskProgressHandler_ContainerOutsideProduct_Returns404(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{summarizeProductTaskProgressErr: store.ErrMilestoneOutsideProduct}

	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(),
		"scope=milestone&container_id="+uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "milestone not found in this product"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "containers", "a refusal must never carry a listing")
}

// TestGetProductTaskProgressHandler_StoreFailure_Returns500 proves any other
// store failure is a 500 with no store text -- a failed aggregate read must
// never render as every container being empty.
func TestGetProductTaskProgressHandler_StoreFailure_Returns500(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{summarizeProductTaskProgressErr: errors.New("connection reset by peer")}

	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "connection reset by peer")
}

// TestGetProductTaskProgressHandler_NotFoundProductFromTheReadIsA500
// covers the aggregate's own ErrNotFound (a product whose current row is
// gone between the handler's lookup and the read). It reaches the same
// writeConsoleQueryError default every other non-token store failure does,
// so the handler cannot claim "product not found" for a read it did not
// resolve itself.
func TestGetProductTaskProgressHandler_NotFoundProductFromTheReadIsA500(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{summarizeProductTaskProgressErr: fmt.Errorf("%w: product id %s", store.ErrNotFound, productID)}

	rec := serveProductProgress(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String())
}

// TestGetProductTaskProgressHandler_IsUngated pins FR3's write-only gate on
// this endpoint: it answers without a krill_session_id, unlike every write
// path in this package.
func TestGetProductTaskProgressHandler_IsUngated(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/task-progress", handlers.GetProductTaskProgressHandler(
		&fakeTaskStore{}, existingProduct(productID, scopeID)))
	rec := httptest.NewRecorder()

	// No X-Krill-Session-Id header, and the request still succeeds.
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/task-progress", nil))

	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
