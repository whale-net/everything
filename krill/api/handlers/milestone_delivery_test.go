// Unit tests for GetProductDeliveryHandler (milestone.go, issue #2689's
// Testing section, FR11): the ungated GET /products/{id}/delivery
// endpoint. No Postgres, no real //krill/slice.Querier --
// fakeProductDeliveryProductStore/fakeProductDeliveryQuerier
// (this file) stand in for store.ProductStore and this package's own
// unexported productDeliveryQuerier interface, mirroring
// delivery_shipment_test.go's fakeDeliveryBreakdownQuerier precedent.
// krill/slice/milestone_listing_integration_test.go covers the real
// query behavior against Postgres; krill/mcp/tools/milestone_test.go's
// TestMCPListProductDelivery_EndToEnd covers the MCP surface end to end.
package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// fakeProductDeliveryProductStore backs this file: unlike fake_entity_
// store_test.go's fakeProductStore (whose GetCurrentByID always returns
// ErrNotFound), GetProductDeliveryHandler reads the target Product back
// (to resolve its scope_id, LB2) before ever calling the querier, so this
// fake needs a configurable success path too -- mirrors
// pointer_test.go's fakePointerProductStore precedent.
type fakeProductDeliveryProductStore struct {
	product  store.Product
	getErr   error
	gotGetID uuid.UUID
}

func (f *fakeProductDeliveryProductStore) Create(ctx context.Context, scopeID uuid.UUID, name, vision string) (store.Product, error) {
	return store.Product{}, errors.New("not used by milestone_delivery_test.go")
}

func (f *fakeProductDeliveryProductStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (store.Product, error) {
	f.gotGetID = id
	if f.getErr != nil {
		return store.Product{}, f.getErr
	}
	return f.product, nil
}

func (f *fakeProductDeliveryProductStore) ListCurrentByScope(ctx context.Context, scopeID uuid.UUID) ([]store.Product, error) {
	return nil, nil
}

var _ store.ProductStore = (*fakeProductDeliveryProductStore)(nil)

// fakeProductDeliveryQuerier stands in for //krill/slice's
// ListProductDelivery (this package's own unexported productDeliveryQuerier
// narrowing of *slice.Querier) -- records the arguments it saw so a test
// can assert the handler passed the resolved scope_id, the path's
// productID, and the parsed status filter through unchanged.
type fakeProductDeliveryQuerier struct {
	listing slice.DeliveryListing
	err     error

	called      bool
	gotScopeID  uuid.UUID
	gotProduct  uuid.UUID
	gotStatuses []store.MilestoneStatus
}

func (f *fakeProductDeliveryQuerier) ListProductDelivery(ctx context.Context, scopeID, productID uuid.UUID, statuses []store.MilestoneStatus) (slice.DeliveryListing, error) {
	f.called = true
	f.gotScopeID, f.gotProduct, f.gotStatuses = scopeID, productID, statuses
	return f.listing, f.err
}

// doGetProductDeliveryRequest drives the ungated GET endpoint directly (no
// RequireSession wrapping, mirroring routes.go's "GET
// /products/{id}/delivery"), with r.PathValue("id") populated via a bare
// mux mount and rawQuery appended verbatim (e.g. "status=planned&status=in
// progress").
func doGetProductDeliveryRequest(t *testing.T, handler http.HandlerFunc, id, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/{id}/delivery", handler)

	target := "/probe/" + id + "/delivery"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestGetProductDeliveryHandler_NoFilter_ResolvesScopeAndReturnsListing
// proves the success path: the handler resolves productID's scope_id from
// the Product row (LB2 parentage) and passes an empty status slice (a
// query string with no `status` param at all) straight through to the
// querier, returning its DeliveryListing verbatim.
func TestGetProductDeliveryHandler_NoFilter_ResolvesScopeAndReturnsListing(t *testing.T) {
	scopeID := uuid.New()
	productID := uuid.New()
	milestoneID := uuid.New()
	products := &fakeProductDeliveryProductStore{product: store.Product{ID: productID, ScopeID: scopeID, Name: "krill"}}
	querier := &fakeProductDeliveryQuerier{listing: slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{{ID: milestoneID, Name: "M1", Status: store.MilestoneStatusPlanned}},
	}}

	rec := doGetProductDeliveryRequest(t, handlers.GetProductDeliveryHandler(products, querier), productID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, querier.called)
	assert.Equal(t, scopeID, querier.gotScopeID, "the querier must receive the Product's own scope_id, not a zero value")
	assert.Equal(t, productID, querier.gotProduct)
	assert.Empty(t, querier.gotStatuses, "an absent `status` query param must mean \"all\" -- an empty slice, never nil-vs-empty confusion downstream")

	var resp slice.DeliveryListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Milestones, 1)
	assert.Equal(t, milestoneID, resp.Milestones[0].ID)
}

