// Unit tests for CreateFeatureHandler (feature.go, issue #2490's Testing
// section). No Postgres dependency -- fakeFeatureStore/fakeSessionStore
// stand in for the store interfaces.
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

// TestCreateFeatureHandler_Success proves FR2/LB1/LB2: a well-formed create
// returns the surrogate id, passes the single parent reference
// (feature_set_id) through unchanged, and writes scope_id from the session.
func TestCreateFeatureHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	featureSetID := uuid.New()

	features := &fakeFeatureStore{}
	rec := doGatedRequest(t, handlers.CreateFeatureHandler(features), sessions, sessionIDStr,
		`{"feature_set_id": "`+featureSetID.String()+`", "name": "Entity write API"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
	assert.Equal(t, scopeID, features.gotScopeID)
	assert.Equal(t, featureSetID, features.gotFeatureSetID)
}

// TestCreateFeatureHandler_MissingOrMalformedParent_Rejected proves LB2's
// "exactly one parent field" is enforced.
func TestCreateFeatureHandler_MissingOrMalformedParent_Rejected(t *testing.T) {
	cases := map[string]string{
		"missing feature_set_id":   `{"name": "Entity write API"}`,
		"empty feature_set_id":     `{"feature_set_id": "", "name": "Entity write API"}`,
		"malformed feature_set_id": `{"feature_set_id": "not-a-uuid", "name": "Entity write API"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			features := &fakeFeatureStore{}
			rec := doGatedRequest(t, handlers.CreateFeatureHandler(features), sessions, sessionIDStr, body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "case %q: got body %q", name, rec.Body.String())
			assert.Equal(t, uuid.Nil, features.gotScopeID, "case %q: a validation failure must never reach the store", name)
		})
	}
}

// TestCreateFeatureHandler_NonexistentParent_Returns400 proves
// writeStoreError maps store.ErrNotFound onto a 400.
func TestCreateFeatureHandler_NonexistentParent_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	features := &fakeFeatureStore{createErr: store.ErrNotFound}

	rec := doGatedRequest(t, handlers.CreateFeatureHandler(features), sessions, sessionIDStr,
		`{"feature_set_id": "`+uuid.New().String()+`", "name": "Entity write API"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
