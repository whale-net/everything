// The two product-scoped routes the product-wide task read serves:
// GET /products/{pid}/tasks and GET /products/{pid}/board. Both are two
// views of one scope (FR ab5f4936), so both go through the same handler
// with the view's own name.
//
// What they render today is the scope the URL resolved to and the total
// behind it, not rows: the read layer they sit on is the whole of what has
// shipped, and the region's own template says so rather than rendering a
// table with nothing in it.
package main

import (
	"net/http"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// handleProductTasks serves the product-wide Tasks region.
func (app *App) handleProductTasks(w http.ResponseWriter, r *http.Request) {
	app.serveProductTaskRegion(w, r, "Tasks")
}

// handleProductBoard serves the Board view of the same scope.
func (app *App) handleProductBoard(w http.ResponseWriter, r *http.Request) {
	app.serveProductTaskRegion(w, r, "Board")
}

// serveProductTaskRegion is the one handler behind both views: resolve the
// product, parse and resolve the scope, read the page and its total, and
// render the region -- or the named failure that scope resolved to.
//
// Every failure has a defined answer here rather than at each call site,
// and each one differs by request mode. A full-page request gets the
// in-shell status page the scope deserves; an htmx request gets 200 with
// the same message inline, because htmx does not swap on a non-2xx and a
// status-coded body would leave the operator clicking Refresh with nothing
// to read (spec_page.go's renderSpecError rule, which handleTaskList
// already follows).
func (app *App) serveProductTaskRegion(w http.ResponseWriter, r *http.Request, view string) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)

	scope, problem := parseProductTaskScope(r.URL.Query())
	if problem != productTaskScopeOK {
		app.renderProductTaskScopeProblem(w, r, product, view, problem)
		return
	}

	resolved, problem := app.resolveProductTaskScope(r.Context(), product.ID, scope)
	if problem != productTaskScopeOK {
		app.renderProductTaskScopeProblem(w, r, product, view, problem)
		return
	}

	region := productTaskRegionOf(r, product, view, resolved)
	page, err := app.readProductTasks(r.Context(), product.ID, resolved)
	if err != nil {
		// A failed read is a 500 the operator can act on, and an empty
		// region would read as "this scope has no tasks" -- the one answer
		// it must never be mistaken for.
		logger.Error("product task read failed", "product", product.ID.String(), "error", err)
		region.Error = "The tasks could not be read. See the logs."
		app.renderProductTaskRegion(w, r, view, region, http.StatusInternalServerError)
		return
	}
	region.Total = page.Total
	region.Empty = len(page.Rows) == 0
	app.renderProductTaskRegion(w, r, view, region, http.StatusOK)
}

// productTaskRegionOf assembles the region's view model from the resolved
// scope alone -- no rows -- so what the page says about itself cannot
// disagree with the scope the read was built from.
func productTaskRegionOf(r *http.Request, product store.Product, view string, scope resolvedProductTaskScope) pages.ProductTaskRegion {
	region := pages.ProductTaskRegion{
		Product:    productHeaderOf(product),
		View:       view,
		Path:       r.URL.Path,
		ScopeLabel: productTaskScopeLabelOf(scope),
		OnlyStuck:  scope.Parsed.OnlyStuck,
		ScopeModes: productTaskScopeModes(scope.Parsed.Kind),
	}
	if scope.Parsed.Lane != nil {
		region.Lane = string(*scope.Parsed.Lane)
	}
	region.Milestones, region.Milepebbles = productTaskContainerOptionsOf(scope)
	return region
}

// productTaskScopeLabelOf is the scope in prose: the product-wide mode's
// own name, or the resolved container's.
func productTaskScopeLabelOf(scope resolvedProductTaskScope) string {
	if scope.Parsed.Kind == store.ProductTaskScopeIncomplete {
		return "All incomplete milestones"
	}
	return scope.Container.Name
}

// productTaskScopeModes is the three modes in the order the control offers
// them, with active marked. active is uuid.Nil's absence expressed as the
// product-wide default, so a caller that has no resolved scope still gets
// the same three names rather than a second list.
func productTaskScopeModes(active store.ProductTaskScopeKind) []pages.ProductTaskScopeLabel {
	if active == "" {
		active = store.ProductTaskScopeIncomplete
	}
	return []pages.ProductTaskScopeLabel{
		{Mode: string(store.ProductTaskScopeIncomplete), Label: "All incomplete milestones", Active: active == store.ProductTaskScopeIncomplete},
		{Mode: string(store.ProductTaskScopeMilestone), Label: "Milestone", Active: active == store.ProductTaskScopeMilestone},
		{Mode: string(store.ProductTaskScopeMilepebble), Label: "Milepebble", Active: active == store.ProductTaskScopeMilepebble},
	}
}

