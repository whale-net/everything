// Unit tests for CreateNonGoalHandler (nongoal.go, FR 7a3906a3). No
// Postgres dependency -- fakeNonGoalStore/fakeSessionStore stand in for the
// store interfaces.
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

// TestCreateNonGoalHandler_Success_BothKinds proves the `kind` discriminator
// selects one of the two store kinds (never a third), each create returns
// its own surrogate id, and scope_id comes from the session.
func TestCreateNonGoalHandler_Success_BothKinds(t *testing.T) {
	for _, kind := range []store.NonGoalKind{store.NonGoalKindPermanent, store.NonGoalKindDeferred} {
		t.Run(string(kind), func(t *testing.T) {
			sessions, scopeID, sessionIDStr := newTestSession(t)
			productID := uuid.New()
			nonGoals := &fakeNonGoalStore{}

			rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
				`{"product_id": "`+productID.String()+`", "kind": "`+string(kind)+`", "name": "No second Product"}`)

			require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			var resp idResponseBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.NotEmpty(t, resp.ID)
			assert.Equal(t, kind, nonGoals.gotKind)
			assert.Equal(t, scopeID, nonGoals.gotScopeID)
			assert.Equal(t, productID, nonGoals.gotProductID)
		})
	}
}

// TestCreateNonGoalHandler_TwoNonGoals_GetDistinctIDs proves the two
// created rows do not collapse onto one id.
func TestCreateNonGoalHandler_TwoNonGoals_GetDistinctIDs(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	productID := uuid.New()
	nonGoals := &fakeNonGoalStore{}

	first := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "kind": "permanent", "name": "No second Product"}`)
	second := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "kind": "deferred", "name": "No second Slice"}`)

	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())
	require.Equal(t, http.StatusCreated, second.Code, second.Body.String())

	var a, b idResponseBody
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &a))
	require.NoError(t, json.Unmarshal(second.Body.Bytes(), &b))
	assert.NotEqual(t, a.ID, b.ID)
}

// TestCreateNonGoalHandler_InvalidKind_Rejected proves there is no third
// bucket: anything other than "permanent"/"deferred" is a 400 and the store
// is never called. The empty case matters because a caller that omits
// `kind` must not silently land in whichever bucket the zero value happens
// to be.
func TestCreateNonGoalHandler_InvalidKind_Rejected(t *testing.T) {
	cases := []string{"", "Permanent", "PERMANENT", "retired"}

	for _, kind := range cases {
		t.Run("kind="+kind, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			nonGoals := &fakeNonGoalStore{}

			rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
				`{"product_id": "`+uuid.New().String()+`", "kind": "`+kind+`", "name": "No second Product"}`)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "kind %q: got body %q", kind, rec.Body.String())
			assert.Equal(t, uuid.Nil, nonGoals.gotScopeID, "kind %q: an invalid kind must never reach the store", kind)
		})
	}
}

// TestCreateNonGoalHandler_MissingOrMalformedParent_Rejected proves LB2's
// "exactly one parent field" is enforced on this route too.
func TestCreateNonGoalHandler_MissingOrMalformedParent_Rejected(t *testing.T) {
	cases := map[string]string{
		"missing product_id":   `{"kind": "permanent", "name": "No second Product"}`,
		"empty product_id":     `{"product_id": "", "kind": "permanent", "name": "No second Product"}`,
		"malformed product_id": `{"product_id": "not-a-uuid", "kind": "permanent", "name": "No second Product"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			nonGoals := &fakeNonGoalStore{}
			rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr, body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "case %q: got body %q", name, rec.Body.String())
			assert.Equal(t, uuid.Nil, nonGoals.gotScopeID, "case %q: a validation failure must never reach the store", name)
		})
	}
}

// TestCreateNonGoalHandler_MissingName_Rejected proves a blank name is a
// 400 before the store is reached.
func TestCreateNonGoalHandler_MissingName_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	nonGoals := &fakeNonGoalStore{}

	rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "kind": "permanent", "name": ""}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, nonGoals.gotScopeID)
}

// TestCreateNonGoalHandler_MultiParentShape_Rejected proves a payload
// carrying an extra parent-shaped field is a 400 rather than the decoder
// silently ignoring it (types.go's decodeStrict).
func TestCreateNonGoalHandler_MultiParentShape_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	nonGoals := &fakeNonGoalStore{}

	rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "kind": "permanent", "name": "No second Product", "feature_id": "`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, nonGoals.gotScopeID, "a multi-parent payload must never reach the store")
}

// TestCreateNonGoalHandler_NonexistentProduct_Returns400 proves
// writeStoreError maps store.ErrNotFound (a missing or cross-scope parent,
// LB2) onto a 400.
func TestCreateNonGoalHandler_NonexistentProduct_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	nonGoals := &fakeNonGoalStore{createErr: store.ErrNotFound}

	rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "kind": "permanent", "name": "No second Product"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestCreateNonGoalHandler_DuplicateName_Returns409 proves a name conflict
// surfaces as the same 409 every other create returns, never a 500.
func TestCreateNonGoalHandler_DuplicateName_Returns409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	nonGoals := &fakeNonGoalStore{createErr: uniqueViolationErr}

	rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "kind": "permanent", "name": "No second Product"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// TestCreateNonGoalHandler_GenericStoreFailure_Returns500 proves
// writeStoreError's default case.
func TestCreateNonGoalHandler_GenericStoreFailure_Returns500(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	nonGoals := &fakeNonGoalStore{createErr: genericStoreErr}

	rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "kind": "permanent", "name": "No second Product"}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}

// TestCreateNonGoalHandler_NoSessionID_Rejected proves the session gate
// sits in front of this handler too (NFR6).
func TestCreateNonGoalHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	nonGoals := &fakeNonGoalStore{}

	rec := doGatedRequest(t, handlers.CreateNonGoalHandler(nonGoals), sessions, "",
		`{"product_id": "`+uuid.New().String()+`", "kind": "permanent", "name": "No second Product"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, nonGoals.gotScopeID)
}