// TestGetProductDeliveryHandler_RepeatedStatusParam_ParsesEveryValue proves
// a repeated `status` query parameter becomes the querier's statuses
// slice, in the order given.
func TestGetProductDeliveryHandler_RepeatedStatusParam_ParsesEveryValue(t *testing.T) {
	productID := uuid.New()
	products := &fakeProductDeliveryProductStore{product: store.Product{ID: productID, ScopeID: uuid.New()}}
	querier := &fakeProductDeliveryQuerier{}

	rec := doGetProductDeliveryRequest(t, handlers.GetProductDeliveryHandler(products, querier), productID.String(), "status=planned&status=in+progress")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, querier.called)
	assert.Equal(t, []store.MilestoneStatus{store.MilestoneStatusPlanned, store.MilestoneStatusInProgress}, querier.gotStatuses)
}

// TestGetProductDeliveryHandler_UnknownStatus_Returns400NamingSevenValues
// proves issue #2689's own design constraint: an unrecognized `status`
// value is a 400 naming the seven valid values, never a silent empty
// result -- and the querier is never called.
func TestGetProductDeliveryHandler_UnknownStatus_Returns400NamingSevenValues(t *testing.T) {
	productID := uuid.New()
	products := &fakeProductDeliveryProductStore{product: store.Product{ID: productID, ScopeID: uuid.New()}}
	querier := &fakeProductDeliveryQuerier{}

	rec := doGetProductDeliveryRequest(t, handlers.GetProductDeliveryHandler(products, querier), productID.String(), "status=bogus-status")

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, querier.called, "an invalid filter must never reach the querier")

	for _, want := range []string{
		"not started", "in design", "planned", "in progress", "shipped", "partially complete", "abandoned",
	} {
		assert.Contains(t, rec.Body.String(), want, "the 400 body must name every one of the seven valid values")
	}
}

// TestGetProductDeliveryHandler_InvalidProductID_Returns400_NoStoreCall
// proves a non-UUID path {id} is rejected before either dependency is
// ever called.
func TestGetProductDeliveryHandler_InvalidProductID_Returns400_NoStoreCall(t *testing.T) {
	products := &fakeProductDeliveryProductStore{}
	querier := &fakeProductDeliveryQuerier{}

	rec := doGetProductDeliveryRequest(t, handlers.GetProductDeliveryHandler(products, querier), "not-a-uuid", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, querier.called)
}

// TestGetProductDeliveryHandler_ProductNotFound_Returns404_NoQuerierCall
// proves an unknown productID reads back as 404, never reaching the
// querier -- there is no scope_id to resolve LB2 parentage against.
func TestGetProductDeliveryHandler_ProductNotFound_Returns404_NoQuerierCall(t *testing.T) {
	products := &fakeProductDeliveryProductStore{getErr: store.ErrNotFound}
	querier := &fakeProductDeliveryQuerier{}

	rec := doGetProductDeliveryRequest(t, handlers.GetProductDeliveryHandler(products, querier), uuid.New().String(), "")

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.False(t, querier.called)
}
