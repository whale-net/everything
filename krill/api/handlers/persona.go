// This file (FR 7a3906a3) is the Persona create endpoint: single parent
// Product.ID, taken from the request body's product_id field -- the same
// shape featureset.go's is, since a Persona is a product-level spec
// document (krill/ARCHITECTURE/02-capability-map-entries.md).
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// createPersonaRequest is CreatePersonaHandler's request body.
// Description is optional; ProductID is the single required parent
// reference (LB2 -- exactly one parent field, never a list).
type createPersonaRequest struct {
	ProductID   string  `json:"product_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// CreatePersonaHandler returns the create-Persona endpoint (FR 7a3906a3):
// POST /personas. Must be mounted behind RequireSession (gate.go).
func CreatePersonaHandler(personas store.PersonaStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createPersonaRequest
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

		persona, err := personas.Create(r.Context(), sess.ScopeID, productID, req.Name, req.Description)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: persona.ID.String()})
	}
}
