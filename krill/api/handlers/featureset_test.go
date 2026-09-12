// Unit tests for CreateFeatureSetHandler (featureset.go, issue #2490's
// Testing section). No Postgres dependency -- fakeFeatureSetStore/
// fakeSessionStore stand in for the store interfaces.
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

// TestCreateFeatureSetHandler_Success proves FR2/LB1/LB2: a well-formed
// create returns the surrogate id, passes the single parent reference
// (product_id) through unchanged, and writes scope_id from the session.
func TestCreateFeatureSetHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	productID := uuid.New()

	featureSets := &fakeFeatureSetStore{}
	rec := doGatedRequest(t, handlers.CreateFeatureSetHandler(featureSets), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "name": "M1"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
	assert.Equal(t, scopeID, featureSets.gotScopeID)
	assert.Equal(t, productID, featureSets.gotProductID)
}

// TestCreateFeatureSetHandler_MissingOrMalformedParent_Rejected proves LB2's
// "exactly one parent field" is enforced: an empty, missing, or non-UUID
// product_id is a 400, and the store is never called.
func TestCreateFeatureSetHandler_MissingOrMalformedParent_Rejected(t *testing.T) {
	cases := map[string]string{
		"missing product_id":   `{"name": "M1"}`,
		"empty product_id":     `{"product_id": "", "name": "M1"}`,
		"malformed product_id": `{"product_id": "not-a-uuid", "name": "M1"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			featureSets := &fakeFeatureSetStore{}
			rec := doGatedRequest(t, handlers.CreateFeatureSetHandler(featureSets), sessions, sessionIDStr, body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "case %q: got body %q", name, rec.Body.String())
			assert.Equal(t, uuid.Nil, featureSets.gotScopeID, "case %q: a validation failure must never reach the store", name)
		})
	}
}

// TestCreateFeatureSetHandler_MultiParentShape_Rejected proves a payload
// carrying an extra parent-shaped field (here, a second, unrecognized
// parent reference) is a 400 rather than the decoder silently ignoring it
// (types.go's decodeStrict, DisallowUnknownFields).
func TestCreateFeatureSetHandler_MultiParentShape_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	featureSets := &fakeFeatureSetStore{}

	rec := doGatedRequest(t, handlers.CreateFeatureSetHandler(featureSets), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": "M1", "feature_set_id": "`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, featureSets.gotScopeID, "a multi-parent payload must never reach the store")
}

// TestCreateFeatureSetHandler_NonexistentParent_Returns400 proves
// writeStoreError (types.go) maps store.ErrNotFound (a missing or
// cross-scope parent, LB2) onto a 400.
func TestCreateFeatureSetHandler_NonexistentParent_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	featureSets := &fakeFeatureSetStore{createErr: store.ErrNotFound}

	rec := doGatedRequest(t, handlers.CreateFeatureSetHandler(featureSets), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": "M1"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestCreateFeatureSetHandler_GenericStoreFailure_Returns500 proves
// writeStoreError's default case: an unclassified store failure is a 500.
func TestCreateFeatureSetHandler_GenericStoreFailure_Returns500(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	featureSets := &fakeFeatureSetStore{createErr: genericStoreErr}

	rec := doGatedRequest(t, handlers.CreateFeatureSetHandler(featureSets), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "name": "M1"}`)

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}
