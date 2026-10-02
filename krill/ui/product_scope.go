// The request-scoped product resolver behind every product-scoped URL
// (FR c4bd4bf8). A page reached under productsPath names its product in
// the path; a page reached without one -- the legacy /ops/* routes, the
// pre-redesign task and board URLs, "/", and the non-product-scoped
// credentials page -- resolves one server-side. Either way the caller
// never types or pastes a product id.
//
// The two resolutions are deliberately separate functions rather than one
// with a flag: a prefixed URL's product is authoritative and a bad one is
// a 404, while an un-prefixed URL has no such claim and must always land
// somewhere. Merging them would let the un-prefixed case inherit the 404
// and leave a legacy link broken whenever the cookie went stale.
package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// lastViewedProductCookie names the non-authoritative hint an
// un-prefixed URL falls back to. It is a hint, never an authority: a
// value naming a product no longer in scope is discarded, and a prefixed
// URL never consults it.
const lastViewedProductCookie = "krill_last_viewed_product"

// resolveProductFromPath resolves the {pid} segment of a product-scoped
// URL and confirms it is one of the products in this deployment's sole
// scope.
//
// A pid that is malformed, unknown, or in another scope is answered with
// an in-shell 404 rather than a bare http.Error: a link an operator
// followed has to land inside the shell they can navigate back out of.
// ok=false means the response has already been written.
func (app *App) resolveProductFromPath(w http.ResponseWriter, r *http.Request) (store.Product, bool) {
	pid, err := uuid.Parse(r.PathValue("pid"))
	if err != nil {
		renderProductScopeStatus(w, r, http.StatusNotFound, "Product not found",
			"That link does not name a product.")
		return store.Product{}, false
	}

	products, err := app.scopeProducts(r.Context())
	if err != nil {
		logger.Error("product scope read failed", "error", err)
		renderProductScopeStatus(w, r, http.StatusInternalServerError, "Could not load the product",
			"The product list could not be read. See the logs.")
		return store.Product{}, false
	}

	for _, p := range products {
		if p.ID == pid {
			return p, true
		}
	}

	renderProductScopeStatus(w, r, http.StatusNotFound, "Product not found",
		"No product in your scope matches that id.")
	return store.Product{}, false
}

// resolveProductForUnprefixed resolves the product for a URL that does
// not name one: the last-viewed cookie's value when it is still in scope,
// otherwise the first product in scope. It never fails on a missing or
// stale cookie -- those are the ordinary case for a legacy link, and
// answering 404 there would break every one of them.
//
// A zero Product with a nil error means the scope holds none, which the
// caller renders as an empty state rather than a 404.
func (app *App) resolveProductForUnprefixed(r *http.Request) (store.Product, error) {
	products, err := app.scopeProducts(r.Context())
	if err != nil {
		return store.Product{}, err
	}
	if len(products) == 0 {
		return store.Product{}, nil
	}

	if c, err := r.Cookie(lastViewedProductCookie); err == nil {
		if hinted, err := uuid.Parse(c.Value); err == nil {
			for _, p := range products {
				if p.ID == hinted {
					return p, nil
				}
			}
		}
	}

	return products[0], nil
}

// setLastViewedProductCookie records the product a prefixed URL resolved,
// so the operator's next un-prefixed page lands on the product they were
// just looking at. It is written on every prefixed page render and never
// read by one.
func setLastViewedProductCookie(w http.ResponseWriter, pid uuid.UUID) {
	http.SetCookie(w, &http.Cookie{
		Name:     lastViewedProductCookie,
		Value:    pid.String(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// scopeProducts lists every current product in this deployment's sole
// scope. A browser cannot pick a scope, so the resolution is always the
// same one the spec reader's Products makes -- this binary has no other.
func (app *App) scopeProducts(ctx context.Context) ([]store.Product, error) {
	products, err := app.spec.Products(ctx)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	return products, nil
}

// renderProductScopeStatus renders the product area's error body through
// the shell with an explicit status. The status lives at this seam rather
// than in the component because templ components are body-writers with no
// status concept -- the same seam spec_page.go's renderSpecStatus uses.
func renderProductScopeStatus(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	renderShellStatus(w, r, title, productsPath, pages.SpecStatus(pages.StatusPage{
		Title:  title,
		Detail: detail,
	}), status)
}
