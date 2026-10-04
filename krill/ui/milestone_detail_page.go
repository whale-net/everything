// The Milestone detail page: one container -- a milestone, or a milepebble
// cut from one -- named by its own status and linked to the work it holds
// (FR ef0a0ded).
//
// It is served at /products/{pid}/milestones/{mid}, which is where the
// Milestones table's names, the Overview's in-flight rows and the Board's
// lane headings already link (milestoneDetailHref). Both kinds of
// container answer at that one path, because a milepebble is its own
// milestone_ref row: the id in the URL is all that says which, and the
// delivery listing is what decides it.
//
// Only the header lives here. The cards below it -- the outcome and its
// milepebbles, the shipped/unshipped delivery tables -- and the properties
// rail are separate tasks that build on this page rather than beside it,
// which is why the handler resolves the container and hands it on rather
// than building the whole view in one pass.
package main

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// handleProductMilestoneDetail serves one container's detail page.
//
// The product is resolved from the URL before the container id is even
// looked at, so an out-of-scope link is already an in-shell 404 by the
// time this page's own rules run. The container is then resolved out of
// the product's OWN delivery listing rather than looked up by id: a
// milepebble and a milestone share the {mid} wildcard, and the listing is
// the only read here that can say which of them this id is -- and, at the
// same time, whether it belongs to this product at all.
func (app *App) handleProductMilestoneDetail(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)

	mid, err := uuid.Parse(r.PathValue("mid"))
	if err != nil {
		app.renderMilestoneDetailProblem(w, r, product, http.StatusNotFound,
			"That link does not name a milestone or milepebble.")
		return
	}

	// The listing is read unfiltered. A detail URL names one container and
	// carries no status, so filtering it would mean a container whose own
	// status fell outside the filter could not be opened by its own id --
	// and the detail is exactly where an operator goes to read a status
	// the table's current filter may be hiding.
	listing, err := app.spec.Delivery(r.Context(), product.ID, nil)
	if err != nil {
		logger.Error("milestone detail: delivery listing read failed",
			"product", product.ID.String(), "container", mid.String(), "error", err)
		app.renderMilestoneDetailProblem(w, r, product, http.StatusInternalServerError,
			"The product's milestones could not be read. See the logs.")
		return
	}

	container, found := resolveTaskContainer(listing, mid)
	if !found {
		app.renderMilestoneDetailProblem(w, r, product, http.StatusNotFound,
			"No milestone or milepebble with that id belongs to this product.")
		return
	}

	app.renderShell(w, r, container.Name, r.URL.Path,
		pages.MilestoneDetail(buildMilestoneDetailPage(product, container)))
}

// buildMilestoneDetailPage assembles the header's view model from the one
// resolved container.
//
// It is a pure function of the product and the container, so the page's
// markup is checkable without a handler: the breadcrumb walk, the badge's
// input and both hrefs all come from the same container, and a test can
// assert the links without rendering through the shell.
func buildMilestoneDetailPage(product store.Product, c taskContainer) pages.MilestoneDetailPage {
	return pages.MilestoneDetailPage{
		Product:   productHeaderOf(product),
		Path:      milestoneDetailHref(product.ID, c.ID),
		ID:        c.ID.String(),
		Kind:      c.Kind,
		Crumbs:    milestoneDetailCrumbsOf(productHeaderOf(product), product.ID, c),
		Title:     c.Name,
		Status:    string(c.Status),
		TasksPath: productTaskContainerHref(product.ID, tasksSuffix, c),
		BoardPath: productTaskContainerHref(product.ID, boardSuffix, c),
	}
}

// milestoneDetailCrumbsOf is the breadcrumb from the product down to this
// container: product -> Milestones -> the container's own name.
//
// The name carries no href because the operator is already on that page --
// the same rule the task detail's last crumb follows.
//
// A milepebble's PARENT is deliberately not a level here. The FR names
// three levels and this is the page the container's own children will
// list under it, so a milepebble's parent is reachable from the Milestones
// table and, once the milepebbles card lands, from the page it is a child
// of -- but not from a trail that would put a milestone between an operator
// and the milepebble they clicked.
func milestoneDetailCrumbsOf(product pages.ProductHeader, pid uuid.UUID, c taskContainer) []pages.MilestoneCrumb {
	crumbs := make([]pages.MilestoneCrumb, 0, 3)
	if product.Name != "" {
		crumbs = append(crumbs, pages.MilestoneCrumb{Label: product.Name, Href: product.Href})
	}
	crumbs = append(crumbs, pages.MilestoneCrumb{Label: "Milestones", Href: milestonesPath(pid)})
	return append(crumbs, pages.MilestoneCrumb{Label: c.Name})
}

// renderMilestoneDetailProblem answers a URL that names no container of
// this product, or a listing that could not be read.
//
// The 404 is the same answer the product-wide Tasks scope gives an id
// outside its product, and for the same reason: an id that belongs to
// another product must never render as that other product's milestone, and
// an empty page would read as a milestone that holds nothing rather than
// one that does not exist. The back link is the Milestones table, because
// that is the page an operator who followed a bad link came from and the
// one they can navigate on from.
func (app *App) renderMilestoneDetailProblem(w http.ResponseWriter, r *http.Request, product store.Product, status int, detail string) {
	app.renderShellStatus(w, r, "Not found", r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    "Not found",
		Detail:   detail,
		BackHref: milestonesPath(product.ID),
		BackText: "Back to milestones",
	}), status)
}
