// This file (issue #2490, FR4) is the Load-Bearing Decision attach
// endpoint: single parent FeatureSet.ID (not Product, not Feature -- C2's
// "attached to the area it constrains"), taken from the request body's
// feature_set_id field. There is no separate create-then-attach step: per
// store/decision.go's LoadBearingDecisionStore.Create doc comment, Create
// against a FeatureSet id *is* the attach for a brand-new decision.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// attachLoadBearingDecisionRequest is
// AttachLoadBearingDecisionHandler's request body. FeatureSetID is the
// single required parent reference (LB2).
type attachLoadBearingDecisionRequest struct {
	FeatureSetID string  `json:"feature_set_id"`
	Name         string  `json:"name"`
	Body         *string `json:"body,omitempty"`
}

// AttachLoadBearingDecisionHandler returns the attach-Load-Bearing-Decision
// endpoint (FR4): POST /load-bearing-decisions. Must be mounted behind
// RequireSession (gate.go).
func AttachLoadBearingDecisionHandler(decisions store.LoadBearingDecisionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req attachLoadBearingDecisionRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		featureSetID, err := ParseUUIDField("feature_set_id", req.FeatureSetID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		decision, err := decisions.Create(r.Context(), sess.ScopeID, featureSetID, req.Name, req.Body)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: decision.ID.String()})
	}
}
