// This file is the move half of the SCD2 write path amend.go opens:
// supersede itself, reused to change WHERE an entity sits rather than
// what it says. A reparent is a Feature that keeps its immutable id and
// its `Cn`, closing its current row and opening a successor under a
// different FeatureSet -- the same physical operation AmendFeature
// performs, differing only in which column differs.
//
// It is a separate store rather than another method on AmendStore because
// amend.go's own contract is that no method on it ever reparents
// (AmendPlacementChange.Refuse points a caller who asks for one here).
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReparentStore is the spec axis's one placement verb: it moves an entity
// under a different parent, leaving every other field alone. Reparenting
// is not amending -- amend replaces an entity's content in place and
// refuses any request that also changes its parent (FR f0f6bc18) -- but
// it is the same close-and-open on the same row, so it goes through the
// same `supersede` helper rather than a second transaction shape.
type ReparentStore interface {
	// ReparentFeature closes the current row for id and opens a successor
	// under featureSetID, carrying the closed row's id, scope_id, name,
	// description, position, and display_number (the `Cn` a caller already
	// cites, migration 017) forward unchanged. Exactly one current Feature
	// row for id exists before and after.
	//
	// featureSetID must be a current FeatureSet of the Feature's own
	// scope AND of the same Product the Feature's current parent belongs
	// to. The same-Product rule is not cosmetic: a Feature's `Cn` is
	// numbered product-wide (position.go's nextDisplayNumber), and the
	// renderer resolves a milestone's `Delivers: Cn` list by looking the
	// entity up in the product slice it is rendering, so a Feature moved
	// across a product boundary would silently drop out of the
	// `Delivers` line of the milestone that delivers it.
	//
	// Refuses, writing nothing, with ErrReparentNoOp when featureSetID is
	// the parent the Feature already has (a no-op move is not a
	// revision), with ErrReparentAcrossProduct when the target sits under
	// another Product, with ErrNotFound when the target is not a current
	// FeatureSet of the Feature's scope, and with ErrNameConflict when a
	// live Feature of the target FeatureSet already holds the name.
	//
	// Position is carried forward, not renumbered: the Feature keeps the
	// position it had under its previous parent, so a collision with a
	// sibling's position in the target set is possible and harmless --
	// every read orders by (position, name), so ties fall through to name
	// deterministically. The consequence is that a move can leave the
	// moved Feature sitting at an arbitrary point in the target set's
	// rendered order rather than at the end of it; appending instead
	// would silently discard the ordering a caller chose when they
	// created the Feature. LoadBearingDecisions are FeatureSet-scoped,
	// not Feature-scoped, so they stay with the FeatureSet the Feature
	// moved out of; the Feature's own Requirements follow it, keyed on
	// its unchanged id.
	ReparentFeature(ctx context.Context, id, featureSetID uuid.UUID) (Feature, error)
}

type reparentStore struct{ pool *pgxpool.Pool }

var _ ReparentStore = reparentStore{}

func (s reparentStore) ReparentFeature(ctx context.Context, id, featureSetID uuid.UUID) (Feature, error) {
	return supersede(ctx, s.pool, "feature", featureColumns, scanFeature, id,
		func(ctx context.Context, q txQuerier, current Feature) (Feature, error) {
			if err := refuseFeatureReparent(ctx, q, current, featureSetID); err != nil {
				return Feature{}, err
			}
			moved, err := scanFeature(q.QueryRow(ctx, `
				INSERT INTO feature (id, scope_id, feature_set_id, name, description, position, display_number)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+featureColumns,
				current.ID, current.ScopeID, featureSetID, current.Name, current.Description, current.Position, current.DisplayNumber))
			return moved, errNameConflict("feature", "insert reparented feature", err)
		})
}

// ErrReparentNoOp is ReparentFeature's refusal when asked to move a
// Feature under the FeatureSet it is already under. Applying it anyway
// would close and reopen an identical row, spending a revision on a change
// that changed nothing and leaving a meaningless entry in the Feature's
// version list.
var ErrReparentNoOp = errors.New("krill/store: feature is already under this feature_set")

// ErrReparentAcrossProduct is ReparentFeature's refusal when the target
// FeatureSet belongs to a different Product than the one the Feature is
// currently under. A scope holds many Products, so a same-scope check
// alone would permit the move -- but a Feature's `Cn` is numbered
// product-wide and a milestone's rendered `Delivers` list resolves the
// entity through the Product's own slice, so a cross-Product move would
// carry the number into a second numbering space and drop the entity out
// of the milestone that delivers it. Move it with its own product's
// move path instead; this is not a verb that crosses Products.
var ErrReparentAcrossProduct = errors.New("krill/store: a feature cannot be reparented into another product's feature_set")

// refuseFeatureReparent validates a Feature's move to featureSetID
// against the three ways a move can be wrong, all of which leave the
// Feature where it is. It runs inside the supersede transaction, after
// the row is locked and closed -- any error it returns rolls the close
// back with the rest of the transaction, so a refused move writes nothing.
func refuseFeatureReparent(ctx context.Context, q txQuerier, current Feature, featureSetID uuid.UUID) error {
	if current.FeatureSetID == featureSetID {
		return fmt.Errorf("%w: feature %s", ErrReparentNoOp, current.ID)
	}

	var targetProductID uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT product_id FROM feature_set
		WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL
	`, featureSetID, current.ScopeID).Scan(&targetProductID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errParentNotFound("feature_set", featureSetID)
	}
	if err != nil {
		return fmt.Errorf("get feature_set for reparent: %w", err)
	}

	// The current parent's own product is read unfiltered on valid_to: a
	// Feature whose FeatureSet was voided is still parented to that row,
	// and refusing its move over a stale parent would be a worse answer
	// than the move itself.
	var currentProductID uuid.UUID
	err = q.QueryRow(ctx, `
		SELECT product_id FROM feature_set WHERE id = $1 AND scope_id = $2
	`, current.FeatureSetID, current.ScopeID).Scan(&currentProductID)
	if err != nil {
		return fmt.Errorf("get current feature_set for reparent: %w", err)
	}

	if targetProductID != currentProductID {
		return fmt.Errorf("%w: feature %s is under product %s, target feature_set %s is under product %s",
			ErrReparentAcrossProduct, current.ID, currentProductID, featureSetID, targetProductID)
	}
	return nil
}
