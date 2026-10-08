// The Milestone detail page: one milestone or milepebble with its status, outcome,
// cuts, delivered scope and properties rail. Both kinds share one URL; the
// delivery listing decides which the id names.
package main

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// handleProductMilestoneDetail serves one container's detail page. The container
// is resolved from this product's own delivery listing, which both tells milestone
// from milepebble and proves the id belongs to this product.
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

	// Read unfiltered so a container whose status a table filter hides still opens by id.
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

	page := buildMilestoneDetailPage(product, container)
	page.Outcome = milestoneDetailOutcomeOf(listing, container.ID)
	page.Milepebbles = app.milestoneDetailMilepebbles(r.Context(), product.ID, container)
	page.Rail = app.buildMilestoneDetailRailFor(r, product, listing, container)

	app.renderShell(w, r, container.Name, r.URL.Path,
		pages.MilestoneDetail(deliveryCardOn(page,
			app.milestoneDeliveryBreakdown(r.Context(), container))))
}

// buildMilestoneDetailRailFor reads the rail's figures independently; a failed
// read marks only its own row, never reading as zero.
func (app *App) buildMilestoneDetailRailFor(r *http.Request, product store.Product, listing slice.DeliveryListing, c taskContainer) pages.MilestoneRail {
	reads := app.readMilestoneRailProgress(r.Context(), product.ID, c)
	if reads.ProgressErr != nil {
		logger.Error("milestone detail: progress read failed",
			"container", c.ID.String(), "error", reads.ProgressErr)
	}

	history := app.readMilestoneRailHistory(r.Context(), c)
	reads.History = history.History
	reads.HistoryErr = history.HistoryErr

	return buildMilestoneDetailRail(product.ID, milestoneRailEntry(listing, c.ID), c, reads)
}

// deliveryCardOn attaches a container's delivered scope, keeping the header
// builder free of the breakdown read.
func deliveryCardOn(page pages.MilestoneDetailPage, b *pages.DeliveryBreakdown) pages.MilestoneDetailPage {
	page.Delivery = b
	return page
}

// milestoneDeliveryBreakdown reads the Delivery card through the same query the
// MCP get_delivery_breakdown tool wraps, ungated by status. A failed read costs
// only this card.
func (app *App) milestoneDeliveryBreakdown(ctx context.Context, c taskContainer) *pages.DeliveryBreakdown {
	shipped, unshipped, err := app.spec.DeliveryBreakdown(ctx, c.ID)
	if err != nil {
		logger.Error("milestone detail: delivery breakdown read failed",
			"container", c.ID.String(), "error", err)
		return &pages.DeliveryBreakdown{
			Error: "This container's delivered scope could not be read. See the logs.",
		}
	}
	b := deliveryBreakdownOf(c.Status, shipped, unshipped)
	return &b
}

// buildMilestoneDetailPage builds the header from the resolved container alone,
// as a pure function; cards and rail come from separate reads the caller adds.
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

// milestoneDetailOutcomeOf is the container's own outcome sentence from the
// listing, never its parent's.
func milestoneDetailOutcomeOf(listing slice.DeliveryListing, id uuid.UUID) string {
	for _, m := range listing.Milestones {
		if m.ID == id {
			return deref(m.Outcome)
		}
		for _, mp := range m.Milepebbles {
			if mp.ID == id {
				return deref(mp.Outcome)
			}
		}
	}
	return ""
}

// milestoneDetailMilepebbles builds one row per cut using the single-container
// progress read, so shipped cuts still show bars. A failed read costs only the
// bars, which say so rather than "No tasks yet".
func (app *App) milestoneDetailMilepebbles(ctx context.Context, productID uuid.UUID, c taskContainer) []pages.MilestoneDetailMilepebble {
	if len(c.Milepebbles) == 0 {
		return nil
	}
	rows := make([]pages.MilestoneDetailMilepebble, 0, len(c.Milepebbles))
	progress, err := app.containerMilepebbleProgress(ctx, productID, c.ID)
	if err != nil {
		logger.Error("milestone detail: milepebble progress read failed",
			"product", productID.String(), "milestone", c.ID.String(), "error", err)
	}
	for _, mp := range c.Milepebbles {
		row := pages.MilestoneDetailMilepebble{
			ID:   mp.ID.String(),
			Name: mp.Name,
			// Work views scoped to the milepebble itself, not its parent.
			TasksPath: productTaskContainerHref(productID, tasksSuffix, taskContainer{
				ID:     mp.ID,
				Name:   mp.Name,
				Kind:   string(store.MilestoneKindMilepebble),
				Status: mp.Status,
			}),
			BoardPath: productTaskContainerHref(productID, boardSuffix, taskContainer{
				ID:     mp.ID,
				Name:   mp.Name,
				Kind:   string(store.MilestoneKindMilepebble),
				Status: mp.Status,
			}),
			Status: string(mp.Status),
			// progressCell is the Milestones table's builder, so accounting matches.
			ProgressCell: progressCell(progress[mp.ID], mp.ID),
		}
		rows = append(rows, row)
	}
	return rows
}

// containerMilepebbleProgress indexes a milestone's progress by milepebble id; the
// read carries the parent id in Milestone, so keying on it would collide.
func (app *App) containerMilepebbleProgress(ctx context.Context, productID, milestoneID uuid.UUID) (map[uuid.UUID]store.ContainerTaskProgress, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return nil, err
	}
	progress, err := app.tasks.SummarizeProductTaskProgress(ctx, store.ProductTaskProgressParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeMilestone, ContainerID: milestoneID},
	})
	if err != nil {
		return nil, err
	}
	byContainer := make(map[uuid.UUID]store.ContainerTaskProgress, len(progress.Containers))
	for _, c := range progress.Containers {
		if c.Milepebble != nil {
			byContainer[c.Milepebble.ID] = c
		}
	}
	return byContainer, nil
}

// milestoneDetailCrumbsOf is product -> Milestones -> container name (unlinked, as
// the current page). A milepebble's parent is deliberately not a level.
func milestoneDetailCrumbsOf(product pages.ProductHeader, pid uuid.UUID, c taskContainer) []pages.MilestoneCrumb {
	crumbs := make([]pages.MilestoneCrumb, 0, 3)
	if product.Name != "" {
		crumbs = append(crumbs, pages.MilestoneCrumb{Label: product.Name, Href: product.Href})
	}
	crumbs = append(crumbs, pages.MilestoneCrumb{Label: "Milestones", Href: milestonesPath(pid)})
	return append(crumbs, pages.MilestoneCrumb{Label: c.Name})
}

// renderMilestoneDetailProblem renders an in-shell 404 for an id outside this
// product or an unreadable listing, linking back to Milestones.
func (app *App) renderMilestoneDetailProblem(w http.ResponseWriter, r *http.Request, product store.Product, status int, detail string) {
	app.renderShellStatus(w, r, "Not found", r.URL.Path, pages.SpecStatus(pages.StatusPage{
		Title:    "Not found",
		Detail:   detail,
		BackHref: milestonesPath(product.ID),
		BackText: "Back to milestones",
	}), status)
}
