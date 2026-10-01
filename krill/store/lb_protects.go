package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LBProtectsStore covers `lb_protects_feature` (migration 033): the
// association recording that a load-bearing decision protects a Feature. It
// is not a parent link -- a decision stays single-parent under its
// FeatureSet. Edges are never deleted; Withdraw stamps withdrawn_at and the
// acting / on_behalf_of pair, and a withdrawn pair may be added again.
type LBProtectsStore interface {
	// Add records decisionID as protecting featureID. Both must be current
	// rows in scopeID and belong to the same Product (ErrNotFound
	// otherwise). Idempotent: an already-active edge is returned unchanged.
	Add(ctx context.Context, scopeID, decisionID, featureID uuid.UUID) (LBProtectsFeature, error)

	// Withdraw retires the active edge for (decisionID, featureID), keeping
	// its row. Returns ErrNotFound if no active edge exists.
	Withdraw(ctx context.Context, scopeID, decisionID, featureID uuid.UUID, acting, onBehalfOf Subject) error

	// ListActiveByFeatures returns every active edge whose feature_id is in
	// featureIDs, ordered by creation time. Empty input yields no rows.
	ListActiveByFeatures(ctx context.Context, featureIDs []uuid.UUID) ([]LBProtectsFeature, error)
}

type lbProtectsStore struct{ pool *pgxpool.Pool }

var _ LBProtectsStore = lbProtectsStore{}

// LBProtects returns the LBProtectsStore implementation.
func (s *Store) LBProtects() LBProtectsStore { return lbProtectsStore{pool: s.pool} }

const lbProtectsActiveColumns = `id, scope_id, decision_id, feature_id, created_at`

func (s lbProtectsStore) Add(ctx context.Context, scopeID, decisionID, featureID uuid.UUID) (LBProtectsFeature, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LBProtectsFeature{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var decisionProduct, featureProduct uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT fs.product_id FROM load_bearing_decision d
		JOIN feature_set fs ON fs.id = d.feature_set_id AND fs.valid_to IS NULL
		WHERE d.id = $1 AND d.scope_id = $2 AND d.valid_to IS NULL
	`, decisionID, scopeID).Scan(&decisionProduct)
	if err != nil {
		return LBProtectsFeature{}, notFoundOr(err, "load_bearing_decision", decisionID)
	}
	err = tx.QueryRow(ctx, `
		SELECT fs.product_id FROM feature f
		JOIN feature_set fs ON fs.id = f.feature_set_id AND fs.valid_to IS NULL
		WHERE f.id = $1 AND f.scope_id = $2 AND f.valid_to IS NULL
	`, featureID, scopeID).Scan(&featureProduct)
	if err != nil {
		return LBProtectsFeature{}, notFoundOr(err, "feature", featureID)
	}
	if decisionProduct != featureProduct {
		return LBProtectsFeature{}, fmt.Errorf("%w: decision %s and feature %s belong to different products", ErrNotFound, decisionID, featureID)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO lb_protects_feature (scope_id, decision_id, feature_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (decision_id, feature_id) WHERE withdrawn_at IS NULL DO NOTHING
	`, scopeID, decisionID, featureID); err != nil {
		return LBProtectsFeature{}, fmt.Errorf("insert lb_protects_feature: %w", err)
	}
	var e LBProtectsFeature
	if err := tx.QueryRow(ctx, `
		SELECT `+lbProtectsActiveColumns+` FROM lb_protects_feature_active
		WHERE decision_id = $1 AND feature_id = $2
	`, decisionID, featureID).Scan(&e.ID, &e.ScopeID, &e.DecisionID, &e.FeatureID, &e.CreatedAt); err != nil {
		return LBProtectsFeature{}, fmt.Errorf("read lb_protects_feature: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return LBProtectsFeature{}, fmt.Errorf("commit: %w", err)
	}
	return e, nil
}

func (s lbProtectsStore) Withdraw(ctx context.Context, scopeID, decisionID, featureID uuid.UUID, acting, onBehalfOf Subject) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE lb_protects_feature SET
			withdrawn_at = NOW(),
			withdrawn_by_acting_iss = $4, withdrawn_by_acting_sub = $5, withdrawn_by_acting_kind = $6,
			withdrawn_by_on_behalf_iss = $7, withdrawn_by_on_behalf_sub = $8, withdrawn_by_on_behalf_kind = $9
		WHERE scope_id = $1 AND decision_id = $2 AND feature_id = $3 AND withdrawn_at IS NULL
	`, scopeID, decisionID, featureID,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind))
	if err != nil {
		return fmt.Errorf("withdraw lb_protects_feature: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: no active protects edge from decision %s to feature %s", ErrNotFound, decisionID, featureID)
	}
	return nil
}

func (s lbProtectsStore) ListActiveByFeatures(ctx context.Context, featureIDs []uuid.UUID) ([]LBProtectsFeature, error) {
	if len(featureIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+lbProtectsActiveColumns+` FROM lb_protects_feature_active
		WHERE feature_id = ANY($1) ORDER BY created_at, id
	`, featureIDs)
	if err != nil {
		return nil, fmt.Errorf("list lb_protects_feature: %w", err)
	}
	defer rows.Close()
	var out []LBProtectsFeature
	for rows.Next() {
		var e LBProtectsFeature
		if err := rows.Scan(&e.ID, &e.ScopeID, &e.DecisionID, &e.FeatureID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan lb_protects_feature: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
