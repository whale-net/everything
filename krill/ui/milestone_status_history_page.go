// The Milestone status-history view: one container's status transitions, oldest
// first. Read through the same StatusHistory call as the detail rail's count, so
// the count and the rows always agree.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// handleProductMilestoneStatusHistory serves one container's status register. The
// container is resolved from this product's own listing so another product's id
// never renders as this one's, and a shared link resolves on its own.
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

// buildMilestoneStatusHistoryPage assembles the view model. A failed history read
// costs only the register; the header still renders.
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

// statusTransitionOf maps one store event onto its row. Order comes from the read
// (created_at ASC), not a local sort.
func statusTransitionOf(e store.MilestoneStatusEvent, now time.Time) pages.StatusTransition {
	return pages.StatusTransition{
		ID:         e.ID.String(),
		Status:     string(e.Status),
		Actor:      subjectLabel(e.CreatedByActing),
		OnBehalfOf: statusTransitionOnBehalfOf(e),
		At:         e.CreatedAt.UTC().Format(time.RFC3339),
		Relative:   relativeTime(e.CreatedAt, now),
		Note:       deref(e.Note),
	}
}

// statusTransitionOnBehalfOf is the on-behalf-of subject, or "" when it equals the
// actor; all subject fields are compared since any difference is a different person.
func statusTransitionOnBehalfOf(e store.MilestoneStatusEvent) string {
	if e.CreatedByOnBehalfOf == e.CreatedByActing {
		return ""
	}
	return subjectLabel(e.CreatedByOnBehalfOf)
}

// milestoneStatusHistoryCrumbsOf is the detail page's breadcrumb plus "Status
// history", with the container's name linked back to its detail.
func milestoneStatusHistoryCrumbsOf(product pages.ProductHeader, pid uuid.UUID, c taskContainer) []pages.MilestoneCrumb {
	crumbs := milestoneDetailCrumbsOf(product, pid, c)
	for i := range crumbs {
		if crumbs[i].Label == c.Name && crumbs[i].Href == "" {
			crumbs[i].Href = milestoneDetailHref(pid, c.ID)
		}
	}
	return append(crumbs, pages.MilestoneCrumb{Label: "Status history"})
}

// renderMilestoneStatusHistoryProblem renders the detail page's 404 for an id
// outside this product or an unreadable listing.
func (app *App) renderMilestoneStatusHistoryProblem(w http.ResponseWriter, r *http.Request, product store.Product, status int, detail string) {
	app.renderShellStatus(w, r, "Not found", r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    "Not found",
		Detail:   detail,
		BackHref: milestonesPath(product.ID),
		BackText: "Back to milestones",
	}), status)
}
