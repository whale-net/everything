// This file (issue #2686, FR10, C28) is DeliveryShipmentStore -- the
// per-delivered-item shipment register (migration 013) that makes
// "partially complete" (migration 012's MilestoneStatus) concrete: for a
// milestone or milepebble's `delivers` associations (entity_milestone,
// relation='delivers'), which have shipped and which have not. Kept as a
// sibling accessor ((*Store).DeliveryShipments()), mirroring
// MilestoneStatusEventStore's own relationship to MilestoneAuthoringStore,
// so the existing entity_milestone write paths stay untouched -- this
// file is additive. See migration 013's LB3/NFR2/NFR3 boundary comment
// for why this table is append-only rather than SCD2, and why
// shipped-ness hangs off the (entity, container) association rather than
// the spec entity itself.
//
// Scaffold-stage: interface signatures and the compile-time interface
// assertion are in place; method bodies land in the Implementation phase.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DeliveryShipmentStore covers `delivery_shipment` (migration 013, issue
// #2686). Every write here is an INSERT only -- NFR2/LB3/NFR3 forbid an
// update or delete path anywhere in this package, so this interface
// deliberately has no such method, and none may be added later without
// revisiting that constraint.
type DeliveryShipmentStore interface {
	// MarkShipped appends one DeliveryShipment row recording that
	// entityID shipped as part of milestoneID's delivered scope (FR10),
	// and records the LB4 subject pair. Rejects (with a named, loud
	// error, never a silent skip) an entityID that is not a
	// relation='delivers' entity_milestone association of milestoneID --
	// you cannot ship what the container does not deliver, mirroring
	// AddMilepebbleDelivers' subset check (milestone_authoring.go).
	// Idempotent only at the read level: a second call for the same
	// (entityID, milestoneID) pair appends a second row rather than being
	// rejected or collapsed -- IsShipped/DeliveryBreakdown still report
	// entityID as shipped either way.
	MarkShipped(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, note *string, acting, onBehalfOf Subject) error

	// ShippedEntityIDs returns the set of entity ids that have at least
	// one DeliveryShipment row against milestoneID, in one query. An
	// entity id absent from the returned map has never been marked
	// shipped against this container.
	ShippedEntityIDs(ctx context.Context, milestoneID uuid.UUID) (map[uuid.UUID]bool, error)

	// DeliveryBreakdown partitions milestoneID's relation='delivers'
	// entity_milestone associations into shipped and unshipped entity id
	// slices (FR10). This is the "not-yet-shipped scope" definition the
	// re-cut and abandon work on this board both consume -- callers there
	// use the unshipped slice as the exact scope those operations act on.
	// A container with zero `delivers` associations returns two empty
	// slices, never an error.
	DeliveryBreakdown(ctx context.Context, milestoneID uuid.UUID) (shipped []uuid.UUID, unshipped []uuid.UUID, err error)
}

// deliveryShipmentStore is the pgx-backed DeliveryShipmentStore
// implementation.
type deliveryShipmentStore struct{ pool *pgxpool.Pool }

var _ DeliveryShipmentStore = deliveryShipmentStore{}

const deliveryShipmentColumns = `id, scope_id, entity_id, milestone_id, note, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func (s deliveryShipmentStore) MarkShipped(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, note *string, acting, onBehalfOf Subject) error {
	return fmt.Errorf("MarkShipped: not implemented")
}

func (s deliveryShipmentStore) ShippedEntityIDs(ctx context.Context, milestoneID uuid.UUID) (map[uuid.UUID]bool, error) {
	return nil, fmt.Errorf("ShippedEntityIDs: not implemented")
}

func (s deliveryShipmentStore) DeliveryBreakdown(ctx context.Context, milestoneID uuid.UUID) (shipped []uuid.UUID, unshipped []uuid.UUID, err error) {
	return nil, nil, fmt.Errorf("DeliveryBreakdown: not implemented")
}
