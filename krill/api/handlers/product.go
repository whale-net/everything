// This file (issue #2490, FR1) is the Product create endpoint: a Product
// has no parent (store/product.go), so this is the one handler in this
// package with no parent field to validate. It also holds the ungated
// Product list endpoint (issue #2941), the discovery entry point every
// get_*_slice read needs a Product id from.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// ProductSummary is one entry of ListProductsResponse.Products. Exported
// so krill/mcp/tools' list_products tool returns this exact shape (LB7).
type ProductSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Vision string `json:"vision"`
}

// ListProductsResponse is ListProductsHandler's response body.
type ListProductsResponse struct {
	Products []ProductSummary `json:"products"`
}

// NewListProductsResponse maps store.ProductStore.ListCurrentByScope's
// result onto its wire shape -- shared by ListProductsHandler and the
// list_products MCP tool.
func NewListProductsResponse(products []store.Product) ListProductsResponse {
	summaries := make([]ProductSummary, len(products))
	for i, p := range products {
		summaries[i] = ProductSummary{ID: p.ID.String(), Name: p.Name, Vision: p.Vision}
	}
	return ListProductsResponse{Products: summaries}
}

// ListProductsHandler returns the Product list endpoint: GET
// /products?scope_id=..., ungated like every other read endpoint in this
// package. A Product has no parent entity to resolve scope from, so
// scope_id is a required query parameter (console.go's same posture).
func ListProductsHandler(products store.ProductStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scopeID, err := uuid.Parse(r.URL.Query().Get(scopeIDQueryParam))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "scope_id: invalid or missing UUID")
			return
		}

		list, err := products.ListCurrentByScope(r.Context(), scopeID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, NewListProductsResponse(list))
	}
}

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
