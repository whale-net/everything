// The Overview: the shell's home page, answering the two questions an
// operator opens it to ask -- is anything stuck, and which milestone is in
// flight (FR c3e1c276).
//
// It is served at two URLs, /products/{pid}/overview and the un-prefixed
// "/", which resolves a product and then serves the same page. The frame
// lives here; the stat tiles are built in overview_tiles.go, and the
// Needs-attention panel and the in-flight panel are separate pages' own
// work and their slots render empty until they land.
package main

import (
	"net/http"

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
// containers in flight.
func (app *App) buildOverview(r *http.Request, product store.Product, badge navBadge) pages.OverviewPage {
	page := pages.OverviewPage{
		Product:           productHeaderOf(product),
		Escalated:         badge.count,
		EscalatedReadable: badge.readable,
		EscalatedHref:     escalatedTabHref,
		StatTiles:         app.overviewStatTiles(r, product.ID, badge),
	}

	listing, err := app.spec.Delivery(r.Context(), product.ID, inFlightStatuses)
	if err != nil {
		logger.Error("overview in-flight read failed", "product", product.ID.String(), "error", err)
		// The header is the one thing this page cannot do without, so it
		// says what could not be read rather than claiming no milestone
		// is in flight.
		page.InFlightError = "Which milestones are in flight could not be read. See the logs."
		return page
	}
	page.InFlight = inFlightOf(listing)
	return page
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
