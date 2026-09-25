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
// of the seven-value set a milestone is in.
package main

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// deliveryEntity is one spec entity (a Feature or a Requirement) a container
// delivers, flattened out of a breakdown Document into a single display row.
type deliveryEntity struct {
	Label string // "Cn -- Name" for a Feature, "FR -- Name" for a Requirement
	ID    string
}

// deliveryBreakdown is one container's per-item shipped vs not-yet-shipped
// scope, the get_delivery_breakdown shape. Present only for a
// partially-complete container; nil for every other status.
type deliveryBreakdown struct {
	Shipped   []deliveryEntity
	Unshipped []deliveryEntity
}

// deliveryMilepebble is one milepebble row: its own status and, when
// partially complete, its shipped/unshipped breakdown.
type deliveryMilepebble struct {
	ID          string
	Name        string
	Outcome     string
	Status      string // one of the seven-value store.MilestoneStatus set
	StatusClass string // CSS modifier keeping the seven states visually distinct
	Breakdown   *deliveryBreakdown
}

// deliveryMilestone is one milestone row with its nested milepebbles.
type deliveryMilestone struct {
	ID          string
	Name        string
	Outcome     string
	FRBudget    string // the stored budget, empty when unset
	Status      string
	StatusClass string // CSS modifier keeping the seven states visually distinct
	Breakdown   *deliveryBreakdown
	Milepebbles []deliveryMilepebble
}

type deliveryPage struct {
	Product    productHeader
	Nav        productNav
	Milestones []deliveryMilestone
}

// deliveryBreakdownTemplate is the shared shipped/unshipped block, included
// for both a milestone and its milepebbles so the two render identically.
const deliveryBreakdownTemplate = `{{define "deliverybreakdown"}}
<div class="breakdown">
<strong>Shipped</strong>
{{if .Shipped}}<ul>{{range .Shipped}}<li>{{.Label}} <code class="id">{{.ID}}</code></li>{{end}}</ul>{{else}}<p>Nothing shipped yet.</p>{{end}}
<strong>Unshipped</strong>
{{if .Unshipped}}<ul>{{range .Unshipped}}<li>{{.Label}} <code class="id">{{.ID}}</code></li>{{end}}</ul>{{else}}<p>Nothing unshipped.</p>{{end}}
</div>
{{end}}`

var deliveryTemplate = template.Must(template.New("delivery").Parse(productNavTemplate + deliveryBreakdownTemplate + `<h2>{{.Product.Name}} &mdash; delivery &amp; roadmap</h2>
{{if .Product.Vision}}<p>{{.Product.Vision}}</p>{{end}}
{{template "productnav" .}}
{{if .Milestones}}
<ol>
{{range .Milestones}}
  <li id="{{.ID}}">
    <strong>{{.Name}}</strong> <code class="id">{{.ID}}</code>
    <span class="status {{.StatusClass}}">{{.Status}}</span>
    {{if .FRBudget}}<span class="frbudget">FR budget {{.FRBudget}}</span>{{end}}
    {{if .Outcome}}<div>{{.Outcome}}</div>{{end}}
    {{if .Breakdown}}{{template "deliverybreakdown" .Breakdown}}{{end}}
    {{if .Milepebbles}}
    <ul>
    {{range .Milepebbles}}
      <li id="{{.ID}}">
        <strong>{{.Name}}</strong> <code class="id">{{.ID}}</code>
        <span class="status {{.StatusClass}}">{{.Status}}</span>
        {{if .Outcome}}<div>{{.Outcome}}</div>{{end}}
        {{if .Breakdown}}{{template "deliverybreakdown" .Breakdown}}{{end}}
      </li>
    {{end}}
    </ul>
    {{end}}
  </li>
{{end}}
</ol>
{{else}}
<p>No milestones yet.</p>
{{end}}`))

// handleSpecDelivery renders a product's delivery/roadmap: every milestone
// and milepebble with its current status, plus the shipped/unshipped
// breakdown for each partially-complete container.
func (app *App) handleSpecDelivery(w http.ResponseWriter, r *http.Request) {
	productID, ok := specProductID(w, r)
	if !ok {
		return
	}

	product, err := app.spec.Product(r.Context(), productID)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}
	// A nil status filter means "all", mirroring the querier's contract.
	listing, err := app.spec.Delivery(r.Context(), productID, nil)
	if err != nil {
		renderSpecError(w, r, err)
		return
	}
	// A per-container breakdown read that fails is non-fatal: every
	// container's status still renders, just without that one's breakdown.
	breakdowns := app.deliveryBreakdowns(r.Context(), listing)

	renderShell(w, r, "Delivery", specPath, renderPage(deliveryTemplate, deliveryPageOf(product, listing, breakdowns, productID)))
}

