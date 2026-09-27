// This file (FR 7a3906a3) is the Non-Goal create endpoint: single parent
// Product.ID, taken from the request body's product_id field, plus the
// `kind` discriminator separating PRODUCT.md's two Non-goals buckets
// (permanent, deferred) -- one endpoint, not one per kind, mirroring
// store/nongoal.go's one-table-two-kinds design.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// createNonGoalRequest is CreateNonGoalHandler's request body. ProductID
// is the single required parent reference (LB2); Kind must be "permanent"
// or "deferred" -- there is no third kind (store.NonGoalKind). Kind is
// validated here rather than by a JSON-Schema enum, the same house pattern
// requirement.go's FR/NFR discriminator uses.
type createNonGoalRequest struct {
	ProductID string  `json:"product_id"`
	Kind      string  `json:"kind"`
	Name      string  `json:"name"`
	Body      *string `json:"body,omitempty"`
}

// CreateNonGoalHandler returns the create-Non-Goal endpoint (FR 7a3906a3):
// POST /non-goals. Must be mounted behind RequireSession (gate.go).
func CreateNonGoalHandler(nonGoals store.NonGoalStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createNonGoalRequest
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

		kind := store.NonGoalKind(req.Kind)
		switch kind {
		case store.NonGoalKindPermanent, store.NonGoalKindDeferred:
		default:
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("kind must be %q or %q, got %q", store.NonGoalKindPermanent, store.NonGoalKindDeferred, req.Kind))
			return
		}

		nonGoal, err := nonGoals.Create(r.Context(), sess.ScopeID, productID, kind, req.Name, req.Body)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: nonGoal.ID.String()})
	}
}
