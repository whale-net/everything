// The Milestone status-history view (FR 9a6e7924): one container's whole
// status-transition register, oldest first, each transition carrying its own
// status badge, actor, relative instant and note.
//
// It is served at /products/{pid}/milestones/{mid}/status-history -- the URL
// the Milestone detail's properties rail links to (milestoneStatusHistoryHref,
// milestoneStatusHistorySuffix). Both kinds of container answer there, for
// the same reason they share the detail's URL: a milepebble is its own
// milestone_ref row, and the id in the path is all that says which.
//
// The register is read through specReadClient.StatusHistory -- the same
// get_milestone_status_history read the rail's count comes from -- so the
// number an operator follows off the detail and the rows they land on are two
// renderings of one answer. Nothing here re-reads or re-counts.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// handleProductMilestoneStatusHistory serves one container's status
// register.
//
// The product is resolved from the URL before the container id is looked at,
// and the container itself is resolved out of the product's OWN delivery
// listing for the reason the detail page does the same: the id is only
// meaningful against this product's containers, and an id belonging to
// another product must never render as though it were this one's. The
// listing is a second read on this page rather than a parameter the rail
// passed along, because a URL is what an operator bookmarks and shares, and a
// copied status-history link has to resolve its own container.
func (app *App) handleProductMilestoneStatusHistory(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)

	mid, err := uuid.Parse(r.PathValue("mid"))
	if err != nil {
		app.renderMilestoneStatusHistoryProblem(w, r, product, http.StatusNotFound,
			"That link does not name a milestone or milepebble.")
		return
	}

	listing, err := app.spec.Delivery(r.Context(), product.ID, nil)
	if err != nil {
		logger.Error("milestone status history: delivery listing read failed",
			"product", product.ID.String(), "container", mid.String(), "error", err)
		app.renderMilestoneStatusHistoryProblem(w, r, product, http.StatusInternalServerError,
			"The product's milestones could not be read. See the logs.")
		return
	}
	container, found := resolveTaskContainer(listing, mid)
	if !found {
		app.renderMilestoneStatusHistoryProblem(w, r, product, http.StatusNotFound,
			"No milestone or milepebble with that id belongs to this product.")
		return
	}

	page := app.buildMilestoneStatusHistoryPage(r.Context(), product, container)
	app.renderShell(w, r, "Status history", r.URL.Path, pages.MilestoneStatusHistory(page))
}

// buildMilestoneStatusHistoryPage assembles the view model: the header the
// rail's own link sits beside, and the register as read.
//
// The header is a pure function of the product and the container, so the
// register is the only read this makes -- and a read that failed costs the
// register, not the page. An operator who followed the rail's link to a
// milestone whose history could not be read still learns which milestone they
// are on and can go back.
func (app *App) buildMilestoneStatusHistoryPage(ctx context.Context, product store.Product, c taskContainer) pages.MilestoneStatusHistoryPage {
	header := productHeaderOf(product)
	page := pages.MilestoneStatusHistoryPage{
		Product:       header,
		Path:          milestoneStatusHistoryHref(product.ID, c.ID),
		ID:            c.ID.String(),
		Kind:          c.Kind,
		ContainerName: c.Name,
		Title:         "Status history",
		DetailPath:    milestoneDetailHref(product.ID, c.ID),
		Crumbs:        milestoneStatusHistoryCrumbsOf(header, product.ID, c),
	}

	events, err := app.spec.StatusHistory(ctx, c.ID)
	if err != nil {
		logger.Error("milestone status history: status history read failed",
			"container", c.ID.String(), "error", err)
		page.Error = "This milestone's status history could not be read. See the logs."
		return page
	}
	now := time.Now()
	page.Transitions = make([]pages.StatusTransition, 0, len(events))
	for _, e := range events {
		page.Transitions = append(page.Transitions, statusTransitionOf(e, now))
	}
	return page
}

// statusTransitionOf maps one store event onto its row.
//
// now is passed rather than read from the clock so the rendered ages are a
// function of the inputs -- the same rule relativeTime documents. The rows
// come out in the read's order: StatusHistory wraps ListTransitions, which
// orders by created_at ASC, so oldest-first is the read's own guarantee rather
// than a sort this view could get wrong.
func statusTransitionOf(e store.MilestoneStatusEvent, now time.Time) pages.StatusTransition {
	return pages.StatusTransition{
		ID:          e.ID.String(),
		Status:      string(e.Status),
		Actor:       subjectLabel(e.CreatedByActing),
		OnBehalfOf:  statusTransitionOnBehalfOf(e),
		At:          e.CreatedAt.UTC().Format(time.RFC3339),
		Relative:    relativeTime(e.CreatedAt, now),
		Note:        deref(e.Note),
	}
}

// statusTransitionOnBehalfOf is the on-behalf-of subject, or "" when it is
// the actor's own self.
//
// The pair is equal for the ordinary case -- an operator acting as themselves
// -- and the equality is what makes the second line worth suppressing. It is
// compared on all three fields, because two subjects with the same sub and
// different kinds, or the same kind from different issuers, are different
// people as far as this register is concerned.
func statusTransitionOnBehalfOf(e store.MilestoneStatusEvent) string {
	if e.CreatedByOnBehalfOf == e.CreatedByActing {
		return ""
	}
	return subjectLabel(e.CreatedByOnBehalfOf)
}

// milestoneStatusHistoryCrumbsOf is the breadcrumb from the product down to
// this page: product -> Milestones -> the container's name -> Status history.
//
// It is the detail page's own walk with one difference: the container's name
// carries its detail href here, where on the detail itself it carries none.
// On that page the name is where the operator already is; on this one it is
// the step back out, and a crumb that names a page without linking to it is
// the one dead level in a trail whose whole job is climbing back up.
func milestoneStatusHistoryCrumbsOf(product pages.ProductHeader, pid uuid.UUID, c taskContainer) []pages.MilestoneCrumb {
	crumbs := milestoneDetailCrumbsOf(product, pid, c)
	for i := range crumbs {
		if crumbs[i].Label == c.Name && crumbs[i].Href == "" {
			crumbs[i].Href = milestoneDetailHref(pid, c.ID)
		}
	}
	return append(crumbs, pages.MilestoneCrumb{Label: "Status history"})
}

// renderMilestoneStatusHistoryProblem answers a URL that names no container
// of this product, or a listing that could not be read.
//
// It is the detail page's own 404 with the detail page's own sentences, for
// the same reason: an id that belongs to another product must never render as
// this product's milestone, and the operator who followed a bad link came
// from the Milestones table.
func (app *App) renderMilestoneStatusHistoryProblem(w http.ResponseWriter, r *http.Request, product store.Product, status int, detail string) {
	app.renderShellStatus(w, r, "Not found", r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    "Not found",
		Detail:   detail,
		BackHref: milestonesPath(product.ID),
		BackText: "Back to milestones",
	}), status)
}