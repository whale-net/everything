// The Overview: the shell's home page, answering the two questions an
// operator opens it to ask -- is anything stuck, and which milestone is in
// flight (FR c3e1c276).
//
// It is served at two URLs, /products/{pid}/overview and the un-prefixed
// "/", which resolves a product and then serves the same page. The frame
// lives here, along with the Milestones-in-flight and Needs-attention
// panels; the stat tiles are built in overview_tiles.go.
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// escalatedTabHref is where the Overview's primary action goes: the
// Escalated tab of Needs attention. The needs-attention page has not
// shipped, so the tab is the console view that already holds the same
// rows -- pointing at the placeholder instead would send an operator who
// believes something is stuck to a page that says nothing is there yet.
const escalatedTabHref = opsEscalatedPath

// inFlightStatuses are the two statuses that mean work is happening now.
// They are passed to the delivery read so a product's whole roadmap is
// not walked to find them, and re-checked against milestoneInFlight so the
// classification below stays the one place the rule is written.
var inFlightStatuses = []store.MilestoneStatus{
	store.MilestoneStatusInDesign,
	store.MilestoneStatusInProgress,
}

// milestoneInFlight reports whether a container's status counts as in
// flight.
//
// It is two of the eight statuses and no more. "designed" and "planned"
// are up next and "partially complete" is stalled, so none of those --
// nor not started, shipped, or abandoned -- answers "which milestone is
// in flight". Reading partially complete as in flight is the specific
// mistake this rule exists to prevent: it is the status that most looks
// like progress and is in fact work that has stopped.
func milestoneInFlight(status store.MilestoneStatus) bool {
	switch status {
	case store.MilestoneStatusInDesign, store.MilestoneStatusInProgress:
		return true
	default:
		return false
	}
}

// handleProductOverview serves the Overview for the product its URL names.
func (app *App) handleProductOverview(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)
	app.renderOverview(w, r, product)
}

// renderOverview writes the Overview for an already-resolved product.
//
// The nav key is the product's overview URL rather than the path served:
// the shell home reaches this page at "/", which is the Overview item's
// other URL, and marking the item active is what the operator needs on
// both.
//
// The escalated count is read here and put on the request, so the chrome
// the render seam builds reuses this figure rather than reading it again:
// the FR requires the sidebar's badge and this page's action to be the
// same number, and two reads is how two numbers happen.
func (app *App) renderOverview(w http.ResponseWriter, r *http.Request, product store.Product) {
	badge := app.needsAttentionBadge(r.Context(), product.ID)
	r = withEscalationBadge(r, badge)
	body := app.buildOverview(r, product, badge)
	app.renderShell(w, r, product.Name, productHref(product.ID, overviewSuffix), pages.Overview(body))
}

// buildOverview assembles the Overview's view model from the reads the
// page makes beyond the escalated count it is handed: the product's
// containers in flight, and its most recently escalated tasks.
//
// Every region below is independent. Each read writes its own field and
// none returns early, so one region's failure can neither suppress another
// region's answer nor let it render its empty state: a failed read is
// never a confident claim about a different read.
func (app *App) buildOverview(r *http.Request, product store.Product, badge navBadge) pages.OverviewPage {
	page := pages.OverviewPage{
		Product:           productHeaderOf(product),
		Escalated:         badge.count,
		EscalatedReadable: badge.readable,
		EscalatedHref:     escalatedTabHref,
		StatTiles:         app.overviewStatTiles(r, product.ID, badge),
	}
	if !badge.readable {
		// The header's action slot is a region of its own, and an
		// unreadable count leaves it with nothing to say. Silence there
		// is the same false claim as "nothing is escalated", so it says
		// what it could not read instead.
		page.EscalatedError = "How many tasks are escalated could not be read. See the logs."
	}

	listing, err := app.spec.Delivery(r.Context(), product.ID, inFlightStatuses)
	if err != nil {
		logger.Error("overview in-flight read failed", "product", product.ID.String(), "error", err)
		// The header says what could not be read rather than claiming no
		// milestone is in flight, but it does not stop the other regions:
		// they read different stores and share no state, so one failing
		// must not cost the operator the others' answers.
		page.InFlightError = "Which milestones are in flight could not be read. See the logs."
	} else {
		page.InFlight = inFlightOf(listing)
	}

	page.InFlightPanel = app.inFlightPanel(r, product.ID)

	// The attention panel is a second region rather than part of the
	// header: its read failing must not cost the operator the in-flight
	// answer the header just rendered, and its success must not be
	// reported alongside an in-flight failure.
	escalated, err := app.needsAttentionRows(r.Context(), product.ID, time.Now())
	if err != nil {
		logger.Error("overview needs-attention read failed", "product", product.ID.String(), "error", err)
		page.NeedsAttentionError = "Which tasks are escalated could not be read. See the logs."
	} else {
		page.NeedsAttention = escalated
	}
	return page
}

// inFlightPanel reads the per-container task progress and keeps only the
// containers milestoneInFlight accepts, so the panel's rows are the
// header's badges with a progress figure beside each.
//
// The two lists come from different reads because they answer different
// questions -- which containers are in flight, and how far along each
// one is -- but the classification is the same predicate, so a container
// cannot be badged in the header and absent from the panel.
func (app *App) inFlightPanel(r *http.Request, productID uuid.UUID) pages.OverviewInFlightPanel {
	scopeID, err := app.soleScopeID(r.Context())
	if err != nil {
		logger.Warn("overview in-flight progress: could not resolve scope", "error", err)
		return unreadableInFlightPanel
	}
	progress, err := app.tasks.SummarizeProductTaskProgress(r.Context(), store.ProductTaskProgressParams{
		ScopeID:   scopeID,
		ProductID: productID,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
	})
	if err != nil {
		logger.Error("overview in-flight progress read failed", "product", productID.String(), "error", err)
		return unreadableInFlightPanel
	}
	return pages.OverviewInFlightPanel{Rows: inFlightRows(productID, progress.Containers)}
}

