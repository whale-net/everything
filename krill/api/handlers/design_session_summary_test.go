// Unit tests for ListProductDesignSessionsHandler (design_session_summary.go,
// FR d0a63ffb-8e80-47c1-8a64-2729d4950522): the ungated GET
// /products/{id}/design-sessions aggregate read. No Postgres --
// fakeDesignSessionStore (fake_design_session_store_test.go) seeds the
// aggregate; krill/store/design_session_integration_test.go proves the
// query's own derivation against real Postgres, and
// krill/store/design_session_summary_test.go pins the Stage vocabulary.
package handlers_test

import (
	"encoding/json"
	"errors"
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

// doGetProductDesignSessionsRequest mounts ListProductDesignSessionsHandler
// behind a "GET /products/{id}/design-sessions" pattern -- exactly like
// routes.go -- so r.PathValue("id") is populated the way a real request
// populates it. Ungated, like every other read route here.
func doGetProductDesignSessionsRequest(t *testing.T, designSessions store.DesignSessionStore, id string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle("GET /products/{id}/design-sessions", handlers.ListProductDesignSessionsHandler(designSessions))

	req := httptest.NewRequest(http.MethodGet, "/products/"+id+"/design-sessions", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// dssSeed seeds one product's aggregate into the fake, with every row's
// Stage set from store's own vocabulary so a wire change in the vocabulary
// is caught here too.
func dssSeed(f *fakeDesignSessionStore, productID uuid.UUID, rows []store.DesignSessionSummary, blockingTotal, holding int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summaries[productID] = store.ProductDesignSessionsSummary{
		ProductID:                   productID,
		Sessions:                    rows,
		OpenBlockingQuestionCount:   blockingTotal,
		SessionsHoldingOpenBlocking: holding,
	}
}

// TestListProductDesignSessionsHandler_ReturnsAggregateWithStageAndCounts
// proves the wire shape end to end: one call returns every session row
// with its derived stage (store.Stage's string verbatim) and its open
// blocking/non-blocking counts, plus the two product-level totals.
func TestListProductDesignSessionsHandler_ReturnsAggregateWithStageAndCounts(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	productID := uuid.New()
	opened := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	dssSeed(designSessions, productID, []store.DesignSessionSummary{
		{DesignSession: store.DesignSession{ID: uuid.New(), ProductID: productID, OpeningSubmission: "newest idea"},
			Stage: store.StageRuled, OpenBlockingQuestions: 1, OpenNonBlockingQuestions: 2},
		{DesignSession: store.DesignSession{ID: uuid.New(), ProductID: productID, OpeningSubmission: "older idea", CreatedAt: opened},
			Stage: store.StageApproved, OpenBlockingQuestions: 2, OpenNonBlockingQuestions: 0},
	}, 3, 2)

	rec := doGetProductDesignSessionsRequest(t, designSessions, productID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp handlers.ProductDesignSessionsSummaryWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, productID.String(), resp.ProductID)
	assert.Equal(t, 3, resp.OpenBlockingQuestionCount)
	assert.Equal(t, 2, resp.SessionsHoldingOpenBlocking)

	require.Len(t, resp.Sessions, 2)
	assert.Equal(t, "newest idea", resp.Sessions[0].OpeningSubmission)
	assert.Equal(t, string(store.StageRuled), resp.Sessions[0].Stage)
	assert.Equal(t, 1, resp.Sessions[0].OpenBlockingQuestions)
	assert.Equal(t, 2, resp.Sessions[0].OpenNonBlockingQuestions)

	assert.Equal(t, "older idea", resp.Sessions[1].OpeningSubmission)
	assert.Equal(t, string(store.StageApproved), resp.Sessions[1].Stage)
	assert.Equal(t, 2, resp.Sessions[1].OpenBlockingQuestions)
	assert.Equal(t, 0, resp.Sessions[1].OpenNonBlockingQuestions)
	assert.Equal(t, opened, resp.Sessions[1].CreatedAt.UTC())
}

// TestListProductDesignSessionsHandler_EmptyProduct_ReturnsEmptyJSONArray
// keeps a client from having to nil-check: a live product with no sessions
// yet is a 200 whose sessions field is [], never null (the store
// distinguishes this case from an unknown product, which 404s below).
func TestListProductDesignSessionsHandler_EmptyProduct_ReturnsEmptyJSONArray(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	productID := uuid.New()
	dssSeed(designSessions, productID, nil, 0, 0)

	rec := doGetProductDesignSessionsRequest(t, designSessions, productID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"sessions":[]`, "sessions must serialize as [] so a client can index it without a nil check")

	var resp handlers.ProductDesignSessionsSummaryWire
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.Sessions)
}

// TestListProductDesignSessionsHandler_UnknownProduct_Returns404 proves the
// two empty cases stay distinguishable on the wire: a product that does
// not exist is a 404, not an empty listing a caller could read as "no
// sessions yet".
func TestListProductDesignSessionsHandler_UnknownProduct_Returns404(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	designSessions.summarizeErr = store.ErrNotFound

	rec := doGetProductDesignSessionsRequest(t, designSessions, uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestListProductDesignSessionsHandler_StoreFailure_Returns500 proves a
// read that fails is not dressed up as an empty aggregate.
func TestListProductDesignSessionsHandler_StoreFailure_Returns500(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	designSessions.summarizeErr = errors.New("connection refused")

	rec := doGetProductDesignSessionsRequest(t, designSessions, uuid.New().String())

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "connection refused", "the internal error must not leak store text")
}

// TestListProductDesignSessionsHandler_InvalidProductID_Returns400 rejects
// a non-UUID path id before the store is ever reached.
func TestListProductDesignSessionsHandler_InvalidProductID_Returns400(t *testing.T) {
	designSessions := newFakeDesignSessionStore()

	rec := doGetProductDesignSessionsRequest(t, designSessions, "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
