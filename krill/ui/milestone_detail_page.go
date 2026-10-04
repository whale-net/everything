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
// The header lives here, and so do the reads behind the cards under it: the
// outcome sentence and, for a milestone, the cuts taken from it with their
// progress (FR 0f1fb763), plus the shipped/unshipped split of what this
// container delivers (FR 1d16afe2), read through the same
// get_delivery_breakdown the MCP tool wraps. The properties rail is a
// separate task that builds on this page rather than beside it, which is why
// the handler resolves the container and hands it on rather than building the
// whole view in one pass.
package main

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
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

	page := buildMilestoneDetailPage(product, container)
	page.Outcome = milestoneDetailOutcomeOf(listing, container.ID)
	page.Milepebbles = app.milestoneDetailMilepebbles(r.Context(), product.ID, container)
	page.Rail = app.buildMilestoneDetailRailFor(r, product, listing, container)

	app.renderShell(w, r, container.Name, r.URL.Path,
		pages.MilestoneDetail(deliveryCardOn(page,
			app.milestoneDeliveryBreakdown(r.Context(), container))))
}

// buildMilestoneDetailRailFor reads the rail's two figures and assembles the
// rail.
//
// The reads are independent of each other and of the page, so both are
// always attempted and neither can take the container's own header down: the
// page answers the operator's question ("which work does this milestone
// hold?") even when the progress aggregate or the status register cannot be
// read, and the one row that lost its figure says so rather than reading as
// a count of zero.
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

// deliveryCardOn attaches a container's delivered scope to its own view
// model.
//
// It is a separate step rather than a third argument to
// buildMilestoneDetailPage because the header and the cards below it are
// separate reads: the header is a pure function of the product and the
// container, so its tests need no breakdown fixture, and a card that could
// only be had by reading one would drag the read into every header case.
func deliveryCardOn(page pages.MilestoneDetailPage, b *pages.DeliveryBreakdown) pages.MilestoneDetailPage {
	page.Delivery = b
	return page
}

// milestoneDeliveryBreakdown reads one container's delivered scope for the
// Delivery card, through the same Querier.GetDeliveryBreakdown the MCP tool
// get_delivery_breakdown wraps -- so the card and that tool can never
// describe the same container differently.
//
// Unlike the roadmap's per-container pass, this read is not gated on the
// container being partially complete. On its own detail page the delivered
// scope IS the container's subject, and a shipped milestone answering "these
// are the things it delivers, and all of them shipped" is a real answer, not
// a missing one. The disagreement flag inside deliveryBreakdownOf is what
// keeps that from reading as a contradiction.
//
// A read that fails is non-fatal in the same way the roadmap's is: the page
// still renders the header, the name, the status and both work links, and
// the one card that could not be read says so in its own place.
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

// buildMilestoneDetailPage assembles the header's view model from the one
// resolved container.
//
// It is a pure function of the product and the container, so the page's
// markup is checkable without a handler: the breadcrumb walk, the badge's
// input and both hrefs all come from the same container, and a test can
// assert the links without rendering through the shell.
//
// Nothing below the header is built here: the cards and the rail are filled
// in by the caller rather than here, because they come from reads this
// function does not make -- the outcome is the listing's own field for this
// container, the progress is a second read, the delivered scope is a third,
// and the rail needs two more. A builder that took all of it would no longer
// be the pure function the header's tests assert against.
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

// milestoneDetailOutcomeOf is the outcome sentence the delivery listing
// carries for this container.
//
// It reads the listing rather than deriving one, so the sentence here is
// the same sentence the Milestones table and the delivery page show: the
// outcome is the milestone's own prose, not a fact this page re-phrases.
// A milepebble's outcome is its own, never its parent's -- the card shows
// the container the operator opened.
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

// milestoneDetailMilepebbles is the Milepebbles card's rows: one per cut
// container, with its own status and its own progress.
//
// The figures come from the SINGLE-container progress read -- the scope
// naming this milestone -- rather than the all-containers scope the
// Milestones table uses. A detail page is about one container, and the
// single-container scope walks exactly this milestone and the cuts under
// it whatever their status, so a shipped milepebble's finished bar is
// still an answer here rather than an omission.
//
// A failed read costs the bars, not the card: every row still names its
// milepebble and links to its tasks, and each bar says the figures could
// not be read rather than reading "No tasks yet" -- which is a different
// fact, and the one an operator would act on wrongly.
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
			// The milepebble's OWN task list, scoped to the milepebble: an
			// operator following a cut's name is asking for the work cut
			// from THAT cut, and the parent's tasks answer a different
			// question.
			TasksPath: productTaskContainerHref(productID, tasksSuffix, taskContainer{
				ID:     mp.ID,
				Name:   mp.Name,
				Kind:   string(store.MilestoneKindMilepebble),
				Status: mp.Status,
			}),
			Status: string(mp.Status),
			// progressCell is the Milestones table's own builder, so a
			// milepebble's bar here carries the same accounting -- including
			// the same refusal to call an unread figure "No tasks yet".
			ProgressCell: progressCell(progress[mp.ID], mp.ID),
		}
		rows = append(rows, row)
	}
	return rows
}

// containerMilepebbleProgress reads one milestone's own progress aggregate
// and indexes it by each milepebble's OWN id.
//
// The index is by the milepebble ref, not the milestone ref beside it, for
// the reason milestoneProgressByID documents: the read carries the PARENT's
// id in Milestone for a milepebble's container, so a milestone-keyed index
// would collide every cut onto one entry and give them all the same bar.
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
