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
	"github.com/jackc/pgx/v5"
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

	// ListBacklog returns the entity ids currently in (scopeID,
	// productID)'s backlog bucket -- its relation='delivers'
	// `entity_milestone` associations, the same raw shape
	// DeliveryShipmentStore.DeliveryBreakdown returns. A product with no
	// backlog bucket yet (GetOrCreateBacklog never called for it) returns
	// an empty slice, not an error -- there is nothing to list, not a
	// missing parent. krill/slice.Querier.GetBacklog turns this id slice
	// into typed entities via GetEntitySetSlice (LB7), mirroring how
	// krill/slice.Querier.GetDeliveryBreakdown wraps DeliveryBreakdown's
	// own raw ids.
	ListBacklog(ctx context.Context, scopeID, productID uuid.UUID) ([]uuid.UUID, error)

	// MoveScope re-points entityIDs' relation='delivers' `entity_milestone`
	// associations from fromContainerID to toContainerID, in one
	// transaction (FR5). `from`/`to` may each name a milestone, a
	// milepebble, or the backlog bucket -- any `milestone_ref` row.
	//
	// Rejects, writing nothing (all-or-nothing over the whole entityIDs
	// batch -- every entityID is validated in one pass before any write
	// runs), if:
	//   - any entityID is not currently a Delivers association of from
	//     (ErrEntityNotInContainer);
	//   - any entityID is already shipped in from, checked directly
	//     against `delivery_shipment` inside this same transaction rather
	//     than through DeliveryShipmentStore.DeliveryBreakdown (which reads
	//     via the pool, not a tx, and so cannot share this transaction's
	//     snapshot) -- this is NFR3's tool-enforced half of "truncate,
	//     never rewind";
	//   - to is a milepebble and entityID is not already in that
	//     milepebble's parent milestone's Delivers set, unless from is
	//     itself a milepebble sharing that same parent (ErrMilepebbleDeliversNotSubset)
	//     -- see the FR3-subset paragraph below.
	//
	// Never touches a `delivery_shipment` row (NFR3: those are append-only
	// history, immutable by construction -- see migration 013's own LB3/
	// NFR2/NFR3 comment) and never changes any entity's own `id`, nor its
	// real spec-tree parent (FeatureSetID/FeatureID) (NFR1) -- only the
	// delivery-axis association moves. Because of that, this method never
	// calls #2682's nextSiblingPosition/nextSiblingPositionPlain: that
	// helper assigns `position` among a *spec entity's* real siblings
	// (Feature under a FeatureSet, Requirement under a Feature, ...) or a
	// milestone_ref row's own siblings when ONE OF THOSE ROWS IS CREATED --
	// MoveScope creates no milestone_ref row and does not reparent the
	// entity in the spec tree, so there is no sibling set here for that
	// helper to place anything into. `entity_milestone` itself carries no
	// `position` column at all (unlike `milestone_ref`). This resolves the
	// open question the Scaffold phase flagged on this method.
	//
	// FR3 subset invariant (a milepebble's Delivers set is always a subset
	// of its parent milestone's own, #2684): moving an item into a
	// milepebble whose parent does not yet deliver it is only allowed when
	// from is a sibling milepebble of that same parent -- in that case the
	// parent-level association is added in this same transaction (it
	// should already exist, by the same invariant on the source side; the
	// insert is an idempotent no-op if so). Every other source is rejected
	// rather than silently expanding the target milepebble's parent
	// milestone's own committed scope as a side effect of an unrelated
	// move.
	//
	// Moving an item OUT of a milestone that has milepebbles delivering it
	// (chosen over rejecting the move): dropping the milestone's own
	// Delivers association also drops the entity's Delivers association
	// with every milepebble cut from that milestone, in the same
	// transaction. A milepebble's Delivers set cannot outlive its parent's
	// no-longer-true claim to the item, and requiring a caller to first
	// walk every milepebble and detach the item there before it can move
	// the milestone-level association would make re-cutting a milestone
	// with milepebbles impractical for exactly the case FR5 exists for.
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