// productTaskContainerOptionsOf lists the product's own containers as
// selectable options, marking the one this scope resolved to. The
// milepebble list is the selected milestone's children, so it is non-empty
// only in milestone scope -- which is exactly the pair of selects the
// control needs when it ships.
func productTaskContainerOptionsOf(scope resolvedProductTaskScope) ([]pages.ProductTaskContainerOption, []pages.ProductTaskContainerOption) {
	var milestones []pages.ProductTaskContainerOption
	for _, c := range scope.Milestones {
		milestones = append(milestones, pages.ProductTaskContainerOption{
			ID:       c.ID.String(),
			Name:     c.Name,
			Selected: scope.Parsed.Kind == store.ProductTaskScopeMilestone && c.ID == scope.Store.ContainerID,
		})
	}
	var milepebbles []pages.ProductTaskContainerOption
	for _, c := range scope.Container.Milepebbles {
		milepebbles = append(milepebbles, pages.ProductTaskContainerOption{
			ID:       c.ID.String(),
			Name:     c.Name,
			Selected: scope.Parsed.Kind == store.ProductTaskScopeMilepebble && c.ID == scope.Store.ContainerID,
		})
	}
	return milestones, milepebbles
}

// renderProductTaskRegion writes the region, as a bare fragment for an
// htmx request and inside the shell otherwise. The nav key is the
// request's own path, which is the Tasks or Board item's own URL -- the
// two views are separate nav items, so each marks itself.
func (app *App) renderProductTaskRegion(w http.ResponseWriter, r *http.Request, view string, region pages.ProductTaskRegion, status int) {
	body := pages.ProductTasks(region)
	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, body)
		return
	}
	app.renderShellStatus(w, r, view, r.URL.Path, body, status)
}

// renderProductTaskScopeProblem maps a named scope problem onto the two
// answers a caller can receive: the in-shell status page for a browser, and
// the same sentence inline for htmx.
//
// A container outside the product is a 404, never another product's rows
// and never an empty page that would read as "this milestone has no work".
// A product with no container of the requested kind is not a failure at
// all -- it is an ordinary empty result, so it renders the region with
// nothing in it rather than an error.
func (app *App) renderProductTaskScopeProblem(w http.ResponseWriter, r *http.Request, product store.Product, view string, problem productTaskScopeProblem) {
	if problem == productTaskScopeNoContainers {
		app.renderProductTaskRegion(w, r, view, pages.ProductTaskRegion{
			Product:    productHeaderOf(product),
			View:       view,
			Path:       r.URL.Path,
			ScopeLabel: "No milestones yet",
			Empty:      true,
			ScopeModes: productTaskScopeModes(""),
		}, http.StatusOK)
		return
	}

	if r.Header.Get("HX-Request") != "" {
		renderFragment(w, r, pages.ProductTasksInlineError(pages.ProductTasksAnchor,
			productTaskScopeProblemMessage(problem)))
		return
	}

	status := http.StatusBadRequest
	if problem == productTaskScopeNotFound {
		status = http.StatusNotFound
	}
	if problem == productTaskScopeUnreadable {
		status = http.StatusInternalServerError
	}
	app.renderShellStatus(w, r, view, r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    view + " scope",
		Detail:   productTaskScopeProblemMessage(problem),
		BackHref: productHref(product.ID, overviewSuffix),
		BackText: "Back to overview",
	}), status)
}

// productTaskScopeProblemMessage is each named problem in the operator's
// words -- never the store's Go error string, which names an internal
// package and tells the operator nothing they can act on.
func productTaskScopeProblemMessage(problem productTaskScopeProblem) string {
	switch problem {
	case productTaskScopeInvalid:
		return "That scope or filter is not one this page understands."
	case productTaskScopeNotFound:
		return "No milestone or milepebble with that id belongs to this product."
	case productTaskScopeUnreadable:
		return "The product's milestones could not be read. See the logs."
	}
	return "The scope could not be resolved."
}
