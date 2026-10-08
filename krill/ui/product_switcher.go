// The sidebar's Product switcher. handleProductSwitch re-resolves the scope
// because the rendered list is a snapshot and a product may retire before the click.
package main

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
)

// productSwitchPath is top-level because it names no product of its own.
const productSwitchPath = "/product-switch"

// productSwitcherData builds the select for every product in the caller's scope.
// An unreadable scope yields no switcher, never an empty one that implies "no products".
func (app *App) productSwitcherData(r *http.Request) *components.ProductSwitcherData {
	products, err := app.scopeProducts(r.Context())
	if err != nil {
		logger.Warn("product list read failed for the sidebar switcher", "path", r.URL.Path, "error", err)
		return nil
	}
	if len(products) == 0 {
		return nil
	}

	current, _ := currentProduct(r.Context())
	options := make([]components.ProductOption, 0, len(products))
	for _, p := range products {
		options = append(options, components.ProductOption{
			ID:       p.ID.String(),
			Name:     p.Name,
			Selected: p.ID == current.ID,
		})
	}

	return &components.ProductSwitcherData{
		Action:  productSwitchPath,
		From:    r.URL.Path,
		Options: options,
	}
}

// handleProductSwitch redirects to the same area under the picked product. The id
// is checked against the caller's scope, and "from" is only classified, never
// echoed back, so it cannot become an open redirect.
func (app *App) handleProductSwitch(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("product")))
	if err != nil {
		app.renderProductScopeStatus(w, r, http.StatusBadRequest, "Not a product",
			"That is not a product id.")
		return
	}

	products, err := app.scopeProducts(r.Context())
	if err != nil {
		logger.Error("product list read failed for the product switch", "error", err)
		app.renderProductScopeStatus(w, r, http.StatusInternalServerError, "Could not switch product",
			"The product list could not be read. See the logs.")
		return
	}
	if !containsProduct(products, productID) {
		app.renderProductScopeStatus(w, r, http.StatusNotFound, "Product not found",
			"No product in your scope matches that id.")
		return
	}

	// The pick becomes the last-viewed product for the next un-prefixed page.
	setLastViewedProductCookie(w, productID)
	http.Redirect(w, r, productAreaHref(r.URL.Query().Get("from"), productID), http.StatusFound)
}

func containsProduct(products []store.Product, id uuid.UUID) bool {
	for _, p := range products {
		if p.ID == id {
			return true
		}
	}
	return false
}

// productAreaHref maps the page at from to the matching list page for pid,
// dropping every detail id since it has no meaning under another product.
// Unknown areas fall back to the product overview.
func productAreaHref(from string, pid uuid.UUID) string {
	switch strings.Join(productAreaShape(from), "/") {
	case "products/*/needs-attention":
		return productHref(pid, needsAttentionSuffix)
	case "products/*/tasks",
		"spec/products/*/milestones/*/tasks",
		"spec/products/*/milestones/*/tasks/*":
		return productHref(pid, tasksSuffix)
	case "products/*/board", "spec/products/*/milestones/*/board":
		return productHref(pid, boardSuffix)
	case "products/*/milestones", "products/*/milestones/*":
		return productHref(pid, milestonesSuffix)
	case "spec/products/*/decisions":
		return decisionsPath(pid)
	case "spec/products/*/personas":
		return personasPath(pid)
	case "spec/products/*/non-goals":
		return nonGoalsPath(pid)
	case "spec/products/*/delivery":
		return deliveryPath(pid)
	case "design/products/*/design-sessions", "design/design-sessions/*":
		return designProductSessionsPath(pid)
	case "spec/products/*":
		return productPath(pid)
	default:
		return productHref(pid, overviewSuffix)
	}
}

// productAreaShape replaces every UUID segment with "*" so ids are recognised by
// form rather than position and cannot leak across a switch.
func productAreaShape(path string) []string {
	segments := pathSegments(path)
	for i, seg := range segments {
		if _, err := uuid.Parse(seg); err == nil {
			segments[i] = "*"
		}
	}
	return segments
}
