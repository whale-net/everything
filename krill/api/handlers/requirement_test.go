// Unit tests for CreateRequirementHandler (requirement.go, issue #2490's
// Testing section). No Postgres dependency -- fakeRequirementStore/
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

// TestCreateRequirementHandler_Success_BothKinds proves FR2: both an FR and
// an NFR can be created under a Feature, discriminated by kind, each
// returning its own surrogate id and carrying the session's scope_id.
func TestCreateRequirementHandler_Success_BothKinds(t *testing.T) {
	for _, kind := range []store.RequirementKind{store.RequirementKindFR, store.RequirementKindNFR} {
		t.Run(string(kind), func(t *testing.T) {
			sessions, scopeID, sessionIDStr := newTestSession(t)
			featureID := uuid.New()

			requirements := &fakeRequirementStore{}
			rec := doGatedRequest(t, handlers.CreateRequirementHandler(requirements), sessions, sessionIDStr,
				`{"feature_id": "`+featureID.String()+`", "kind": "`+string(kind)+`", "name": "Create a Product"}`)

			require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
			var resp idResponseBody
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.NotEmpty(t, resp.ID)
			assert.Equal(t, scopeID, requirements.gotScopeID)
			assert.Equal(t, featureID, requirements.gotFeatureID)
			assert.Equal(t, kind, requirements.gotKind)
		})
	}
}

// TestCreateRequirementHandler_InvalidKind_Rejected proves there is no third
// kind in M1: anything other than "FR"/"NFR" is a 400, the store is never
// called.
func TestCreateRequirementHandler_InvalidKind_Rejected(t *testing.T) {
	cases := []string{"", "fr", "REQUIREMENT", "C1"}

	for _, kind := range cases {
		t.Run("kind="+kind, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			requirements := &fakeRequirementStore{}
			rec := doGatedRequest(t, handlers.CreateRequirementHandler(requirements), sessions, sessionIDStr,
				`{"feature_id": "`+uuid.New().String()+`", "kind": "`+kind+`", "name": "Create a Product"}`)

			assert.Equal(t, http.StatusBadRequest, rec.Code, "kind %q: got body %q", kind, rec.Body.String())
			assert.Equal(t, uuid.Nil, requirements.gotScopeID, "kind %q: an invalid kind must never reach the store", kind)
		})
	}
}

// TestCreateRequirementHandler_MissingParent_Rejected proves LB2's single
// required parent field.
func TestCreateRequirementHandler_MissingParent_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	requirements := &fakeRequirementStore{}

	rec := doGatedRequest(t, handlers.CreateRequirementHandler(requirements), sessions, sessionIDStr,
		`{"kind": "FR", "name": "Create a Product"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, requirements.gotScopeID)
}
