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
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

// ErrEntityNotDelivered is MarkShipped's named, loud rejection: entityID
// is not a relation='delivers' `entity_milestone` association of
// milestoneID -- you cannot ship what the container does not deliver,
// mirroring ErrMilepebbleDeliversNotSubset's role in
// AddMilepebbleDelivers (milestone_authoring.go).
var ErrEntityNotDelivered = errors.New("krill/store: entity is not a delivers association of this milestone")

func (s deliveryShipmentStore) MarkShipped(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, note *string, acting, onBehalfOf Subject) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var delivers bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM entity_milestone
			WHERE entity_id = $1 AND milestone_id = $2 AND relation = $3
		)
	`, entityID, milestoneID, string(MilestoneRelationDelivers)).Scan(&delivers); err != nil {
		return fmt.Errorf("check delivers association: %w", err)
	}
	if !delivers {
		return fmt.Errorf("%w: entity %s, milestone %s", ErrEntityNotDelivered, entityID, milestoneID)
	}

	// Plain INSERT, no ON CONFLICT -- NFR2/NFR3 require a second call for
	// the same (entityID, milestoneID) pair to append a second row, never
	// collapse or reject as a duplicate. Idempotency for a reader ("is
	// this shipped") lives in ShippedEntityIDs/DeliveryBreakdown, not here.
	if _, err := tx.Exec(ctx, `
		INSERT INTO delivery_shipment (
			scope_id, entity_id, milestone_id, note,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, scopeID, entityID, milestoneID, note,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)); err != nil {
		return fmt.Errorf("insert delivery_shipment: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s deliveryShipmentStore) ShippedEntityIDs(ctx context.Context, milestoneID uuid.UUID) (map[uuid.UUID]bool, error) {
	return shippedEntityIDs(ctx, s.pool, milestoneID)
}

// deliveryQueryer is the one method ShippedEntityIDs/DeliveryBreakdown's
// shared cores need -- satisfied by both *pgxpool.Pool (their own
// pool-backed methods) and pgx.Tx (Abandon, abandon.go, issue #2688,
// which computes a container's not-yet-shipped scope as one read inside
// its own transaction, the same snapshot the move step that consumes it
// runs against).
type deliveryQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func shippedEntityIDs(ctx context.Context, q deliveryQueryer, milestoneID uuid.UUID) (map[uuid.UUID]bool, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT entity_id FROM delivery_shipment WHERE milestone_id = $1
	`, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("list delivery_shipment entity ids: %w", err)
	}
	defer rows.Close()

	result := make(map[uuid.UUID]bool)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan delivery_shipment entity id: %w", err)
		}
		result[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list delivery_shipment entity ids: %w", err)
	}
	return result, nil
}

func (s deliveryShipmentStore) DeliveryBreakdown(ctx context.Context, milestoneID uuid.UUID) (shipped []uuid.UUID, unshipped []uuid.UUID, err error) {
	return deliveryBreakdown(ctx, s.pool, milestoneID)
}

func deliveryBreakdown(ctx context.Context, q deliveryQueryer, milestoneID uuid.UUID) (shipped []uuid.UUID, unshipped []uuid.UUID, err error) {
	rows, err := q.Query(ctx, `
		SELECT entity_id FROM entity_milestone
		WHERE milestone_id = $1 AND relation = $2
	`, milestoneID, string(MilestoneRelationDelivers))
	if err != nil {
		return nil, nil, fmt.Errorf("list entity_milestone delivers: %w", err)
	}

	var deliveredIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan entity_milestone: %w", err)
		}
		deliveredIDs = append(deliveredIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list entity_milestone delivers: %w", err)
	}

	// A container with zero `delivers` associations short-circuits here:
	// deliveredIDs is nil, so both returned slices stay nil, and
	// shippedEntityIDs is never called for an id set that would only ever
	// produce an empty map.
	if len(deliveredIDs) == 0 {
		return nil, nil, nil
	}

	shippedSet, err := shippedEntityIDs(ctx, q, milestoneID)
	if err != nil {
		return nil, nil, err
	}

	for _, id := range deliveredIDs {
		if shippedSet[id] {
			shipped = append(shipped, id)
		} else {
			unshipped = append(unshipped, id)
		}
	}
	return shipped, unshipped, nil
}
