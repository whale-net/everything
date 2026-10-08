// Request-scoped product resolution. A prefixed URL's product is authoritative
// (bad pid is a 404); an un-prefixed URL must always land somewhere, so the two
// resolutions stay separate functions.
package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// lastViewedProductCookie is the hint an un-prefixed URL falls back to. A value
// no longer in scope is discarded, and a prefixed URL never consults it.
const lastViewedProductCookie = "krill_last_viewed_product"

// currentProductKey carries the resolved product so downstream chrome names the
// same product the page read. Only a resolver sets it, never the cookie.
type currentProductKey struct{}

// withCurrentProduct returns a request carrying p as the resolved
// current product.
func withCurrentProduct(r *http.Request, p store.Product) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), currentProductKey{}, p))
}

// currentProduct is the product resolved for this request; ok is false when
// none was (page outside the product area, or resolution failed).
func currentProduct(ctx context.Context) (store.Product, bool) {
	p, ok := ctx.Value(currentProductKey{}).(store.Product)
	return p, ok
}

// resolveProductFromPath resolves the {pid} segment and confirms it is in scope.
// A bad pid gets an in-shell 404; ok=false means the response is written.
func (app *App) resolveProductFromPath(w http.ResponseWriter, r *http.Request) (*http.Request, store.Product, bool) {
	pid, err := uuid.Parse(r.PathValue("pid"))
	if err != nil {
		app.renderProductScopeStatus(w, r, http.StatusNotFound, "Product not found",
			"That link does not name a product.")
		return r, store.Product{}, false
	}

	products, err := app.scopeProducts(r.Context())
	if err != nil {
		logger.Error("product scope read failed", "error", err)
		app.renderProductScopeStatus(w, r, http.StatusInternalServerError, "Could not load the product",
			"The product list could not be read. See the logs.")
		return r, store.Product{}, false
	}

	for _, p := range products {
		if p.ID == pid {
			return withCurrentProduct(r, p), p, true
		}
	}

	app.renderProductScopeStatus(w, r, http.StatusNotFound, "Product not found",
		"No product in your scope matches that id.")
	return r, store.Product{}, false
}

// resolveProductForUnprefixed returns the in-scope last-viewed product, else the
// first in scope; a stale cookie never fails. A zero Product means none exist.
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

// resolveUnprefixedProduct resolves the product, sets the cookie, and reports ok;
// ok=false means the response is written. An empty scope renders a 200 page.
func (app *App) resolveUnprefixedProduct(w http.ResponseWriter, r *http.Request) (*http.Request, store.Product, bool) {
	product, err := app.resolveProductForUnprefixed(r)
	if err != nil {
		logger.Error("product list read failed", "error", err)
		app.renderProductScopeStatus(w, r, http.StatusInternalServerError, "Could not load the product",
			"The product list could not be read. See the logs.")
		return r, store.Product{}, false
	}
	if product.ID == uuid.Nil {
		app.renderShell(w, r, "No products in this scope", r.URL.Path, pages.NoProductsInScope())
		return r, store.Product{}, false
	}
	setLastViewedProductCookie(w, product.ID)
	return withCurrentProduct(r, product), product, true
}

// rememberUnprefixedProduct records the current product in the cookie without
// ever writing a response, for pages whose body does not depend on the product.
func (app *App) rememberUnprefixedProduct(w http.ResponseWriter, r *http.Request) (*http.Request, store.Product) {
	product, err := app.resolveProductForUnprefixed(r)
	if err != nil {
		logger.Warn("could not resolve the current product for the last-viewed cookie", "path", r.URL.Path, "error", err)
		return r, store.Product{}
	}
	if product.ID == uuid.Nil {
		return r, store.Product{}
	}
	setLastViewedProductCookie(w, product.ID)
	return withCurrentProduct(r, product), product
}

// setLastViewedProductCookie records the resolved product for the next
// un-prefixed page.
func setLastViewedProductCookie(w http.ResponseWriter, pid uuid.UUID) {
	http.SetCookie(w, &http.Cookie{
		Name:     lastViewedProductCookie,
		Value:    pid.String(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// scopeProducts lists every current product in this deployment's sole scope.
func (app *App) scopeProducts(ctx context.Context) ([]store.Product, error) {
	products, err := app.spec.Products(ctx)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	return products, nil
}

// renderProductScopeStatus renders an error body through the shell with an
// explicit status, since templ components have no status concept.
func (app *App) renderProductScopeStatus(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	app.renderShellStatus(w, r, title, productsPath, pages.SpecStatus(pages.StatusPage{
		Title:  title,
		Detail: detail,
	}), status)
}
