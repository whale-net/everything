// Unit tests for ListProductTasksHandler (task_product_list.go, FR
// cfcd1104-b015-4ffd-a07a-abb7c6e435d3): GET /products/{id}/tasks is the
// product-wide paged task read's HTTP surface. No Postgres -- fakeTaskStore
// (fake_task_store_test.go) and fakeProductTaskProductStore (below) stand in
// for store.TaskStore and store.ProductStore; the real query, its
// keyset paging and its incomplete-container predicate are covered by
// krill/store/task_integration_test.go against Postgres.
//
// What this file pins: the wire shape of every ProductTaskRow field
// (FR2), the query-param validation that answers a bad request with a 400
// naming the valid values rather than a silent empty page, the
// container_id-required rule for the two single-container scopes, the
// 404 mapping of store.ErrMilestoneOutsideProduct (LB1), and the 400
// mapping of a continuation token presented under a different filter set
// (FR3) -- the three-way writeConsoleQueryError mapping this read relies on.
package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// fakeProductTaskProductStore backs this file: unlike
// fake_entity_store_test.go's fakeProductStore (whose GetCurrentByID always
// returns ErrNotFound), this read resolves the target Product's scope_id
// (LB2) before it reaches the store, so the fake needs a configurable
// success path too -- mirrors milestone_delivery_test.go's
// fakeProductDeliveryProductStore precedent.
type fakeProductTaskProductStore struct {
	product store.Product
	getErr  error
	gotID   uuid.UUID
}

func (f *fakeProductTaskProductStore) Create(context.Context, uuid.UUID, string, string) (store.Product, error) {
	return store.Product{}, errors.New("not used by task_product_list_test.go")
}

func (f *fakeProductTaskProductStore) GetCurrentByID(_ context.Context, id uuid.UUID) (store.Product, error) {
	f.gotID = id
	if f.getErr != nil {
		return store.Product{}, f.getErr
	}
	return f.product, nil
}

func (f *fakeProductTaskProductStore) ListCurrentByScope(context.Context, uuid.UUID) ([]store.Product, error) {
	return nil, errors.New("not used by task_product_list_test.go")
}

var _ store.ProductStore = (*fakeProductTaskProductStore)(nil)

