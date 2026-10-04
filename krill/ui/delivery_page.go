// The delivery/roadmap view: a product's every milestone and milepebble,
// each with its derived current status, and -- for a partially-complete
// container -- the per-item shipped vs unshipped breakdown. Reads through
// app.spec (readclient.go) via the same //krill/slice.Querier the MCP tools'
// list_product_delivery and get_delivery_breakdown call underneath
// (Querier.ListProductDelivery / Querier.GetDeliveryBreakdown), so a page
// and the matching tools always show the same current delivery state.
//
// Scoped to one product at /spec/products/{id}/delivery, alongside the
// capability map, decisions, personas, and non-goals. Every container's
// status is the batched MilestoneStatusEventStore.CurrentStatuses derivation
// the querier performs, so a page never disagrees with the tools about which
// of the eight-value set a milestone is in.
package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// handleSpecDelivery renders a product's delivery/roadmap: every milestone
// and milepebble with its current status, plus the shipped/unshipped
// breakdown for each partially-complete container.
func (app *App) handleSpecDelivery(w http.ResponseWriter, r *http.Request) {
	productID, ok := app.specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		app.renderSpecError(w, r, pages.DeliveryAnchor, err)
		return
	}
	// A nil status filter means "all", mirroring the querier's contract.
	listing, err := app.spec.Delivery(r.Context(), productID, nil)
	if err != nil {
		app.renderSpecError(w, r, pages.DeliveryAnchor, err)
		return
	}
	// A per-container breakdown read that fails is non-fatal: every
	// container's status still renders, just with that one's breakdown
	// replaced by an inline error.
	breakdowns := app.deliveryBreakdowns(r.Context(), listing)

	app.renderSpecPage(w, r, "Delivery", pages.Delivery(deliveryPageOf(product, listing, breakdowns, productID)))
}

// deliveryBreakdowns resolves the per-item shipped/unshipped breakdown for
// every partially-complete milestone and milepebble in the listing, via the
// same Querier.GetDeliveryBreakdown the MCP tool get_delivery_breakdown
// wraps. A container in any other status gets no entry -- its shipped/unshipped
// breakdown is not applicable, not "zero shipped, zero unshipped". The
// partially-complete-only gate is also what makes StatusDisagrees meaningful:
// a container that is fully shipped and lists nothing unshipped is the normal
// end state, not a disagreement, so it renders no breakdown at all.
//
// A breakdown read that fails for one container is non-fatal: it is logged
// at ERROR (a genuine failed read, not expected control flow) and that
// container is given a breakdown carrying the error instead of its lists, so
// the page renders an inline error in place of that one block rather than
// taking down every container's status with it.
func (app *App) deliveryBreakdowns(ctx context.Context, listing slice.DeliveryListing) map[uuid.UUID]pages.DeliveryBreakdown {
	var wanted []containerRef
	for _, m := range listing.Milestones {
		if m.Status == store.MilestoneStatusPartiallyComplete {
			wanted = append(wanted, containerRef{id: m.ID, status: m.Status})
		}
		for _, mp := range m.Milepebbles {
			if mp.Status == store.MilestoneStatusPartiallyComplete {
				wanted = append(wanted, containerRef{id: mp.ID, status: mp.Status})
			}
		}
	}

	breakdowns := make(map[uuid.UUID]pages.DeliveryBreakdown, len(wanted))
	for _, ref := range wanted {
		shipped, unshipped, err := app.spec.DeliveryBreakdown(ctx, ref.id)
		if err != nil {
			logger.Error("delivery breakdown read failed", "container", ref.id.String(), "error", err)
			breakdowns[ref.id] = pages.DeliveryBreakdown{Error: "This container's shipped/unshipped breakdown could not be read. See the logs."}
			continue
		}
		breakdowns[ref.id] = deliveryBreakdownOf(ref.status, shipped, unshipped)
	}
	return breakdowns
}

// containerRef is one container the roadmap wants a breakdown for, with the
// status that made it worth reading.
//
// The status rides along because deliveryBreakdownOf needs it: an empty
// unshipped list means one thing under a "partially complete" badge and
// another under a "shipped" one, and the flag that says so has to be decided
// from the same status the listing reported rather than re-guessed here.
type containerRef struct {
	id     uuid.UUID
	status store.MilestoneStatus
}

