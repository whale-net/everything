// Unit tests for CreateProductHandler (product.go, issue #2490's Testing
// section). No Postgres dependency -- fakeProductStore/fakeSessionStore
// stand in for the store interfaces (krill/store/entities_integration_test.go
// covers the real-Postgres chain).
package handlers_test

import (
	"encoding/json"
	"net/http"
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
