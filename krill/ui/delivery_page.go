// Delivery/roadmap view: a product's milestones and milepebbles with derived
// status, read through the same Querier as the MCP delivery tools so both agree.
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

// handleSpecDelivery renders a product's delivery roadmap.
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
	// A nil status filter means "all".
	listing, err := app.spec.Delivery(r.Context(), productID, nil)
	if err != nil {
		app.renderSpecError(w, r, pages.DeliveryAnchor, err)
		return
	}
	// A failed breakdown read only replaces that container's breakdown with an
	// inline error.
	breakdowns := app.deliveryBreakdowns(r.Context(), listing)

	app.renderSpecPage(w, r, "Delivery", pages.Delivery(deliveryPageOf(product, listing, breakdowns, productID)))
}

// deliveryBreakdowns reads the shipped/unshipped breakdown for each
// partially-complete container; other statuses get none. A failed read is
// logged and becomes an inline error for that container only.
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

// containerRef is a container to read a breakdown for, with the status that
// decides deliveryBreakdownOf's disagreement flag.
type containerRef struct {
	id     uuid.UUID
	status store.MilestoneStatus
}

// deliveryBreakdownOf builds a container's breakdown block. StatusDisagrees
// flags a "partially complete" container with nothing unshipped.
func deliveryBreakdownOf(status store.MilestoneStatus, shipped, unshipped slice.Document) pages.DeliveryBreakdown {
	unshippedEntities := deliveryEntitiesOf(unshipped)
	return pages.DeliveryBreakdown{
		Shipped:         deliveryEntitiesOf(shipped),
		Unshipped:       unshippedEntities,
		StatusDisagrees: status == store.MilestoneStatusPartiallyComplete && len(unshippedEntities) == 0,
	}
}

// deliveryPageOf assembles the roadmap view model. Pure, so it is unit-testable
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

// breakdownFor returns a container's breakdown, or nil when it has none.
func breakdownFor(breakdowns map[uuid.UUID]pages.DeliveryBreakdown, id uuid.UUID) *pages.DeliveryBreakdown {
	b, ok := breakdowns[id]
	if !ok {
		return nil
	}
	return &b
}

// deliveryEntitiesOf flattens a breakdown Document into rows, features first.
// Kind is the entity type; Label carries the display number operators cite.
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

func frBudgetString(budget *int) string {
	if budget == nil {
		return ""
	}
	return strconv.Itoa(*budget)
}
