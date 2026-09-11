package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FeatureSetStore covers `feature_set` (migration 002). Single parent:
// Product.ID.
type FeatureSetStore interface {
	// Create inserts a new FeatureSet row under productID, minting a fresh
	// surrogate id (LB2). Returns ErrNotFound if productID has no current
	// `product` row (LB2 parentage -- enforced here, not by a DB FK; see
	// migration 002's comment). Fails if scopeID/productID already has a
	// current FeatureSet with the same name (LB1).
	Create(ctx context.Context, scopeID, productID uuid.UUID, name string, description *string) (FeatureSet, error)

	// GetCurrentByID returns the current row for id, or ErrNotFound.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (FeatureSet, error)

	// ListCurrentByProduct returns every current FeatureSet under
	// productID, ordered by Position then Name.
	ListCurrentByProduct(ctx context.Context, productID uuid.UUID) ([]FeatureSet, error)
}

type featureSetStore struct{ pool *pgxpool.Pool }

var _ FeatureSetStore = featureSetStore{}

const featureSetColumns = `revision_id, id, scope_id, product_id, name, description, position, valid_from, valid_to`

func scanFeatureSet(row pgx.Row) (FeatureSet, error) {
	var fs FeatureSet
	err := row.Scan(&fs.RevisionID, &fs.ID, &fs.ScopeID, &fs.ProductID, &fs.Name, &fs.Description, &fs.Position, &fs.ValidFrom, &fs.ValidTo)
	return fs, err
}

func (s featureSetStore) Create(ctx context.Context, scopeID, productID uuid.UUID, name string, description *string) (FeatureSet, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return FeatureSet{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	exists, err := currentRowExists(ctx, tx, "product", productID)
	if err != nil {
		return FeatureSet{}, err
	}
	if !exists {
		return FeatureSet{}, errParentNotFound("product", productID)
	}

	featureSet, err := scanFeatureSet(tx.QueryRow(ctx, `
		INSERT INTO feature_set (scope_id, product_id, name, description)
		VALUES ($1, $2, $3, $4)
		RETURNING `+featureSetColumns,
		scopeID, productID, name, description))
	if err != nil {
		return FeatureSet{}, fmt.Errorf("insert feature_set: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return FeatureSet{}, fmt.Errorf("commit: %w", err)
	}
	return featureSet, nil
}

func (s featureSetStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (FeatureSet, error) {
	featureSet, err := scanFeatureSet(s.pool.QueryRow(ctx, `
		SELECT `+featureSetColumns+`
		FROM feature_set
		WHERE id = $1 AND valid_to IS NULL
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return FeatureSet{}, fmt.Errorf("%w: feature_set id %s", ErrNotFound, id)
	}
	if err != nil {
		return FeatureSet{}, fmt.Errorf("get current feature_set: %w", err)
	}
	return featureSet, nil
}

func (s featureSetStore) ListCurrentByProduct(ctx context.Context, productID uuid.UUID) ([]FeatureSet, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+featureSetColumns+`
		FROM feature_set
		WHERE product_id = $1 AND valid_to IS NULL
		ORDER BY position, name
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list current feature_sets: %w", err)
	}
	defer rows.Close()

	var featureSets []FeatureSet
	for rows.Next() {
		fs, err := scanFeatureSet(rows)
		if err != nil {
			return nil, fmt.Errorf("scan feature_set: %w", err)
		}
		featureSets = append(featureSets, fs)
	}
	return featureSets, rows.Err()
}
