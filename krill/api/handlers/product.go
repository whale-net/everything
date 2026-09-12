// This file (issue #2490, FR1) is the Product create endpoint: a Product
// has no parent (store/product.go), so this is the one handler in this
// package with no parent field to validate.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/store"
)

// createProductRequest is CreateProductHandler's request body.
type createProductRequest struct {
	Name   string `json:"name"`
	Vision string `json:"vision"`
}

// CreateProductHandler returns the create-Product endpoint (FR1):
// POST /products. Must be mounted behind RequireSession (gate.go) --
// scope_id is always taken from the caller's session (LB1), never accepted
// as a request field.
func CreateProductHandler(products store.ProductStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createProductRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("vision", req.Vision); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		product, err := products.Create(r.Context(), sess.ScopeID, req.Name, req.Vision)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: product.ID.String()})
	}
}
