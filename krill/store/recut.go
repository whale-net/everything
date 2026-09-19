// This file (issue #2687, FR5, C13) is RecutStore -- the delivery-axis
// re-cut surface: moving a milestone's or milepebble's not-yet-shipped
// scope to a different milestone, a different milepebble, or the backlog
// bucket (migration 014), while every item #2686's DeliveryBreakdown
// already reports as shipped stays exactly as recorded (NFR3). Kept as a
// sibling accessor ((*Store).Recut()) next to MilestoneAuthoringStore and
// DeliveryShipmentStore, mirroring their own relationship to the bare
// MilestoneStore -- this file writes no new table, only `entity_milestone`
// rows the existing accessors already own.
//
// The backlog bucket this file also creates (GetOrCreateBacklog) is the
// move destination FR5 names and FR6 (abandon, a separate issue on this
// board) also consumes -- see migration 014's comment and
// krill/ARCHITECTURE.md's "The backlog bucket" section for why it is a
// `milestone_ref` row (kind='backlog'), never a parallel table, and never
// the same thing as the product's own `Later` capability-map bucket.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecutStore covers the backlog bucket and the scope-move primitive
// (migration 014, issue #2687, FR5). Every method here follows the same
// LB2 parentage / LB4 subject-pair conventions as every other write in
// this package.
type RecutStore interface {
	// GetOrCreateBacklog resolves the one reserved-name `milestone_ref`
	// row (kind='backlog', parent_milestone_id NULL) for (scopeID,
	// productID), creating it on first use -- idempotent, mirroring
	// MilestoneStore.GetOrCreateRef's ON-CONFLICT-then-fallback-SELECT
	// shape, scoped by migration 014's partial unique index
	// (milestone_ref_backlog_product_idx) instead of the name-based one
	// GetOrCreateRef relies on. Returns ErrNotFound if productID has no
	// current `product` row in scopeID (LB2 parentage). Two calls for the
	// same (scopeID, productID) always resolve to the same row; two
	// different products resolve to two different rows.
	GetOrCreateBacklog(ctx context.Context, scopeID, productID uuid.UUID, acting, onBehalfOf Subject) (MilestoneRef, error)

	// MoveScope re-points entityIDs' relation='delivers' `entity_milestone`
	// associations from fromContainerID to toContainerID, in one
	// transaction (FR5). `from`/`to` may each name a milestone, a
	// milepebble, or the backlog bucket -- any `milestone_ref` row.
	//
	// Rejects, writing nothing (all-or-nothing over the whole entityIDs
	// batch), if:
	//   - any entityID is not currently a Delivers association of from
	//     (ErrEntityNotInContainer);
	//   - any entityID is already shipped in from, per
	//     DeliveryShipmentStore.DeliveryBreakdown (ErrEntityShipped) --
	//     this is NFR3's tool-enforced half of "truncate, never rewind".
	//
	// Never touches a `delivery_shipment` row (NFR3: those are append-only
	// history, immutable by construction -- see migration 013's own LB3/
	// NFR2/NFR3 comment) and never changes any entity's own `id` (NFR1) --
	// only the association moves.
	//
	// Also preserves the FR3 subset invariant (a milepebble's Delivers set
	// is always a subset of its parent milestone's own) across the move;
	// see this method's Implementation-phase doc comment for the exact
	// rule chosen between "add to the new parent" and "reject" when a move
	// would otherwise violate it.
	MoveScope(ctx context.Context, scopeID uuid.UUID, entityIDs []uuid.UUID, fromContainerID, toContainerID uuid.UUID, acting, onBehalfOf Subject) error
}

// recutStore is the pgx-backed RecutStore implementation.
type recutStore struct{ pool *pgxpool.Pool }

var _ RecutStore = recutStore{}

// backlogName is the backlog bucket's fixed, reserved `milestone_ref.name`
// -- never a caller-chosen value, unlike a milestone's or a milepebble's
// own name. GetOrCreateBacklog always writes (or looks up) exactly this
// name, so migration 014's partial unique index
// (scope_id, product_id WHERE kind='backlog') is the real arbiter of
// idempotency; this constant only needs to be stable, not meaningful.
const backlogName = "__backlog__"

// ErrEntityShipped is MoveScope's named, loud rejection (NFR3): entityID
// is already shipped in the `from` container (per
// DeliveryShipmentStore.DeliveryBreakdown), so the whole batch is rejected
// and nothing is written -- never silently skipped, never partially
// applied.
var ErrEntityShipped = errors.New("krill/store: entity is already shipped and cannot be re-cut")

// ErrEntityNotInContainer is MoveScope's named rejection when entityID is
// not currently a relation='delivers' `entity_milestone` association of
// the `from` container -- there is nothing to move.
var ErrEntityNotInContainer = errors.New("krill/store: entity is not a delivers association of the from container")

// GetOrCreateBacklog is implemented in the Implementation phase (issue
// #2687) -- see this method's interface doc comment for its full contract.
func (s recutStore) GetOrCreateBacklog(ctx context.Context, scopeID, productID uuid.UUID, acting, onBehalfOf Subject) (MilestoneRef, error) {
	return MilestoneRef{}, fmt.Errorf("krill/store: RecutStore.GetOrCreateBacklog not yet implemented")
}

// MoveScope is implemented in the Implementation phase (issue #2687) --
// see this method's interface doc comment for its full contract.
func (s recutStore) MoveScope(ctx context.Context, scopeID uuid.UUID, entityIDs []uuid.UUID, fromContainerID, toContainerID uuid.UUID, acting, onBehalfOf Subject) error {
	return fmt.Errorf("krill/store: RecutStore.MoveScope not yet implemented")
}
