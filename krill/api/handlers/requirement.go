// This file (issue #2490, FR2) is the Requirement (FR or NFR) create
// endpoint: single parent Feature.ID, taken from the request body's
// feature_id field. kind discriminates FR vs NFR (store.RequirementKind) --
// there is no separate endpoint per kind, mirroring store/requirement.go's
// one-table-two-kinds design.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// createRequirementRequest is CreateRequirementHandler's request body.
// FeatureID is the single required parent reference (LB2). Kind must be
// "FR" or "NFR" -- there is no third kind in M1 (store.RequirementKind).
type createRequirementRequest struct {
	FeatureID string  `json:"feature_id"`
	Kind      string  `json:"kind"`
	Name      string  `json:"name"`
	Body      *string `json:"body,omitempty"`
}

// CreateRequirementHandler returns the create-Requirement endpoint (FR2):
// POST /requirements. Must be mounted behind RequireSession (gate.go).
func CreateRequirementHandler(requirements store.RequirementStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createRequirementRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		featureID, err := ParseUUIDField("feature_id", req.FeatureID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		kind := store.RequirementKind(req.Kind)
		switch kind {
		case store.RequirementKindFR, store.RequirementKindNFR:
		default:
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("kind must be %q or %q, got %q", store.RequirementKindFR, store.RequirementKindNFR, req.Kind))
			return
		}

		requirement, err := requirements.Create(r.Context(), sess.ScopeID, featureID, kind, req.Name, req.Body)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: requirement.ID.String()})
	}
}
