package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FeatureStore covers `feature` (migration 002). Single parent:
// FeatureSet.ID.
//
// Feature is also the entity a capability-map entry (`Cn`) resolves onto
// (see krill/ARCHITECTURE.md and models.go's doc comment on Feature) --
// this store has no separate "capability" concept or column.
type FeatureStore interface {
	// Create inserts a new Feature row under featureSetID, minting a fresh
	// surrogate id (LB2). Returns ErrNotFound if featureSetID has no
	// current `feature_set` row (LB2 parentage). Fails if
	// scopeID/featureSetID already has a current Feature with the same
	// name (LB1).
	Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, description *string) (Feature, error)

	// GetCurrentByID returns the current row for id, or ErrNotFound.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (Feature, error)

	// ListCurrentByFeatureSet returns every current Feature under
	// featureSetID, ordered by Position then Name.
	ListCurrentByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]Feature, error)
}

type featureStore struct{ pool *pgxpool.Pool }

var _ FeatureStore = featureStore{}

const featureColumns = `revision_id, id, scope_id, feature_set_id, name, description, position, valid_from, valid_to`

func scanFeature(row pgx.Row) (Feature, error) {
	var f Feature
	err := row.Scan(&f.RevisionID, &f.ID, &f.ScopeID, &f.FeatureSetID, &f.Name, &f.Description, &f.Position, &f.ValidFrom, &f.ValidTo)
	return f, err
}

func (s featureStore) Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, description *string) (Feature, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Feature{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	exists, err := currentRowExists(ctx, tx, "feature_set", featureSetID)
	if err != nil {
		return Feature{}, err
	}
	if !exists {
		return Feature{}, errParentNotFound("feature_set", featureSetID)
	}

	feature, err := scanFeature(tx.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, description)
		VALUES ($1, $2, $3, $4)
		RETURNING `+featureColumns,
		scopeID, featureSetID, name, description))
	if err != nil {
		return Feature{}, fmt.Errorf("insert feature: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Feature{}, fmt.Errorf("commit: %w", err)
	}
	return feature, nil
}

func (s featureStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (Feature, error) {
	feature, err := scanFeature(s.pool.QueryRow(ctx, `
		SELECT `+featureColumns+`
		FROM feature
		WHERE id = $1 AND valid_to IS NULL
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Feature{}, fmt.Errorf("%w: feature id %s", ErrNotFound, id)
	}
	if err != nil {
		return Feature{}, fmt.Errorf("get current feature: %w", err)
	}
	return feature, nil
}

func (s featureStore) ListCurrentByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]Feature, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+featureColumns+`
		FROM feature
		WHERE feature_set_id = $1 AND valid_to IS NULL
		ORDER BY position, name
	`, featureSetID)
	if err != nil {
		return nil, fmt.Errorf("list current features: %w", err)
	}
	defer rows.Close()

	var features []Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, fmt.Errorf("scan feature: %w", err)
		}
		features = append(features, f)
	}
	return features, rows.Err()
}
