// The sidebar's Product switcher (FR c4bd4bf8): the one control that
// moves an operator between products without ever typing an id.
//
// Two halves live here. productSwitcherData builds the select from the
// scope read the page already depends on, and handleProductSwitch is where
// the select's change lands. They are separate because the second must
// re-resolve the scope itself: the list in the HTML is a snapshot, and a
// product retired between render and click is not one to navigate to.
package main

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
)

// productSwitchPath is where the sidebar's select posts. It is a
// top-level path rather than a sub-path of productsPath because it names
// no product of its own -- it is the hop between two of them.
const productSwitchPath = "/product-switch"

// productSwitcherData builds the select for the page at r: every product
// in the caller's scope, with the current one marked.
//
// A scope the page cannot read yields no switcher rather than an empty
// one. An empty select would read as "this deployment has no products",
// which is the one thing it would not mean -- the same reason a failed
// list read must never render as an empty list elsewhere in the shell.
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

// handleProductSwitch lands the select's change: it sends the operator to
// the same area's list page under the product they picked.
//
// The picked id is parsed as a UUID and checked against the caller's own
// scope, so the control cannot be used to reach a product the scope does
// not hold. The area comes from the "from" the form carried, but only as
// a classification: productAreaHref builds the target from the validated
// id alone and never echoes "from" back, so a hand-edited "from" cannot
// turn this into an open redirect.
func (app *App) handleProductSwitch(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("product")))
	if err != nil {
		renderProductScopeStatus(w, r, http.StatusBadRequest, "Not a product",
			"That is not a product id.")
		return
	}

	products, err := app.scopeProducts(r.Context())
	if err != nil {
		logger.Error("product list read failed for the product switch", "error", err)
		renderProductScopeStatus(w, r, http.StatusInternalServerError, "Could not switch product",
			"The product list could not be read. See the logs.")
		return
	}
	if !containsProduct(products, productID) {
		renderProductScopeStatus(w, r, http.StatusNotFound, "Product not found",
			"No product in your scope matches that id.")
		return
	}

	// The pick becomes the last-viewed product, so the next un-prefixed
	// page follows the operator back out to the one they just chose.
	setLastViewedProductCookie(w, productID)
	http.Redirect(w, r, productAreaHref(r.URL.Query().Get("from"), productID), http.StatusFound)
}

// containsProduct reports whether id names one of these products.
func containsProduct(products []store.Product, id uuid.UUID) bool {
	for _, p := range products {
		if p.ID == id {
			return true
		}
	}
	return false
}

// productAreaHref is where a switch from the page at from lands for
// product pid.
//
// It classifies the page by *shape* and rebuilds the target from pid, so
// every id the old URL carried -- a milestone, a task, a design session --
// is dropped on the way. That is the whole point: a detail id from one
// product has no meaning under another, so a switch always lands on a
// list page.
//
// A page that names no area (the shell home, a legacy /ops view) or one
// this binary has no counterpart for falls back to the new product's
// overview, which is the one product-scoped list page every deployment
// has.
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

// productAreaShape is a URL path with every id segment replaced by a
// "*", so one table of shapes can classify every product-scoped page.
//
// Ids are recognised by being UUIDs rather than by counting segments off
// a known prefix: a page's shape is what says which area it is, and an id
// is exactly the part of a path that must not survive the switch. Being
// positional instead would mean re-deciding, for each new area, which
// segment holds the id -- and getting that wrong leaks it.
func productAreaShape(path string) []string {
	segments := pathSegments(path)
	for i, seg := range segments {
		if _, err := uuid.Parse(seg); err == nil {
			segments[i] = "*"
		}
	}
	return segments
}
