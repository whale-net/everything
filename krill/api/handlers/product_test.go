// Unit tests for CreateProductHandler (product.go, issue #2490's Testing
// section) and ListProductsHandler (product.go, issue #2941). No Postgres
// dependency -- fakeProductStore/fakeSessionStore stand in for the store
// interfaces (krill/store/entities_integration_test.go covers the
// real-Postgres chain).
package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// TestCreateProductHandler_Success proves FR1/LB1/LB2: a well-formed create
// returns the surrogate id and writes scope_id from the session, never from
// the request body (createProductRequest has no scope_id field at all).
func TestCreateProductHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)

	products := &fakeProductStore{}
	rec := doGatedRequest(t, handlers.CreateProductHandler(products), sessions, sessionIDStr,
		`{"name": "Krill", "vision": "A spec-of-record substrate"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
	assert.Equal(t, scopeID, products.gotScopeID, "scope_id must come from the session, never the request body")
	assert.Equal(t, "Krill", products.gotName)
	assert.Equal(t, "A spec-of-record substrate", products.gotVision)
}

// TestCreateProductHandler_NoSessionID_Rejected proves FR3's write gate sits
// in front of this handler: a create with no krill session id never reaches
// the store.
func TestCreateProductHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	products := &fakeProductStore{}

	rec := doGatedRequest(t, handlers.CreateProductHandler(products), sessions, "",
		`{"name": "Krill", "vision": "vision"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, products.gotScopeID, "the store must never be called without a valid session")
}

// TestCreateProductHandler_ValidationFailures proves each documented
// rejection returns 400 without ever calling the store.
func TestCreateProductHandler_ValidationFailures(t *testing.T) {
	cases := map[string]string{
		"missing name":   `{"vision": "vision"}`,
		"missing vision": `{"name": "Krill"}`,
		"unknown field":  `{"name": "Krill", "vision": "vision", "unexpected_field": true}`,
		"malformed json": `{not json`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)

			products := &fakeProductStore{}
			rec := doGatedRequest(t, handlers.CreateProductHandler(products), sessions, sessionIDStr, body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "case %q: got body %q", name, rec.Body.String())
			assert.Equal(t, uuid.Nil, products.gotScopeID, "case %q: a validation failure must never reach the store", name)
		})
	}
}

// TestCreateProductHandler_DuplicateName_Returns409 proves writeStoreError
// (types.go) maps a scope-qualified unique-constraint violation onto a 409,
// not a 500 -- see krill/store/entities_integration_test.go for the real
// Postgres error this fake's injected error stands in for.
func TestCreateProductHandler_DuplicateName_Returns409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)

	products := &fakeProductStore{createErr: uniqueViolationErr}
	rec := doGatedRequest(t, handlers.CreateProductHandler(products), sessions, sessionIDStr,
		`{"name": "Krill", "vision": "vision"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// TestCreateProductHandler_ActingDiffersFromOnBehalfOf_BothRecordedDistinctly
// proves this task's "a create where acting and on-behalf-of differ records
// both subjects distinctly on the session used" bullet: an agent acting on
// behalf of a human still succeeds, and using that session for a create
// does not collapse or overwrite either subject on the underlying session
// (gate_test.go/session_integration_test.go cover InitSession's own LB4
// dual-subject population; this proves a create's use of that session
// leaves it undisturbed).
func TestCreateProductHandler_ActingDiffersFromOnBehalfOf_BothRecordedDistinctly(t *testing.T) {
	sessions := newFakeSessionStore()
	scopeID := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-caller", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "human-owner", Kind: store.SubjectKindHuman}
	sessionID := mustInitSession(t, sessions, scopeID, acting, onBehalfOf)

	products := &fakeProductStore{}
	rec := doGatedRequest(t, handlers.CreateProductHandler(products), sessions, sessionID.String(),
		`{"name": "Krill", "vision": "vision"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, products.gotScopeID)

	sess, err := sessions.GetSession(t.Context(), sessionID)
	require.NoError(t, err)
	assert.Equal(t, acting, sess.Acting)
	assert.Equal(t, onBehalfOf, sess.OnBehalfOf)
	assert.NotEqual(t, sess.Acting, sess.OnBehalfOf, "a create must never collapse acting and on_behalf_of into one subject")
}

// TestListProductsHandler_MissingOrInvalidScopeID_Returns400 proves
// scope_id is a required query parameter -- a Product has no parent
// entity to resolve scope from.
func TestListProductsHandler_MissingOrInvalidScopeID_Returns400(t *testing.T) {
	for name, target := range map[string]string{
		"missing": "/products",
		"invalid": "/products?scope_id=not-a-uuid",
	} {
		t.Run(name, func(t *testing.T) {
			products := &fakeProductStore{}
			rec := httptest.NewRecorder()
			handlers.ListProductsHandler(products)(rec, httptest.NewRequest(http.MethodGet, target, nil))

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, uuid.Nil, products.gotListScopeID, "a bad scope_id must never reach the store")
		})
	}
}

// TestListProductsHandler_Success proves scope_id passes through unchanged
// and every current Product is returned in store order with id/name/vision.
func TestListProductsHandler_Success(t *testing.T) {
	scopeID := uuid.New()
	first, second := uuid.New(), uuid.New()
	products := &fakeProductStore{listed: []store.Product{
		{ID: first, ScopeID: scopeID, Name: "Alpha", Vision: "first vision"},
		{ID: second, ScopeID: scopeID, Name: "Beta", Vision: "second vision"},
	}}

	rec := httptest.NewRecorder()
	handlers.ListProductsHandler(products)(rec, httptest.NewRequest(http.MethodGet, "/products?scope_id="+scopeID.String(), nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, products.gotListScopeID)
	var resp handlers.ListProductsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, []handlers.ProductSummary{
		{ID: first.String(), Name: "Alpha", Vision: "first vision"},
		{ID: second.String(), Name: "Beta", Vision: "second vision"},
	}, resp.Products)
}

// TestListProductsHandler_Empty_ReturnsEmptyArray proves a scope with no
// Products lists as `[]`, never `null`.
func TestListProductsHandler_Empty_ReturnsEmptyArray(t *testing.T) {
	rec := httptest.NewRecorder()
	handlers.ListProductsHandler(&fakeProductStore{})(rec, httptest.NewRequest(http.MethodGet, "/products?scope_id="+uuid.New().String(), nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"products": []}`, rec.Body.String())
}

// TestListProductsHandler_StoreError_Returns500 proves an unexpected store
// failure is a 500, not a partial list.
func TestListProductsHandler_StoreError_Returns500(t *testing.T) {
	products := &fakeProductStore{listErr: errors.New("boom")}
	rec := httptest.NewRecorder()
	handlers.ListProductsHandler(products)(rec, httptest.NewRequest(http.MethodGet, "/products?scope_id="+uuid.New().String(), nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}