func (s recutStore) GetOrCreateBacklog(ctx context.Context, scopeID, productID uuid.UUID, acting, onBehalfOf Subject) (MilestoneRef, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	exists, err := currentRowExists(ctx, tx, "product", productID, scopeID)
	if err != nil {
		return MilestoneRef{}, err
	}
	if !exists {
		return MilestoneRef{}, errParentNotFound("product", productID)
	}

	// ON CONFLICT targets migration 014's partial unique index
	// (milestone_ref_backlog_product_idx: (scope_id, product_id) WHERE
	// kind = 'backlog') -- the same insert-then-fallback-SELECT shape
	// MilestoneStore.GetOrCreateRef uses against its own (differently
	// scoped) partial index.
	ref, err := scanMilestoneRef(tx.QueryRow(ctx, `
		INSERT INTO milestone_ref (
			scope_id, product_id, name, kind,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (scope_id, product_id) WHERE kind = 'backlog' DO NOTHING
		RETURNING `+milestoneRefColumns,
		scopeID, productID, backlogName, string(MilestoneKindBacklog),
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)))
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING returned no row: this product's backlog
		// bucket already exists, so fetch it instead.
		ref, err = scanMilestoneRef(tx.QueryRow(ctx, `
			SELECT `+milestoneRefColumns+`
			FROM milestone_ref
			WHERE scope_id = $1 AND product_id = $2 AND kind = $3
		`, scopeID, productID, string(MilestoneKindBacklog)))
	}
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("get or create backlog milestone_ref: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return MilestoneRef{}, fmt.Errorf("commit: %w", err)
	}
	return ref, nil
}

