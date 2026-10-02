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
	// The last-viewed cookie is recorded only for a real page view. An
	// htmx request is a swap inside a page the operator is already on, and
	// a fragment response has no business moving where their next
	// un-prefixed page lands -- especially since a swap fires on every
	// control change, so it would rewrite the cookie repeatedly for a
	// navigation the operator never made.
	if r.Header.Get("HX-Request") == "" {
		setLastViewedProductCookie(w, product.ID)
	}

	scope, problem := parseProductTaskScope(r.URL.Query())
	if problem != productTaskScopeOK {
		app.renderProductTaskScopeProblem(w, r, product, view, resolvedProductTaskScope{}, problem)
		return
	}

	resolved, problem := app.resolveProductTaskScope(r.Context(), product.ID, scope)
	if problem != productTaskScopeOK {
		app.renderProductTaskScopeProblem(w, r, product, view, resolved, problem)
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
		Scope:      productTaskScopeControlOf(r.URL.Path, scope),
	}
	if scope.Parsed.Lane != nil {
		region.Lane = string(*scope.Parsed.Lane)
	}
	return region
}

// productTaskScopeControlOf builds the control both views render, from the
// resolved scope -- so the marked mode, the selected option and the read
// the rows come from are one answer rather than three.
//
// The two single-container modes reveal different pairs of selects, and
// the difference is deliberate rather than incidental:
//
//   - Milestone mode names one container, so its milestone select submits
//     container_id -- the same parameter the api and the store read.
//   - Milepebble mode names two: the milestone whose milepebbles are on
//     offer, and the chosen milepebble itself. Only the second is the
//     read's container, so only the second is container_id.
//
// Either way the operator chooses from a select of the product's own
// containers; nothing here is a field to type an id into.
func productTaskScopeControlOf(path string, scope resolvedProductTaskScope) pages.ProductTaskScopeControl {
	kind := scope.Parsed.Kind
	control := pages.ProductTaskScopeControl{
		Path:           path,
		Modes:          productTaskScopeModes(kind),
		MilestoneParam: pages.ProductTaskMilestoneQueryParam,
		Lane:           productTaskLaneOf(scope),
		OnlyStuck:      scope.Parsed.OnlyStuck,
	}
	if kind == store.ProductTaskScopeMilestone {
		control.MilestoneParam = pages.ProductTaskContainerQueryParam
	}
	if kind == store.ProductTaskScopeIncomplete {
		// The product-wide mode names no container, so it reveals no
		// select. The listing is not read here either, so there is nothing
		// to offer -- which is why this branch cannot be reached with
		// options attached.
		return control
	}

	control.Milestones = productTaskMilestoneOptionsOf(scope)
	control.ShowMilestone = len(control.Milestones) > 0
	if kind != store.ProductTaskScopeMilepebble {
		return control
	}

	control.Milepebbles = productTaskMilepebbleOptionsOf(scope)
	control.ShowMilepebble = len(control.Milepebbles) > 0
	return control
}

// productTaskMilestoneOptionsOf is the milestone select's options, marking
// the one this request resolved to. In milepebble mode the marked one is
// the resolved milepebble's PARENT -- the milestone whose milepebbles the
// second select offers -- rather than a container this scope does not
// name, so the control shows where the choices in the next select come
// from.
func productTaskMilestoneOptionsOf(scope resolvedProductTaskScope) []pages.ProductTaskContainerOption {
	out := make([]pages.ProductTaskContainerOption, 0, len(scope.Milestones))
	for _, m := range scope.Milestones {
		out = append(out, pages.ProductTaskContainerOption{
			ID:               m.ID.String(),
			Name:             m.Name,
			Selected:         m.ID == scope.Milestone.ID,
			OutOfScopeSuffix: outOfScopeSuffix(m.Status),
		})
	}
	return out
}