// serveProductTasks routes through a real ServeMux so {id} is populated the
// same way routes.go's mount populates it, and the request's query string is
// the one under test.
func serveProductTasks(t *testing.T, tasks *fakeTaskStore, products store.ProductStore, productID, query string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks", handlers.ListProductTasksHandler(tasks, products))
	rec := httptest.NewRecorder()
	target := "/products/" + productID + "/tasks"
	if query != "" {
		target += "?" + query
	}
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// existingProduct returns a fake product store holding product, plus the
// ids a test needs to build rows against.
func existingProduct(productID, scopeID uuid.UUID) *fakeProductTaskProductStore {
	return &fakeProductTaskProductStore{product: store.Product{ID: productID, ScopeID: scopeID, Name: "krill"}}
}

// ── wire shape ──────────────────────────────────────────────────────────────

// TestListProductTasksHandler_WireShape is FR2's per-row contract: every
// field a console needs reaches the wire, in store order, with the optional
// ones present exactly when the row carries them.
func TestListProductTasksHandler_WireShape(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	milestoneID, milepebbleID := uuid.New(), uuid.New()
	escalatedID, claimID := uuid.New(), uuid.New()
	leaseExpiry := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tasks := &fakeTaskStore{listProductTasksResult: store.Page[store.ProductTaskRow]{
		Items: []store.ProductTaskRow{
			{
				TaskID:     escalatedID,
				Title:      "an escalated, claimed, cut task",
				CreatedAt:  createdAt,
				Milestone:  store.ProductTaskMilestoneRef{ID: milestoneID, Name: "M3", Status: store.MilestoneStatusInProgress},
				Milepebble: &store.ProductTaskMilepebbleRef{ID: milepebbleID, Name: "M3.1", Status: store.MilestoneStatusInDesign},
				CurrentLane: store.LaneValidation,
				State:       store.TaskStateEscalated,
				EscalationReason: func() *store.EscalationReason {
					r := store.EscalationReasonAttemptCap
					return &r
				}(),
				AttemptCount:   store.DefaultAttemptCap,
				AttemptCap:     store.DefaultAttemptCap,
				LeaseExpiresAt: &leaseExpiry,
				ClaimID:        &claimID,
			},
			{
				// A plain active, unclaimed, uncut, uncancelled task: every
				// optional field must be absent, never a literal null.
				TaskID:    uuid.New(),
				Title:     "a plain task",
				CreatedAt: createdAt,
				Milestone: store.ProductTaskMilestoneRef{ID: milestoneID, Name: "M3", Status: store.MilestoneStatusInProgress},
				Milepebble: nil,
				CurrentLane: store.LaneScaffold,
				State:       store.TaskStateActive,
				AttemptCount: 1,
				AttemptCap:   store.DefaultAttemptCap,
			},
		},
		NextToken: "cursor-2",
	}}

	rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The raw body, not a decoded struct: the point is which JSON keys are
	// present at all, which a round-trip through Go structs cannot show.
	var envelope struct {
		Tasks     []map[string]any `json:"tasks"`
		NextToken string           `json:"next_token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "cursor-2", envelope.NextToken)
	require.Len(t, envelope.Tasks, 2)

	full := envelope.Tasks[0]
	assert.Equal(t, escalatedID.String(), full["task_id"])
	assert.Equal(t, "an escalated, claimed, cut task", full["title"])
	assert.Equal(t, map[string]any{"id": milestoneID.String(), "name": "M3", "status": string(store.MilestoneStatusInProgress)}, full["milestone"])
	assert.Equal(t, map[string]any{"id": milepebbleID.String(), "name": "M3.1", "status": string(store.MilestoneStatusInDesign)}, full["milepebble"])
	assert.Equal(t, string(store.LaneValidation), full["current_lane"])
	assert.Equal(t, string(store.TaskStateEscalated), full["state"])
	assert.Equal(t, string(store.EscalationReasonAttemptCap), full["escalation_reason"])
	assert.NotContains(t, full, "cancelled_at", "an uncancelled task omits cancelled_at entirely")
	assert.Equal(t, float64(store.DefaultAttemptCap), full["attempt_count"])
	assert.Equal(t, float64(store.DefaultAttemptCap), full["attempt_cap"])
	assert.Equal(t, leaseExpiry, mustParseTime(t, full["lease_expires_at"]))
	assert.Equal(t, claimID.String(), full["claim_id"])

	bare := envelope.Tasks[1]
	assert.NotContains(t, bare, "milepebble", "an uncut milestone's task omits milepebble entirely")
	assert.NotContains(t, bare, "escalation_reason", "a non-escalated task omits escalation_reason entirely")
	assert.NotContains(t, bare, "lease_expires_at", "an unclaimed task omits lease_expires_at entirely")
	assert.NotContains(t, bare, "claim_id", "an unclaimed task omits claim_id entirely")
	assert.Equal(t, string(store.LaneScaffold), bare["current_lane"])
	assert.Equal(t, string(store.TaskStateActive), bare["state"])
}

// TestListProductTasksHandler_CancelledAtOnTheWire proves the cancelled
// timestamp -- only-stuck's one predicate the state never carries -- reaches
// the wire when the row has it.
func TestListProductTasksHandler_CancelledAtOnTheWire(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	cancelledAt := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	tasks := &fakeTaskStore{listProductTasksResult: store.Page[store.ProductTaskRow]{
		Items: []store.ProductTaskRow{{
			TaskID:      uuid.New(),
			Title:       "a cancelled task",
			Milestone:   store.ProductTaskMilestoneRef{ID: uuid.New(), Name: "M1", Status: store.MilestoneStatusInProgress},
			CurrentLane: store.LaneTesting,
			State:       store.TaskStateActive,
			CancelledAt: &cancelledAt,
		}},
	}}

	rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp handlers.ListProductTasksResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Tasks, 1)
	require.NotNil(t, resp.Tasks[0].CancelledAt)
	assert.Equal(t, cancelledAt, *resp.Tasks[0].CancelledAt)
}

// TestListProductTasksHandler_EmptyPage_ReturnsEmptyArray proves a product
// with no in-scope tasks lists as `[]`, never `null` -- a client can index
// the array without a nil check.
func TestListProductTasksHandler_EmptyPage_ReturnsEmptyArray(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	rec := serveProductTasks(t, &fakeTaskStore{}, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"tasks": []}`, rec.Body.String())
}

// ── param plumbing ───────────────────────────────────────────────────────────

// TestListProductTasksHandler_DefaultParams pins the default request: scope
// absent means every incomplete milestone of the product, the product's own
// scope_id is what the rows are scoped by (LB2), and no lane filter means
// every lane rather than no lanes.
func TestListProductTasksHandler_DefaultParams(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{}

	rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, productID, tasks.gotListProductTasksParams.ProductID)
	assert.Equal(t, scopeID, tasks.gotListProductTasksParams.ScopeID, "the read is scoped by the product's own scope_id, never by a caller's session")
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotListProductTasksParams.Scope)
	assert.Nil(t, tasks.gotListProductTasksParams.Lane, "an absent lane must mean every lane, not no lanes")
	assert.False(t, tasks.gotListProductTasksParams.OnlyStuck)
	assert.Equal(t, store.PageParams{}, tasks.gotListProductTasksParams.Page, "no page_size and no page_token means the server default first page")
}