// deliveryBreakdownOf turns one container's two breakdown Documents into the
// block both surfaces render, deciding the disagreement flag from the
// container's OWN status.
//
// The flag is one rule in one place because it is the one inference in the
// whole breakdown: a container that reads "partially complete" while its
// unshipped list is empty. The roadmap's per-container pass only ever offers
// it partially-complete ids, so the status test is a no-op there; the
// milestone detail offers every container, and a shipped milestone with
// nothing outstanding is the normal end state rather than a disagreement.
func deliveryBreakdownOf(status store.MilestoneStatus, shipped, unshipped slice.Document) pages.DeliveryBreakdown {
	unshippedEntities := deliveryEntitiesOf(unshipped)
	return pages.DeliveryBreakdown{
		Shipped:         deliveryEntitiesOf(shipped),
		Unshipped:       unshippedEntities,
		StatusDisagrees: status == store.MilestoneStatusPartiallyComplete && len(unshippedEntities) == 0,
	}
}

// deliveryPageOf assembles the roadmap from a delivery listing, attaching
// each partially-complete container's shipped/unshipped breakdown from
// breakdowns. Pure, so the field-parity with the MCP wire is unit-testable
// without a database.
func deliveryPageOf(product store.Product, listing slice.DeliveryListing, breakdowns map[uuid.UUID]pages.DeliveryBreakdown, productID uuid.UUID) pages.DeliveryPage {
	page := pages.DeliveryPage{
		Product: productHeaderOf(product),
		Nav:     productNavFor(productID, deliveryPath(productID)),
		Path:    deliveryPath(productID),
	}
	for _, m := range listing.Milestones {
		entry := pages.DeliveryMilestone{
			ID:        m.ID.String(),
			TasksPath: milestoneTasksPath(productID, m.ID),
			BoardPath: milestoneBoardPath(productID, m.ID),
			Name:      m.Name,
			Outcome:   deref(m.Outcome),
			FRBudget:  frBudgetString(m.FRBudget),
			Status:    string(m.Status),
			Breakdown: breakdownFor(breakdowns, m.ID),
		}
		for _, mp := range m.Milepebbles {
			entry.Milepebbles = append(entry.Milepebbles, pages.DeliveryMilepebble{
				ID:        mp.ID.String(),
				TasksPath: milestoneTasksPath(productID, mp.ID),
				BoardPath: milestoneBoardPath(productID, mp.ID),
				Name:      mp.Name,
				Outcome:   deref(mp.Outcome),
				Status:    string(mp.Status),
				Breakdown: breakdownFor(breakdowns, mp.ID),
			})
		}
		page.Milestones = append(page.Milestones, entry)
	}
	return page
}

// breakdownFor returns the breakdown for a container id, or nil when that
// container is not partially complete (and so has no breakdown to show).
func breakdownFor(breakdowns map[uuid.UUID]pages.DeliveryBreakdown, id uuid.UUID) *pages.DeliveryBreakdown {
	b, ok := breakdowns[id]
	if !ok {
		return nil
	}
	return &b
}

// deliveryEntitiesOf flattens a breakdown Document (a Feature or Requirement
// set) into display rows, in Features-then-Requirements order. The Documents
// a breakdown returns carry today's two delivers-able kinds.
//
// Kind is what the entity IS, which is not what Label's prefix says: "C4"
// and "FR" are display numbers, so a table with a Kind column needs the two
// apart. Label keeps carrying the number, because that is what the operator
// cites.
func deliveryEntitiesOf(doc slice.Document) []pages.DeliveryEntity {
	entities := make([]pages.DeliveryEntity, 0, len(doc.Features)+len(doc.Requirements))
	for _, f := range doc.Features {
		entities = append(entities, pages.DeliveryEntity{
			Label: fmt.Sprintf("C%d -- %s", f.DisplayNumber, f.Name),
			ID:    f.ID.String(),
			Kind:  "Feature",
		})
	}
	for _, r := range doc.Requirements {
		entities = append(entities, pages.DeliveryEntity{
			Label: fmt.Sprintf("%s -- %s", r.Kind, r.Name),
			ID:    r.ID.String(),
			Kind:  "Requirement",
		})
	}
	return entities
}

// frBudgetString renders the stored FR budget, empty when unset.
func frBudgetString(budget *int) string {
	if budget == nil {
		return ""
	}
	return strconv.Itoa(*budget)
}
