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
	// surrogate id (LB2) and the next product-wide DisplayNumber (`Cn`,
	// migration 017, issue #2969). Returns ErrNotFound if featureSetID has
	// no current `feature_set` row (LB2 parentage). Fails if
	// scopeID/featureSetID already has a current Feature with the same
	// name (LB1).
	Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, description *string) (Feature, error)

	// CreateWithDisplayNumber is Create, except displayNumber is taken
	// verbatim rather than auto-incremented -- krill/importer/write.go's
	// only caller, so an imported document's own `Cn` token survives the
	// import unchanged instead of being renumbered by creation order.
	// Every other caller (every MCP-driven create_feature) uses Create.
	CreateWithDisplayNumber(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, description *string, displayNumber int) (Feature, error)

	// GetCurrentByID returns the current row for id, or ErrNotFound.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (Feature, error)

	// ListCurrentByFeatureSet returns every current Feature under
	// featureSetID, ordered by Position then Name.
	ListCurrentByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]Feature, error)
}

type featureStore struct{ pool *pgxpool.Pool }

var _ FeatureStore = featureStore{}

const featureColumns = `revision_id, id, scope_id, feature_set_id, name, description, position, display_number, valid_from, valid_to`

func scanFeature(row pgx.Row) (Feature, error) {
	var f Feature
	err := row.Scan(&f.RevisionID, &f.ID, &f.ScopeID, &f.FeatureSetID, &f.Name, &f.Description, &f.Position, &f.DisplayNumber, &f.ValidFrom, &f.ValidTo)
	return f, err
}

// createFeatureTx is Create's/CreateWithDisplayNumber's body against any
// txQuerier, not just a freshly-begun transaction this method owns --
// extracted so krill/store/milestone_authoring.go's AddDiscoveredScope
// (FR4, issue #2684) can insert a discovered Feature inside its own
// transaction, alongside the milepebble/parent-milestone Delivers
// associations that must commit atomically with it, without a second,
// competing s.pool.Begin. Create/CreateWithDisplayNumber themselves are the
// only callers that own their transaction end-to-end.
//
// displayNumberOverride is nil for every ordinary create (Create computes
// the next product-wide number itself, nextDisplayNumber) and non-nil only
// for krill/importer's CreateWithDisplayNumber path, which supplies the
// source document's own `Cn` token verbatim (issue #2969).
func createFeatureTx(ctx context.Context, q txQuerier, scopeID, featureSetID uuid.UUID, name string, description *string, displayNumberOverride *int) (Feature, error) {
	var productID uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT product_id FROM feature_set WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL
	`, featureSetID, scopeID).Scan(&productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Feature{}, errParentNotFound("feature_set", featureSetID)
	}
	if err != nil {
		return Feature{}, fmt.Errorf("get feature_set for create: %w", err)
	}

	position, err := nextSiblingPosition(ctx, q, "feature", "feature_set_id", featureSetID, scopeID)
	if err != nil {
		return Feature{}, err
	}

	displayNumber := 0
	if displayNumberOverride != nil {
		displayNumber = *displayNumberOverride
	} else {
		displayNumber, err = nextDisplayNumber(ctx, q, "feature", productID, scopeID)
		if err != nil {
			return Feature{}, err
		}
	}

	feature, err := scanFeature(q.QueryRow(ctx, `
		INSERT INTO feature (scope_id, feature_set_id, name, description, position, display_number)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+featureColumns,
		scopeID, featureSetID, name, description, position, displayNumber))
	if err != nil {
		return Feature{}, fmt.Errorf("insert feature: %w", err)
	}
	return feature, nil
}

func (s featureStore) Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, description *string) (Feature, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Feature{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	feature, err := createFeatureTx(ctx, tx, scopeID, featureSetID, name, description, nil)
	if err != nil {
		return Feature{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Feature{}, fmt.Errorf("commit: %w", err)
	}
	return feature, nil
}

func (s featureStore) CreateWithDisplayNumber(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, description *string, displayNumber int) (Feature, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Feature{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	feature, err := createFeatureTx(ctx, tx, scopeID, featureSetID, name, description, &displayNumber)
	if err != nil {
		return Feature{}, err
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
