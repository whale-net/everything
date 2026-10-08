// The Overview: the shell's home page, answering whether anything is stuck and
// which milestone is in flight. Served at /products/{pid}/overview and at "/".
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

// escalatedTabHref points at the console's Escalated view, which holds the same
// rows as Needs attention's Escalated tab.
const escalatedTabHref = opsEscalatedPath

// inFlightStatuses narrows the delivery read; milestoneInFlight stays the one
// place the rule is written.
var inFlightStatuses = []store.MilestoneStatus{
	store.MilestoneStatusInDesign,
	store.MilestoneStatusInProgress,
}

// milestoneInFlight reports whether a status counts as in flight. "Partially
// complete" is deliberately excluded: it looks like progress but is stalled work.
func milestoneInFlight(status store.MilestoneStatus) bool {
	switch status {
	case store.MilestoneStatusInDesign, store.MilestoneStatusInProgress:
		return true
	default:
		return false
	}
}

func (app *App) handleProductOverview(w http.ResponseWriter, r *http.Request) {
	r, product, ok := app.resolveProductFromPath(w, r)
	if !ok {
		return
	}
	setLastViewedProductCookie(w, product.ID)
	app.renderOverview(w, r, product)
}

// renderOverview writes the Overview for a resolved product. The escalated count is
// read once and put on the request so the sidebar badge and page action match.
func (app *App) renderOverview(w http.ResponseWriter, r *http.Request, product store.Product) {
	badge := app.needsAttentionBadge(r.Context(), product.ID)
	r = withEscalationBadge(r, badge)
	body := app.buildOverview(r, product, badge)
	app.renderShell(w, r, product.Name, productHref(product.ID, overviewSuffix), pages.Overview(body))
}

// buildOverview assembles the view model. Each region reads independently and never
// returns early, so one failed read cannot suppress or falsify another region.
func (app *App) buildOverview(r *http.Request, product store.Product, badge navBadge) pages.OverviewPage {
	page := pages.OverviewPage{
		Product:           productHeaderOf(product),
		Escalated:         badge.count,
		EscalatedReadable: badge.readable,
		EscalatedHref:     escalatedTabHref,
		StatTiles:         app.overviewStatTiles(r, product.ID, badge),
	}
	if !badge.readable {
		// An unreadable count must say so; silence would claim nothing is escalated.
		page.EscalatedError = "How many tasks are escalated could not be read. See the logs."
	}

	listing, err := app.spec.Delivery(r.Context(), product.ID, inFlightStatuses)
	if err != nil {
		logger.Error("overview in-flight read failed", "product", product.ID.String(), "error", err)
		page.InFlightError = "Which milestones are in flight could not be read. See the logs."
	} else {
		page.InFlight = inFlightOf(listing)
	}

	page.InFlightPanel = app.inFlightPanel(r, product.ID)

	escalated, err := app.needsAttentionRows(r.Context(), product.ID, time.Now())
	if err != nil {
		logger.Error("overview needs-attention read failed", "product", product.ID.String(), "error", err)
		page.NeedsAttentionError = "Which tasks are escalated could not be read. See the logs."
	} else {
		page.NeedsAttention = escalated
	}
	return page
}

// inFlightPanel reads per-container progress filtered by milestoneInFlight, so its
// rows match the header's badges.
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

// unreadableInFlightPanel is a sentence, not an empty list, since an empty panel
// would wrongly claim no milestone is in flight.
var unreadableInFlightPanel = pages.OverviewInFlightPanel{
	Error: "Task progress for the milestones in flight could not be read. See the logs.",
}

// inFlightRows keeps in-flight containers, judged by their own status rather than
// the parent's. Figures come from the read's Done()/Total() so the bar matches the
// lane breakdown.
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

// milestoneDetailHref is a container's detail page; milepebbles are served there too.
func milestoneDetailHref(productID, containerID uuid.UUID) string {
	return productHref(productID, milestonesSuffix+"/"+containerID.String())
}

// needsAttentionRows reads the product's newest escalated tasks through the same
// ConsoleFilter the sidebar badge counts, across all milestones. Order is the
// query's own (escalated_at DESC, task.id DESC).
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

// relativeTime renders how long ago t was; now is a parameter so output is
// deterministic.
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

// inFlightOf flattens a delivery listing to its in-flight containers. Milepebbles
// are listed separately so in-progress work under a "designed" milestone shows.
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
