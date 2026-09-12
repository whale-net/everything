// This file (issue #2490, FR2) is the FeatureSet create endpoint: single
// parent Product.ID, taken from the request body's product_id field --
// never a list, never inferred.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// createFeatureSetRequest is CreateFeatureSetHandler's request body.
// Description is optional; ProductID is the single required parent
// reference (LB2 -- exactly one parent field, never a list).
type createFeatureSetRequest struct {
	ProductID   string  `json:"product_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// CreateFeatureSetHandler returns the create-FeatureSet endpoint (FR2):
// POST /feature-sets. Must be mounted behind RequireSession (gate.go).
func CreateFeatureSetHandler(featureSets store.FeatureSetStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createFeatureSetRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		productID, err := ParseUUIDField("product_id", req.ProductID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		featureSet, err := featureSets.Create(r.Context(), sess.ScopeID, productID, req.Name, req.Description)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: featureSet.ID.String()})
	}
}
