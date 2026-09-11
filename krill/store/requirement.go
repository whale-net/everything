package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RequirementStore covers `requirement` (migration 002) -- an FR or an
// NFR, discriminated by kind. Single parent: Feature.ID. An FR's citation
// of a capability ("FR4 (C3) -- ...") is exactly this Requirement's
// FeatureID, resolved against a real Feature row -- there is no separate
// string-keyed citation to validate.
type RequirementStore interface {
	// Create inserts a new Requirement row of the given kind (FR or NFR)
	// under featureID, minting a fresh surrogate id (LB2). Returns
	// ErrNotFound if featureID has no current `feature` row (LB2
	// parentage). Fails if scopeID/featureID already has a current
	// Requirement with the same name (LB1).
	Create(ctx context.Context, scopeID, featureID uuid.UUID, kind RequirementKind, name string, body *string) (Requirement, error)

	// GetCurrentByID returns the current row for id, or ErrNotFound.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (Requirement, error)

	// ListCurrentByFeature returns every current Requirement (both kinds)
	// under featureID, ordered by Kind, then Position, then Name.
	ListCurrentByFeature(ctx context.Context, featureID uuid.UUID) ([]Requirement, error)
}

type requirementStore struct{ pool *pgxpool.Pool }

var _ RequirementStore = requirementStore{}

const requirementColumns = `revision_id, id, scope_id, feature_id, kind, name, body, position, valid_from, valid_to`

func scanRequirement(row pgx.Row) (Requirement, error) {
	var r Requirement
	err := row.Scan(&r.RevisionID, &r.ID, &r.ScopeID, &r.FeatureID, &r.Kind, &r.Name, &r.Body, &r.Position, &r.ValidFrom, &r.ValidTo)
	return r, err
}

func (s requirementStore) Create(ctx context.Context, scopeID, featureID uuid.UUID, kind RequirementKind, name string, body *string) (Requirement, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Requirement{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	exists, err := currentRowExists(ctx, tx, "feature", featureID, scopeID)
	if err != nil {
		return Requirement{}, err
	}
	if !exists {
		return Requirement{}, errParentNotFound("feature", featureID)
	}

	requirement, err := scanRequirement(tx.QueryRow(ctx, `
		INSERT INTO requirement (scope_id, feature_id, kind, name, body)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+requirementColumns,
		scopeID, featureID, string(kind), name, body))
	if err != nil {
		return Requirement{}, fmt.Errorf("insert requirement: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Requirement{}, fmt.Errorf("commit: %w", err)
	}
	return requirement, nil
}

func (s requirementStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (Requirement, error) {
	requirement, err := scanRequirement(s.pool.QueryRow(ctx, `
		SELECT `+requirementColumns+`
		FROM requirement
		WHERE id = $1 AND valid_to IS NULL
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Requirement{}, fmt.Errorf("%w: requirement id %s", ErrNotFound, id)
	}
	if err != nil {
		return Requirement{}, fmt.Errorf("get current requirement: %w", err)
	}
	return requirement, nil
}

func (s requirementStore) ListCurrentByFeature(ctx context.Context, featureID uuid.UUID) ([]Requirement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+requirementColumns+`
		FROM requirement
		WHERE feature_id = $1 AND valid_to IS NULL
		ORDER BY kind, position, name
	`, featureID)
	if err != nil {
		return nil, fmt.Errorf("list current requirements: %w", err)
	}
	defer rows.Close()

	var requirements []Requirement
	for rows.Next() {
		r, err := scanRequirement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requirement: %w", err)
		}
		requirements = append(requirements, r)
	}
	return requirements, rows.Err()
}