// TestListProductTasksHandler_ExplicitIncompleteScopeIsTheDefault proves
// scope=incomplete and no scope at all reach the store identically, so the
// two spellings cannot drift into different filter sets (and therefore
// different continuation tokens).
func TestListProductTasksHandler_ExplicitIncompleteScopeIsTheDefault(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	explicit := &fakeTaskStore{}
	serveProductTasks(t, explicit, existingProduct(productID, scopeID), productID.String(), "scope=incomplete")
	implicit := &fakeTaskStore{}
	serveProductTasks(t, implicit, existingProduct(productID, scopeID), productID.String(), "")

	assert.Equal(t, implicit.gotListProductTasksParams, explicit.gotListProductTasksParams)
}

// TestListProductTasksHandler_AllParamsPlumb proves every recognized query
// parameter reaches ListProductTasksParams unchanged: both single-container
// scopes carry their container_id, the lane filter parses to the canonical
// Lane, and page_size/page_token reach PageParams as sent.
func TestListProductTasksHandler_AllParamsPlumb(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	containerID := uuid.New()

	for _, tc := range []struct{ scope string }{
		{scope: string(store.ProductTaskScopeMilestone)},
		{scope: string(store.ProductTaskScopeMilepebble)},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			tasks := &fakeTaskStore{}
			query := fmt.Sprintf("scope=%s&container_id=%s&lane=Testing&only_stuck=true&page_size=25&page_token=cursor-1",
				tc.scope, containerID)

			rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(), query)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeKind(tc.scope), ContainerID: containerID}, tasks.gotListProductTasksParams.Scope)
			require.NotNil(t, tasks.gotListProductTasksParams.Lane)
			assert.Equal(t, store.LaneTesting, *tasks.gotListProductTasksParams.Lane)
			assert.True(t, tasks.gotListProductTasksParams.OnlyStuck)
			assert.Equal(t, store.PageParams{PageSize: 25, ContinuationToken: "cursor-1"}, tasks.gotListProductTasksParams.Page)
		})
	}
}

// TestListProductTasksHandler_ContainerIDIgnoredForIncompleteScope proves
// container_id on the product-wide scope is inert rather than silently
// narrowing the read -- the store's FilterSet ignores it too, so a token
// minted under one spelling still resumes under the other.
func TestListProductTasksHandler_ContainerIDIgnoredForIncompleteScope(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{}

	rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(),
		"scope=incomplete&container_id="+uuid.New().String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}, tasks.gotListProductTasksParams.Scope)
	assert.Equal(t, uuid.Nil, tasks.gotListProductTasksParams.Scope.ContainerID)
}

// ── query-param validation ───────────────────────────────────────────────────