func (s recutStore) ListBacklog(ctx context.Context, scopeID, productID uuid.UUID) ([]uuid.UUID, error) {
	var backlogID uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM milestone_ref WHERE scope_id = $1 AND product_id = $2 AND kind = $3
	`, scopeID, productID, string(MilestoneKindBacklog)).Scan(&backlogID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No backlog bucket has ever been created for this product --
		// nothing has been moved into it, so its contents are empty.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get backlog milestone_ref: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT entity_id FROM entity_milestone WHERE milestone_id = $1 AND relation = $2
	`, backlogID, string(MilestoneRelationDelivers))
	if err != nil {
		return nil, fmt.Errorf("list backlog entity_milestone: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan backlog entity_milestone: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// milestoneRefKindAndParent looks up id's Kind and ParentMilestoneID
// inside q, scoped to scopeID -- the parentage/kind check MoveScope needs
// for both its `from` and `to` container ids. Mirrors
// getMilepebbleParent's shape (milestone_authoring.go) but accepts any
// Kind rather than requiring MilestoneKindMilepebble, since MoveScope's
// containers may each be a milestone, a milepebble, or the backlog
// bucket.
func milestoneRefKindAndParent(ctx context.Context, q txQuerier, scopeID, id uuid.UUID) (MilestoneKind, *uuid.UUID, error) {
	var kind string
	var parentMilestoneID uuid.NullUUID
	err := q.QueryRow(ctx, `
		SELECT kind, parent_milestone_id FROM milestone_ref WHERE id = $1 AND scope_id = $2
	`, id, scopeID).Scan(&kind, &parentMilestoneID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, errParentNotFound("milestone_ref", id)
	}
	if err != nil {
		return "", nil, fmt.Errorf("get milestone_ref: %w", err)
	}
	var parent *uuid.UUID
	if parentMilestoneID.Valid {
		parent = &parentMilestoneID.UUID
	}
	return MilestoneKind(kind), parent, nil
}

func (s recutStore) MoveScope(ctx context.Context, scopeID uuid.UUID, entityIDs []uuid.UUID, fromContainerID, toContainerID uuid.UUID, acting, onBehalfOf Subject) error {
	if len(entityIDs) == 0 {
		return fmt.Errorf("entity_ids: at least one entity id is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	fromKind, fromParent, err := milestoneRefKindAndParent(ctx, tx, scopeID, fromContainerID)
	if err != nil {
		return err
	}
	toKind, toParent, err := milestoneRefKindAndParent(ctx, tx, scopeID, toContainerID)
	if err != nil {
		return err
	}

	// Pass 1: validate every entityID before writing anything -- NFR3's
	// "fails loudly and writes nothing", never a partial apply over the
	// batch.
	for _, entityID := range entityIDs {
		var delivers bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM entity_milestone
				WHERE entity_id = $1 AND milestone_id = $2 AND relation = $3
			)
		`, entityID, fromContainerID, string(MilestoneRelationDelivers)).Scan(&delivers); err != nil {
			return fmt.Errorf("check delivers association: %w", err)
		}
		if !delivers {
			return fmt.Errorf("%w: entity %s, container %s", ErrEntityNotInContainer, entityID, fromContainerID)
		}

		// Checked directly against `delivery_shipment` inside this same
		// transaction (rather than via
		// DeliveryShipmentStore.DeliveryBreakdown, which queries through
		// the pool) so the shipped-check and the move it gates share one
		// snapshot -- no window where a concurrent MarkShipped could race
		// this validation pass.
		var shipped bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2)
		`, entityID, fromContainerID).Scan(&shipped); err != nil {
			return fmt.Errorf("check delivery_shipment: %w", err)
		}
		if shipped {
			return fmt.Errorf("%w: entity %s, container %s", ErrEntityShipped, entityID, fromContainerID)
		}

		if toKind == MilestoneKindMilepebble {
			var inParentDelivers bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM entity_milestone
					WHERE entity_id = $1 AND milestone_id = $2 AND relation = $3
				)
			`, entityID, *toParent, string(MilestoneRelationDelivers)).Scan(&inParentDelivers); err != nil {
				return fmt.Errorf("check parent milestone delivers: %w", err)
			}
			if !inParentDelivers {
				sameParentSibling := fromKind == MilestoneKindMilepebble && fromParent != nil && *fromParent == *toParent
				if !sameParentSibling {
					return fmt.Errorf("%w: entity %s, milepebble %s, parent milestone %s", ErrMilepebbleDeliversNotSubset, entityID, toContainerID, *toParent)
				}
			}
		}
	}

	// Pass 2: apply. Every statement here is idempotent (ON CONFLICT DO
	// NOTHING, or a DELETE that may legitimately match zero rows), so this
	// loop always converges on the same end state pass 1 already proved
	// safe for every entityID.
	for _, entityID := range entityIDs {
		if _, err := tx.Exec(ctx, `
			DELETE FROM entity_milestone WHERE entity_id = $1 AND milestone_id = $2 AND relation = $3
		`, entityID, fromContainerID, string(MilestoneRelationDelivers)); err != nil {
			return fmt.Errorf("delete from-association: %w", err)
		}

		// Moving an item out of a milestone also drops its Delivers
		// association with every milepebble cut from that milestone (the
		// "drop" half of the FR3-subset choice documented on this
		// method's interface doc comment).
		if fromKind == MilestoneKindMilestone {
			if _, err := tx.Exec(ctx, `
				DELETE FROM entity_milestone
				WHERE entity_id = $1 AND relation = $2
				  AND milestone_id IN (SELECT id FROM milestone_ref WHERE parent_milestone_id = $3)
			`, entityID, string(MilestoneRelationDelivers), fromContainerID); err != nil {
				return fmt.Errorf("drop milepebble associations: %w", err)
			}
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (entity_id, milestone_id, relation) DO NOTHING
		`, scopeID, entityID, toContainerID, string(MilestoneRelationDelivers)); err != nil {
			return fmt.Errorf("insert to-association: %w", err)
		}

		if toKind == MilestoneKindMilepebble {
			if _, err := tx.Exec(ctx, `
				INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (entity_id, milestone_id, relation) DO NOTHING
			`, scopeID, entityID, *toParent, string(MilestoneRelationDelivers)); err != nil {
				return fmt.Errorf("insert parent milestone association: %w", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