// unreadableInFlightPanel is the panel's failed-read state. It is a
// sentence in place of the rows rather than an empty list, because a
// panel with nothing in it is a real answer -- this product has no
// milestone in flight -- and rendering a read failure as one would
// answer the page's central question wrongly and confidently.
var unreadableInFlightPanel = pages.OverviewInFlightPanel{
	Error: "Task progress for the milestones in flight could not be read. See the logs.",
}

// inFlightRows keeps the in-flight containers out of one progress read,
// each carrying the two figures its bar is built from.
//
// A row's own status is the container's, not its parent's: a milepebble
// in progress under a designed milestone is in flight, exactly as the
// header already lists it beside its parent. The figures are the read's
// own Done() and Total() rather than anything summed here -- the read
// documents how a cancelled task counts, and re-deriving the numbers is
// how a progress bar comes to disagree with the lane breakdown beside it.
func inFlightRows(productID uuid.UUID, containers []store.ContainerTaskProgress) []pages.OverviewInFlightRow {
	var rows []pages.OverviewInFlightRow
	for _, c := range containers {
		id, name, status := c.Milestone.ID, c.Milestone.Name, c.Milestone.Status
		if c.Milepebble != nil {
			id, name, status = c.Milepebble.ID, c.Milepebble.Name, c.Milepebble.Status
		}
		if !milestoneInFlight(status) {
			continue
		}
		rows = append(rows, pages.OverviewInFlightRow{
			Name:   name,
			Href:   milestoneDetailHref(productID, id),
			Status: string(status),
			Done:   c.Done(),
			Total:  c.Total(),
		})
	}
	return rows
}

// milestoneDetailHref is where an in-flight row's name links: the
// container's own page under the product's milestones prefix. A
// milepebble is a milestone_ref row too, so the same path serves it.
func milestoneDetailHref(productID, containerID uuid.UUID) string {
	return productHref(productID, milestonesSuffix+"/"+containerID.String())
}

// needsAttentionRows reads the panel's rows: this product's most recently
// escalated tasks, at most the panel's own limit.
//
// The narrowing is the ConsoleFilter the sidebar's badge counts through,
// deliberately the same one: the FR requires the badge, the Escalated
// tile and this panel to describe one set of tasks, and three reads
// spelled three ways is how three numbers happen. MilestoneID stays unset
// so the panel covers the product across all its milestones -- an
// escalation in one container is exactly what an operator must not miss
// while looking at another.
//
// The page size requested is the panel's limit rather than the store
// default, so the store returns the five newest rows in order instead of
// this trimming an arbitrary page down to five. Ordering is the query's
// own (escalated_at DESC, task.id DESC), so nothing here re-sorts.
func (app *App) needsAttentionRows(ctx context.Context, productID uuid.UUID, now time.Time) ([]pages.OverviewEscalation, error) {
	scopeID, err := app.soleScopeID(ctx)
	if err != nil {
		return nil, err
	}
	result, err := app.tasks.ListEscalatedTasks(ctx, store.ListEscalatedTasksParams{
		ScopeID:       scopeID,
		ConsoleFilter: store.ConsoleFilter{ProductID: &productID},
		Page:          store.PageParams{PageSize: pages.NeedsAttentionMax},
	})
	if err != nil {
		return nil, err
	}
	rows := make([]pages.OverviewEscalation, 0, len(result.Items))
	for _, row := range result.Items {
		rows = append(rows, pages.OverviewEscalation{
			TaskID:           row.TaskID.String(),
			Title:            row.Title,
			Href:             taskDetailPath(productID, row.DeliveryRef.ID, row.TaskID),
			Reason:           string(row.Reason),
			EscalatedAt:      relativeTime(row.EscalatedAt, now),
			EscalatedAtExact: row.EscalatedAt.Format(time.RFC3339),
		})
	}
	return rows, nil
}

// relativeTime renders how long ago t was, in the coarse units an
// operator scans a panel for.
//
// now is passed rather than read from the clock so a rendering is a
// function of its inputs: a panel whose timestamps move with wall time is
// a panel whose test can only assert "some number of minutes".
func relativeTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		m := int(d.Minutes())
		if m == 1 {
			return "1 min ago"
		}
		return fmt.Sprintf("%d min ago", m)
	case d < 24*time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 h ago"
		}
		return fmt.Sprintf("%d h ago", h)
	default:
		days := int(d.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	}
}

// inFlightOf flattens a delivery listing down to its in-flight containers.
//
// Milepebbles are listed alongside their milestones, not folded into them:
// a cut milestone whose status is "designed" while its milepebbles are in
// progress has work in flight, and hiding that behind the parent's status
// would report a quiet product that is in fact being built.
func inFlightOf(listing slice.DeliveryListing) []pages.OverviewMilestone {
	var inFlight []pages.OverviewMilestone
	for _, m := range listing.Milestones {
		if milestoneInFlight(m.Status) {
			inFlight = append(inFlight, pages.OverviewMilestone{Name: m.Name, Status: string(m.Status)})
		}
		for _, mp := range m.Milepebbles {
			if milestoneInFlight(mp.Status) {
				inFlight = append(inFlight, pages.OverviewMilestone{Name: mp.Name, Status: string(mp.Status)})
			}
		}
	}
	return inFlight
}
