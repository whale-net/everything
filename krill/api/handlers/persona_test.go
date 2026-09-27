// Unit tests for CreatePersonaHandler (persona.go, FR 7a3906a3). No
// Postgres dependency -- fakePersonaStore/fakeSessionStore stand in for the
// store interfaces, mirroring featureset_test.go's shape one product-level
// create over.
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

// TestCreatePersonaHandler_Success proves the FR's promise: a well-formed
// POST returns a surrogate id of its own, passes the single parent
// reference (product_id) through unchanged, and writes scope_id from the
// session rather than the body.
func TestCreatePersonaHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	productID := uuid.New()

	personas := &fakePersonaStore{}
	rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "name": "Swarm Operator", "description": "runs the swarm"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
	assert.Equal(t, scopeID, personas.gotScopeID)
	assert.Equal(t, productID, personas.gotProductID)
	assert.Equal(t, "Swarm Operator", personas.gotName)
}

// TestCreatePersonaHandler_TwoPersonas_GetDistinctIDs proves the two
// created rows are not collapsing onto one id -- the FR's "each is created
// and returned with its own surrogate id".
func TestCreatePersonaHandler_TwoPersonas_GetDistinctIDs(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	productID := uuid.New()
	personas := &fakePersonaStore{}

	first := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "name": "Requirement Contributor"}`)
	second := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "name": "Swarm Operator"}`)

	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())

	var a, b idResponseBody
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &a))
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &b))
	assert.NotEqual(t, a.ID, b.ID)
}

// TestCreatePersonaHandler_MissingOrMalformedParent_Rejected proves LB2's
// "exactly one parent field" is enforced: an empty, missing, or non-UUID
// product_id is a 400, and the store is never called.
func TestCreatePersonaHandler_MissingOrMalformedParent_Rejected(t *testing.T) {
	cases := map[string]string{
		"missing product_id":   `{"name": "Swarm Operator"}`,
		"empty product_id":     `{"product_id": "", "name": "Swarm Operator"}`,
		"malformed product_id": `{"product_id": "not-a-uuid", "name": "Swarm Operator"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			personas := &fakePersonaStore{}
			rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr, body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "case %q: got body %q", name, rec.Body.String())
			assert.Equal(t, uuid.Nil, personas.gotScopeID, "case %q: a validation failure must never reach the store", name)
		})
	}
}

// TestCreatePersonaHandler_MissingName_Rejected proves a blank name is a
// 400 before the store is reached.
func TestCreatePersonaHandler_MissingName_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	personas := &fakePersonaStore{}

	rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": ""}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, personas.gotScopeID)
}

// TestCreatePersonaHandler_MultiParentShape_Rejected proves a payload
// carrying an extra parent-shaped field is a 400 rather than the decoder
// silently ignoring it (types.go's decodeStrict).
func TestCreatePersonaHandler_MultiParentShape_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	personas := &fakePersonaStore{}

	rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": "Swarm Operator", "feature_set_id": "`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, personas.gotScopeID, "a multi-parent payload must never reach the store")
}

// TestCreatePersonaHandler_NonexistentProduct_Returns400 proves
// writeStoreError maps store.ErrNotFound (a missing or cross-scope parent,
// LB2) onto a 400.
func TestCreatePersonaHandler_NonexistentProduct_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	personas := &fakePersonaStore{createErr: store.ErrNotFound}

	rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": "Swarm Operator"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestCreatePersonaHandler_DuplicateName_Returns409 proves a name conflict
// surfaces as the same 409 every other create returns (LB1's
// scope-qualified name uniqueness), never a 500.
func TestCreatePersonaHandler_DuplicateName_Returns409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	personas := &fakePersonaStore{createErr: uniqueViolationErr}

	rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": "Swarm Operator"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// TestCreatePersonaHandler_GenericStoreFailure_Returns500 proves
// writeStoreError's default case.
func TestCreatePersonaHandler_GenericStoreFailure_Returns500(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	personas := &fakePersonaStore{createErr: genericStoreErr}

	rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": "Swarm Operator"}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}

// TestCreatePersonaHandler_NoSessionID_Rejected proves the session gate
// sits in front of this handler too (NFR6): an ungated POST is a 401 and
// the store is never reached.
func TestCreatePersonaHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	personas := &fakePersonaStore{}

	rec := doGatedRequest(t, handlers.CreatePersonaHandler(personas), sessions, "",
		`{"product_id": "`+uuid.New().String()+`", "name": "Swarm Operator"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, personas.gotScopeID)
}
