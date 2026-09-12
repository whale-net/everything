// This file (issue #2490, FR2) is the Feature create endpoint: single
// parent FeatureSet.ID, taken from the request body's feature_set_id
// field -- never a list, never inferred.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// createFeatureRequest is CreateFeatureHandler's request body.
// FeatureSetID is the single required parent reference (LB2).
type createFeatureRequest struct {
	FeatureSetID string  `json:"feature_set_id"`
	Name         string  `json:"name"`
	Description  *string `json:"description,omitempty"`
}

// CreateFeatureHandler returns the create-Feature endpoint (FR2):
// POST /features. Must be mounted behind RequireSession (gate.go).
func CreateFeatureHandler(features store.FeatureStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createFeatureRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		featureSetID, err := parseUUIDField("feature_set_id", req.FeatureSetID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := requireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		feature, err := features.Create(r.Context(), sess.ScopeID, featureSetID, req.Name, req.Description)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, idResponse{ID: feature.ID.String()})
	}
}
