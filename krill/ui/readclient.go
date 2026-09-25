// specReader is how this binary's own app pages read the spec axis: the
// same //krill/slice.Querier and //krill/store readers the MCP spec tools
// call underneath (get_product_slice -> Querier.GetProductSlice,
// list_personas -> PersonaStore.ListCurrentByProduct, list_non_goals ->
// NonGoalStore.ListCurrentByProduct). Reads go through the same code the
// MCP surface wraps rather than a parallel query, so a page and the
// matching tool can never disagree about what "the current spec" is.
//
// Like every krill read path, these are ungated: no krill session, no
// operator Subject -- the spec axis is readable by anyone who can reach
// the deployment (PRODUCT.md's write-only gate). Every method below reads
// *current* rows (GetCurrentByID / ListCurrentByProduct), never history,
// which is what "current revisions only" means for the views: a superseded
// revision is never surfaced.
package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// specReader is the spec-axis read side of the UI, backed directly by
// store.Store (krill/ui already holds a pool for its session store and
// auth tables). The write side, by contrast, is the HTTP client in
// writeclient.go -- reads need no session to attribute, so they do not
// need api's write gate.
type specReader struct {
	store   *store.Store
	querier *slice.Querier
}

// newSpecReader wires reader to this deployment's store. The querier is
// //krill/slice's, so the Document the capability map and decisions pages
// render is assembled by the exact same code get_product_slice returns.
func newSpecReader(s *store.Store) *specReader {
	return &specReader{store: s, querier: slice.NewQuerier(s)}
}

// ProductSlice is get_product_slice: the whole Product's FeatureSets,
// Features, FRs/NFRs, and LoadBearingDecisions, current revisions only.
func (r *specReader) ProductSlice(ctx context.Context, productID uuid.UUID) (slice.Document, error) {
	doc, err := r.querier.GetProductSlice(ctx, productID)
	if err != nil {
		return slice.Document{}, fmt.Errorf("product slice: %w", err)
	}
	return doc, nil
}

// Personas is list_personas: every current Persona under the Product.
func (r *specReader) Personas(ctx context.Context, productID uuid.UUID) ([]store.Persona, error) {
	personas, err := r.store.Personas().ListCurrentByProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("list personas: %w", err)
	}
	return personas, nil
}

// NonGoals is list_non_goals: every current Non-Goal (both the permanent
// and deferred kinds) under the Product.
func (r *specReader) NonGoals(ctx context.Context, productID uuid.UUID) ([]store.NonGoal, error) {
	nonGoals, err := r.store.NonGoals().ListCurrentByProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("list non-goals: %w", err)
	}
	return nonGoals, nil
}

// Delivery is list_product_delivery: every milestone and milepebble under
// the Product, each with its derived current status and -- for a
// partially-complete container -- its shipped/unshipped counts, resolved
// through the exact //krill/slice.Querier.ListProductDelivery the MCP tool
// wraps. The product's own scope_id is resolved from its current row first,
// exactly as the tool's handler does, because an ungated read carries no
// krill session to read scope_id from. An empty statuses slice means "all",
// mirroring the querier's own contract.
func (r *specReader) Delivery(ctx context.Context, productID uuid.UUID, statuses []store.MilestoneStatus) (slice.DeliveryListing, error) {
	product, err := r.store.Products().GetCurrentByID(ctx, productID)
	if err != nil {
		return slice.DeliveryListing{}, fmt.Errorf("get product: %w", err)
	}
	listing, err := r.querier.ListProductDelivery(ctx, product.ScopeID, productID, statuses)
	if err != nil {
		return slice.DeliveryListing{}, fmt.Errorf("list product delivery: %w", err)
	}
	return listing, nil
}

// DeliveryBreakdown is get_delivery_breakdown: one container's per-item
// shipped vs not-yet-shipped scope, as the same two slice.Documents
// //krill/slice.Querier.GetDeliveryBreakdown returns to the MCP tool. Works
// for a milepebble exactly as for a milestone -- both are milestone_ref
// rows, which the querier resolves through DeliveryShipments().
// DeliveryBreakdown.
func (r *specReader) DeliveryBreakdown(ctx context.Context, containerID uuid.UUID) (shipped, unshipped slice.Document, err error) {
	shipped, unshipped, err = r.querier.GetDeliveryBreakdown(ctx, containerID)
	if err != nil {
		return slice.Document{}, slice.Document{}, fmt.Errorf("delivery breakdown: %w", err)
	}
	return shipped, unshipped, nil
}

// Product returns the Product's own current row -- the header every
// product-scoped page shows (name, vision), so an operator can tell which
// product they are browsing.
func (r *specReader) Product(ctx context.Context, productID uuid.UUID) (store.Product, error) {
	product, err := r.store.Products().GetCurrentByID(ctx, productID)
	if err != nil {
		return store.Product{}, fmt.Errorf("get product: %w", err)
	}
	return product, nil
}

// Products lists every current Product in the deployment's sole scope
// (mirrors the mcp list_products discovery shape, which resolves the sole
// scope itself -- a browser cannot pick a scope). This is the index an
// operator browses a specific product from.
func (r *specReader) Products(ctx context.Context) ([]store.Product, error) {
	scope, err := r.store.Scopes().GetSole(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve scope: %w", err)
	}
	products, err := r.store.Products().ListCurrentByScope(ctx, scope.ID)
	if err != nil {
		return nil, fmt.Errorf("list products: %w", err)
	}
	return products, nil
}