// productTaskMilepebbleOptionsOf is the milepebble select's options: the
// selected milestone's own children, marking the resolved one. Sourced
// from the resolved milestone rather than from scope.Container's
// Milepebbles, because the resolved container IS a milepebble in this mode
// and carries no children of its own.
func productTaskMilepebbleOptionsOf(scope resolvedProductTaskScope) []pages.ProductTaskContainerOption {
	out := make([]pages.ProductTaskContainerOption, 0, len(scope.Milestone.Milepebbles))
	for _, mp := range scope.Milestone.Milepebbles {
		out = append(out, pages.ProductTaskContainerOption{
			ID:               mp.ID.String(),
			Name:             mp.Name,
			Selected:         mp.ID == scope.Store.ContainerID,
			OutOfScopeSuffix: outOfScopeSuffix(mp.Status),
		})
	}
	return out
}

// productTaskNoMilepebblesLabel is the scope in prose for a milepebble
// scope whose milestone has nothing cut under it. It names the milestone
// the operator chose rather than the mode, because the mode's own claim --
// "one milepebble" -- is what could not be honoured.
func productTaskNoMilepebblesLabel(scope resolvedProductTaskScope) string {
	if scope.Milestone.Name == "" {
		return "No milepebbles here"
	}
	return "No milepebbles under " + scope.Milestone.Name
}

// outOfScopeSuffix marks a container the product-wide all-incomplete scope
// would not have included, judged by the store's own predicate rather than
// a UI copy of it. FR 7191dba1 still lets the operator pick such a
// container explicitly -- it shows its tasks whatever its status -- so it
// stays in the list; saying so is what keeps it from reading as a
// contradiction. Empty for a container that scope would include.
func outOfScopeSuffix(status store.MilestoneStatus) string {
	if store.IsIncompleteContainerStatus(status) {
		return ""
	}
	return " (outside the all-incomplete scope)"
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

// productTaskLaneOf is the lane filter's name for the control to carry
// through, empty for "every lane".
func productTaskLaneOf(scope resolvedProductTaskScope) string {
	if scope.Parsed.Lane == nil {
		return ""
	}
	return string(*scope.Parsed.Lane)
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
// The two empty outcomes are not failures at all -- they are ordinary
// answers, so each renders the region with nothing in it rather than an
// error. They are kept apart because their controls differ: a product with
// no milestones has nothing to put in a select, while a product whose
// milestones are simply not cut still has a milestone select, and that
// select is how the operator picks a cut one.
func (app *App) renderProductTaskScopeProblem(w http.ResponseWriter, r *http.Request, product store.Product, view string, resolved resolvedProductTaskScope, problem productTaskScopeProblem) {
	if problem == productTaskScopeNoContainers {
		// The modes are still offered even with nothing to scope to: the
		// operator's way out of this page is the control, not a back link,
		// so a control that disappeared here would be the one case where
		// they cannot leave.
		app.renderProductTaskRegion(w, r, view, pages.ProductTaskRegion{
			Product:    productHeaderOf(product),
			View:       view,
			Path:       r.URL.Path,
			ScopeLabel: "No milestones yet",
			Empty:      true,
			Scope: pages.ProductTaskScopeControl{
				Path:  r.URL.Path,
				Modes: productTaskScopeModes(""),
			},
		}, http.StatusOK)
		return
	}

	if problem == productTaskScopeNoMilepebbles {
		// Milepebble mode over a milestone with nothing cut under it. The
		// product does have milestones, and the milestone select -- which
		// is how the operator picks a cut one -- is exactly what must not
		// disappear here. So the control is built from the resolved scope,
		// which carries the product's milestones and the one named.
		app.renderProductTaskRegion(w, r, view, pages.ProductTaskRegion{
			Product:    productHeaderOf(product),
			View:       view,
			Path:       r.URL.Path,
			ScopeLabel: productTaskNoMilepebblesLabel(resolved),
			Empty:      true,
			Lane:       productTaskLaneOf(resolved),
			OnlyStuck:  resolved.Parsed.OnlyStuck,
			Scope:      productTaskScopeControlOf(r.URL.Path, resolved),
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
