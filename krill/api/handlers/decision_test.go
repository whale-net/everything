// Unit tests for AttachLoadBearingDecisionHandler (decision.go, issue
// #2490's Testing section). No Postgres dependency -- fakeDecisionStore/
// fakeSessionStore stand in for the store interfaces
// (krill/store/entities_integration_test.go's
// TestAttachLoadBearingDecision_ReadableBackFromFeatureSet covers the
// real-Postgres attach-then-read-back proof).
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

// TestAttachLoadBearingDecisionHandler_Success proves FR4: a decision
// attaches to a FeatureSet (not a Product, not a Feature) and returns its
// own surrogate id.
func TestAttachLoadBearingDecisionHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	featureSetID := uuid.New()

	decisions := &fakeDecisionStore{}
	rec := doGatedRequest(t, handlers.AttachLoadBearingDecisionHandler(decisions), sessions, sessionIDStr,
		`{"feature_set_id": "`+featureSetID.String()+`", "name": "LB1"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
	assert.Equal(t, scopeID, decisions.gotScopeID)
	assert.Equal(t, featureSetID, decisions.gotFeatureSetID)
}

// TestAttachLoadBearingDecisionHandler_MissingParent_Rejected proves LB2's
// single required parent field (feature_set_id, never product_id or
// feature_id).
func TestAttachLoadBearingDecisionHandler_MissingParent_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	decisions := &fakeDecisionStore{}

	rec := doGatedRequest(t, handlers.AttachLoadBearingDecisionHandler(decisions), sessions, sessionIDStr,
		`{"name": "LB1"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, decisions.gotScopeID)
}

// TestAttachLoadBearingDecisionHandler_NonexistentFeatureSet_Returns400
// proves writeStoreError maps store.ErrNotFound onto a 400.
func TestAttachLoadBearingDecisionHandler_NonexistentFeatureSet_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	decisions := &fakeDecisionStore{createErr: store.ErrNotFound}

	rec := doGatedRequest(t, handlers.AttachLoadBearingDecisionHandler(decisions), sessions, sessionIDStr,
		`{"feature_set_id": "`+uuid.New().String()+`", "name": "LB1"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestAttachLoadBearingDecisionHandler_NoSessionID_Rejected proves FR3's
// write gate sits in front of this handler too.
func TestAttachLoadBearingDecisionHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	decisions := &fakeDecisionStore{}

	rec := doGatedRequest(t, handlers.AttachLoadBearingDecisionHandler(decisions), sessions, "",
		`{"feature_set_id": "`+uuid.New().String()+`", "name": "LB1"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, decisions.gotScopeID)
}