// deliveryBreakdowns resolves the per-item shipped/unshipped breakdown for
// every partially-complete milestone and milepebble in the listing, via the
// same Querier.GetDeliveryBreakdown the MCP tool get_delivery_breakdown
// wraps. A container in any other status gets no entry -- its shipped/unshipped
// breakdown is not applicable, not "zero shipped, zero unshipped".
//
// A breakdown read that fails for one container is non-fatal: it is logged
// at ERROR (a genuine failed read, not expected control flow) and that
// container is left without a breakdown, so one bad container never takes
// down the page's statuses.
func (app *App) deliveryBreakdowns(ctx context.Context, listing slice.DeliveryListing) map[uuid.UUID]deliveryBreakdown {
	var ids []uuid.UUID
	for _, m := range listing.Milestones {
		if m.Status == store.MilestoneStatusPartiallyComplete {
			ids = append(ids, m.ID)
		}
		for _, mp := range m.Milepebbles {
			if mp.Status == store.MilestoneStatusPartiallyComplete {
				ids = append(ids, mp.ID)
			}
		}
	}

	breakdowns := make(map[uuid.UUID]deliveryBreakdown, len(ids))
	for _, id := range ids {
		shipped, unshipped, err := app.spec.DeliveryBreakdown(ctx, id)
		if err != nil {
			logger.Error("delivery breakdown read failed", "container", id.String(), "error", err)
			continue
		}
		breakdowns[id] = deliveryBreakdown{
			Shipped:   deliveryEntitiesOf(shipped),
			Unshipped: deliveryEntitiesOf(unshipped),
		}
	}
	return breakdowns
}

// deliveryPageOf assembles the roadmap from a delivery listing, attaching
// each partially-complete container's shipped/unshipped breakdown from
// breakdowns. Pure, so the field-parity with the MCP wire is unit-testable
// without a database.
func deliveryPageOf(product store.Product, listing slice.DeliveryListing, breakdowns map[uuid.UUID]deliveryBreakdown, productID uuid.UUID) deliveryPage {
	page := deliveryPage{
		Product: productHeaderOf(product),
		Nav:     productNavFor(productID, deliveryPath(productID)),
	}
	for _, m := range listing.Milestones {
		entry := deliveryMilestone{
			ID:          m.ID.String(),
			Name:        m.Name,
			Outcome:     deref(m.Outcome),
			FRBudget:    frBudgetString(m.FRBudget),
			Status:      string(m.Status),
			StatusClass: statusClass(m.Status),
			Breakdown:   breakdownFor(breakdowns, m.ID),
		}
		for _, mp := range m.Milepebbles {
			entry.Milepebbles = append(entry.Milepebbles, deliveryMilepebble{
				ID:          mp.ID.String(),
				Name:        mp.Name,
				Outcome:     deref(mp.Outcome),
				Status:      string(mp.Status),
				StatusClass: statusClass(mp.Status),
				Breakdown:   breakdownFor(breakdowns, mp.ID),
			})
		}
		page.Milestones = append(page.Milestones, entry)
	}
	return page
}

// breakdownFor returns the breakdown for a container id, or nil when that
// container is not partially complete (and so has no breakdown to show).
func breakdownFor(breakdowns map[uuid.UUID]deliveryBreakdown, id uuid.UUID) *deliveryBreakdown {
	b, ok := breakdowns[id]
	if !ok {
		return nil
	}
	return &b
}

// deliveryEntitiesOf flattens a breakdown Document (a Feature or Requirement
// set) into display rows, in Features-then-Requirements order. The Documents
// a breakdown returns carry today's two delivers-able kinds.
func deliveryEntitiesOf(doc slice.Document) []deliveryEntity {
	entities := make([]deliveryEntity, 0, len(doc.Features)+len(doc.Requirements))
	for _, f := range doc.Features {
		entities = append(entities, deliveryEntity{
			Label: fmt.Sprintf("C%d -- %s", f.DisplayNumber, f.Name),
			ID:    f.ID.String(),
		})
	}
	for _, r := range doc.Requirements {
		entities = append(entities, deliveryEntity{
			Label: fmt.Sprintf("%s -- %s", r.Kind, r.Name),
			ID:    r.ID.String(),
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

// statusClass maps the seven-value MilestoneStatus set to the CSS modifier
// a status badge carries, so each state is visually distinct -- notably
// "shipped" (done) versus "partially complete" (has a breakdown) -- without
// the template doing any string munging. An unrecognized value falls back
// to a neutral class rather than rendering an empty badge.
func statusClass(s store.MilestoneStatus) string {
	switch s {
	case store.MilestoneStatusNotStarted:
		return "status-not-started"
	case store.MilestoneStatusInDesign:
		return "status-in-design"
	case store.MilestoneStatusPlanned:
		return "status-planned"
	case store.MilestoneStatusInProgress:
		return "status-in-progress"
	case store.MilestoneStatusShipped:
		return "status-shipped"
	case store.MilestoneStatusPartiallyComplete:
		return "status-partial"
	case store.MilestoneStatusAbandoned:
		return "status-abandoned"
	default:
		return "status-other"
	}
}
