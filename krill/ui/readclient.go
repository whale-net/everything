// specReader reads the spec axis through the same slice.Querier and store
// readers the MCP spec tools wrap, so a page and its tool cannot disagree.
// Reads carry no krill session and return current revisions only.
package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// specReadClient is the read seam the /spec and delivery pages depend on;
// it exists so view assembly is testable against an in-memory fake.
type specReadClient interface {
	ProductSlice(ctx context.Context, productID uuid.UUID) (slice.Document, error)
	Personas(ctx context.Context, productID uuid.UUID) ([]store.Persona, error)
	NonGoals(ctx context.Context, productID uuid.UUID) ([]store.NonGoal, error)
	Delivery(ctx context.Context, productID uuid.UUID, statuses []store.MilestoneStatus) (slice.DeliveryListing, error)
	DeliveryBreakdown(ctx context.Context, containerID uuid.UUID) (shipped, unshipped slice.Document, err error)
	// StatusHistory is get_milestone_status_history: one container's status
	// transitions, oldest first.
	StatusHistory(ctx context.Context, containerID uuid.UUID) ([]store.MilestoneStatusEvent, error)
	Product(ctx context.Context, productID uuid.UUID) (store.Product, error)
	Products(ctx context.Context) ([]store.Product, error)
}

// specReader is the UI's read side, backed directly by store.Store. Reads
// need no session to attribute, so they bypass api's write gate.
type specReader struct {
	store   *store.Store
	querier *slice.Querier
}

var _ specReadClient = (*specReader)(nil)

// newSpecReader wires the reader to the deployment's store and slice querier.
func newSpecReader(s *store.Store) *specReader {
	return &specReader{store: s, querier: slice.NewQuerier(s)}
}

// ProductSlice is get_product_slice: the Product's whole current spec tree.
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

// NonGoals is list_non_goals: every current Non-Goal (permanent and
// deferred) under the Product.
func (r *specReader) NonGoals(ctx context.Context, productID uuid.UUID) ([]store.NonGoal, error) {
	nonGoals, err := r.store.NonGoals().ListCurrentByProduct(ctx, productID)
	if err != nil {
		return nil, fmt.Errorf("list non-goals: %w", err)
	}
	return nonGoals, nil
}

// Delivery is list_product_delivery. The product's scope_id is resolved from
// its current row, since a read here has no session to supply it. Empty
// statuses means all.
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

// DeliveryBreakdown is get_delivery_breakdown: one milestone or milepebble's
// shipped vs not-yet-shipped scope.
func (r *specReader) DeliveryBreakdown(ctx context.Context, containerID uuid.UUID) (shipped, unshipped slice.Document, err error) {
	shipped, unshipped, err = r.querier.GetDeliveryBreakdown(ctx, containerID)
	if err != nil {
		return slice.Document{}, slice.Document{}, fmt.Errorf("delivery breakdown: %w", err)
	}
	return shipped, unshipped, nil
}

// StatusHistory is get_milestone_status_history: every status transition one
// container (milestone or milepebble) recorded, oldest first. No transitions
// yields an empty slice, not an error.
func (r *specReader) StatusHistory(ctx context.Context, containerID uuid.UUID) ([]store.MilestoneStatusEvent, error) {
	events, err := r.store.MilestoneStatus().ListTransitions(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("list milestone status transitions: %w", err)
	}
	return events, nil
}

// Product returns the Product's current row, for the page header.
func (r *specReader) Product(ctx context.Context, productID uuid.UUID) (store.Product, error) {
	product, err := r.store.Products().GetCurrentByID(ctx, productID)
	if err != nil {
		return store.Product{}, fmt.Errorf("get product: %w", err)
	}
	return product, nil
}

// Products lists every current Product in the deployment's sole scope; a
// browser cannot pick a scope.
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
