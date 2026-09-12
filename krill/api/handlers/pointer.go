// This file (issue #2496, FR20, C9) is the pointer-artifact create
// endpoint: mints krill's one thin GitHub issue for a Product, using the
// caller's scope's forge coordinates (LB1), and records it as a
// pointer_artifact row attributing both LB4 subjects. Unlike every other
// M1 create endpoint (product.go/featureset.go/feature.go/requirement.go/
// decision.go), this one talks to an external system (krill/forge) before
// it ever touches krill/store -- see CreatePointerArtifactHandler's doc
// comment for the order that keeps a caller error (an unknown or
// cross-scope product_id) from minting a spurious GitHub issue.
package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/whale-net/everything/krill/forge"
	"github.com/whale-net/everything/krill/store"
)

// createPointerArtifactRequest is CreatePointerArtifactHandler's request
// body: the single Product to mint a pointer issue for.
type createPointerArtifactRequest struct {
	ProductID string `json:"product_id"`
}

// createPointerArtifactResponse is CreatePointerArtifactHandler's response
// body -- the pointer_artifact row's surrogate id (LB2) plus the created
// GitHub issue's own coordinates, so a caller does not have to re-read the
// slice just to learn the issue number it just minted.
type createPointerArtifactResponse struct {
	ID          string `json:"id"`
	IssueNumber int    `json:"issue_number"`
	IssueURL    string `json:"issue_url"`
}

// CreatePointerArtifactHandler returns the pointer-artifact create
// endpoint (FR20): POST /pointer-artifacts. Must be mounted behind
// RequireSession (gate.go) -- scope_id and both LB4 subjects are always
// taken from the caller's session, never accepted as request fields.
//
// Order of operations: the target Product is read back and checked
// against the caller's own scope BEFORE forgeClient.CreateIssue is ever
// called, so a caller error (an unknown or cross-scope product_id) is
// rejected with 400 without minting a GitHub issue nobody asked for. A
// second Create against the same Product still fails afterwards (a
// pointer_artifact_product_idx unique-constraint violation, mapped to 409
// by writeStoreError) -- that check happens store-side, inside the same
// transaction as the INSERT, per PointerArtifactStore.Create's doc
// comment.
func CreatePointerArtifactHandler(products store.ProductStore, scopes store.ScopeStore, artifacts store.PointerArtifactStore, forgeClient forge.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createPointerArtifactRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		productID, err := parseUUIDField("product_id", req.ProductID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		product, err := products.GetCurrentByID(r.Context(), productID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && product.ScopeID != sess.ScopeID) {
			// A cross-scope product_id is rejected identically to an
			// unknown one -- mirrors every other M1 create handler's LB2
			// parentage rejection (never leaks whether a product_id
			// belongs to a different scope).
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("product_id: no current product row for id %s", productID))
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read product")
			return
		}

		scope, err := scopes.GetByID(r.Context(), sess.ScopeID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to read scope")
			return
		}

		title, body := forge.IssueContent(product.Name, product.ID.String())
		issueNumber, issueURL, err := forgeClient.CreateIssue(r.Context(), scope.RepoFullName, title, body)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to create pointer issue: %v", err))
			return
		}

		artifact, err := artifacts.Create(r.Context(), sess.ScopeID, productID, issueNumber, issueURL, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, createPointerArtifactResponse{
			ID:          artifact.ID.String(),
			IssueNumber: artifact.IssueNumber,
			IssueURL:    artifact.IssueURL,
		})
	}
}