// TestListProductTasksHandler_BadQueryParams_Return400 is the whole
// validation table. Every row is a caller error answered with a 400 that
// names the valid values and never reaches the store -- a silent empty page
// would read as "this product has no work".
func TestListProductTasksHandler_BadQueryParams_Return400(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	containerID := uuid.New()

	for _, tc := range []struct {
		name    string
		id      string
		query   string
		wantMsg string
	}{
		{name: "unparseable path id", id: "not-a-uuid", query: "", wantMsg: "invalid id: must be a UUID"},
		{name: "unknown scope", id: productID.String(), query: "scope=everything", wantMsg: "scope: must be one of incomplete, milestone, milepebble"},
		{name: "milestone scope without container_id", id: productID.String(), query: "scope=milestone", wantMsg: "container_id: required for scope milestone; invalid or missing UUID"},
		{name: "milepebble scope without container_id", id: productID.String(), query: "scope=milepebble", wantMsg: `container_id: required for scope milepebble; invalid or missing UUID`},
		{name: "milestone scope with a non-UUID container_id", id: productID.String(), query: "scope=milestone&container_id=nope", wantMsg: "container_id: required for scope milestone; invalid or missing UUID"},
		{name: "unknown lane", id: productID.String(), query: "lane=Review", wantMsg: "lane: must be one of Scaffold, Implementation, Testing, Validation, Done"},
		{name: "lane is case sensitive", id: productID.String(), query: "lane=testing", wantMsg: "lane: must be one of Scaffold, Implementation, Testing, Validation, Done"},
		{name: "non-boolean only_stuck", id: productID.String(), query: "only_stuck=perhaps", wantMsg: "only_stuck: must be a boolean"},
		{name: "non-numeric page_size", id: productID.String(), query: "page_size=lots", wantMsg: "page_size: must be a non-negative integer"},
		{name: "negative page_size", id: productID.String(), query: "page_size=-1", wantMsg: "page_size: must be a non-negative integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &fakeTaskStore{}
			rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), tc.id, tc.query)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.JSONEq(t, fmt.Sprintf(`{"error": %q}`, tc.wantMsg), rec.Body.String())
			assert.Equal(t, store.ListProductTasksParams{}, tasks.gotListProductTasksParams, "a rejected request must never reach the store")
		})
	}

	// A well-formed single-container scope with a real UUID passes the
	// container_id rule, so the rule above is a requirement and not a
	// blanket rejection of the two scopes.
	t.Run("container_id present is accepted", func(t *testing.T) {
		tasks := &fakeTaskStore{}
		rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(),
			"scope=milestone&container_id="+containerID.String())
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

// TestListProductTasksHandler_BadQueryParamBeatsMissingProduct proves the
// query string is validated before the product is resolved: a bad scope is a
// 400 about the scope even when the product does not exist, so a caller gets
// the fixable error first.
func TestListProductTasksHandler_BadQueryParamBeatsMissingProduct(t *testing.T) {
	tasks := &fakeTaskStore{}
	products := &fakeProductTaskProductStore{getErr: store.ErrNotFound}

	rec := serveProductTasks(t, tasks, products, uuid.New().String(), "scope=nope")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "scope: must be one of")
}

// ── product resolution and error mappings ────────────────────────────────────

func TestListProductTasksHandler_UnknownProduct_Returns404(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := serveProductTasks(t, tasks, &fakeProductTaskProductStore{getErr: store.ErrNotFound}, uuid.New().String(), "")

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "product not found"}`, rec.Body.String())
	assert.Equal(t, store.ListProductTasksParams{}, tasks.gotListProductTasksParams)
}

func TestListProductTasksHandler_ProductStoreError_Returns500(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := serveProductTasks(t, tasks, &fakeProductTaskProductStore{getErr: errors.New("boom")}, uuid.New().String(), "")

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String(), "a store failure must not leak its text")
}

// TestListProductTasksHandler_ContainerOutsideProduct_Returns404 is LB1 on
// this surface: a container the product does not own is a distinct 404, not
// an empty page that would read as "this milestone has no work" and not
// another product's tasks.
func TestListProductTasksHandler_ContainerOutsideProduct_Returns404(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{listProductTasksErr: store.ErrMilestoneOutsideProduct}

	rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(),
		"scope=milestone&container_id="+uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "milestone not found in this product"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "tasks", "a refusal must never carry a page")
}

// TestListProductTasksHandler_ContinuationTokenErrors_Return400 is FR3's
// refusal mapping on this read: a token presented under a different filter
// set, a token from another scope, and a malformed token are all caller
// errors answered with a 400 carrying the store's own message -- while any
// other store failure is a 500 with no store text. The filter-set arm is the
// one writeConsoleQueryError gained for this read.
func TestListProductTasksHandler_ContinuationTokenErrors_Return400(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "filter mismatch", err: store.ErrTokenFilterMismatch},
		{name: "scope mismatch", err: store.ErrTokenScopeMismatch},
		{name: "malformed token", err: store.ErrInvalidContinuationToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &fakeTaskStore{listProductTasksErr: fmt.Errorf("%w: cursor was issued for a different filter set", tc.err)}

			rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(), "page_token=cursor-1")

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tc.err.Error())
		})
	}

	t.Run("any other store failure is a 500 with no store text", func(t *testing.T) {
		tasks := &fakeTaskStore{listProductTasksErr: errors.New("connection reset by peer")}

		rec := serveProductTasks(t, tasks, existingProduct(productID, scopeID), productID.String(), "page_token=cursor-1")

		assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
		assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String())
	})
}

// mustParseTime decodes one JSON time value, failing the test rather than
// returning a zero time on an unexpected shape.
func mustParseTime(t *testing.T, v any) time.Time {
	t.Helper()
	raw, ok := v.(string)
	require.True(t, ok, "expected an RFC3339 string, got %#v", v)
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	require.NoError(t, err)
	return parsed.UTC()
}