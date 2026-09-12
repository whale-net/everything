// This file (issue #2493, FR11) is the read side of SCD2 supersession
// (AGENTS.md "SCD2" -- "Value at time T"): as-of reads and version lists
// for the same two entity kinds store/amend.go's write path covers,
// Requirement and LoadBearingDecision. Never gated by `init` (FR3 is
// write-only) -- a read path, exactly like krill/slice's four
// granularities.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// HistoryStore is FR11: as-of reads and version lists over Requirement and
// LoadBearingDecision's SCD2 history.
type HistoryStore interface {
	// GetRequirementAsOf returns the revision of id that was current at
	// asOf -- `valid_from <= asOf AND (valid_to IS NULL OR valid_to > asOf)`
	// (AGENTS.md "SCD2"'s "Value at time T" query, applied to `requirement`).
	// Returns ErrNotFound if id names no revision current at asOf -- either
	// asOf predates the entity's first revision, or id never existed.
	GetRequirementAsOf(ctx context.Context, id uuid.UUID, asOf time.Time) (Requirement, error)

	// ListRequirementVersions returns every revision of id, oldest first,
	// each carrying its own ValidFrom and (for every revision but the
	// current one) the ValidTo timestamp it was superseded at. Returns
	// ErrNotFound if id names no revision at all.
	ListRequirementVersions(ctx context.Context, id uuid.UUID) ([]Requirement, error)

	// GetLoadBearingDecisionAsOf mirrors GetRequirementAsOf for
	// `load_bearing_decision`.
	GetLoadBearingDecisionAsOf(ctx context.Context, id uuid.UUID, asOf time.Time) (LoadBearingDecision, error)

	// ListLoadBearingDecisionVersions mirrors ListRequirementVersions for
	// `load_bearing_decision`.
	ListLoadBearingDecisionVersions(ctx context.Context, id uuid.UUID) ([]LoadBearingDecision, error)
}

type historyStore struct{ pool *pgxpool.Pool }

var _ HistoryStore = historyStore{}

func (s historyStore) GetRequirementAsOf(ctx context.Context, id uuid.UUID, asOf time.Time) (Requirement, error) {
	requirement, err := scanRequirement(s.pool.QueryRow(ctx, `
		SELECT `+requirementColumns+`
		FROM requirement
		WHERE id = $1 AND valid_from <= $2 AND (valid_to IS NULL OR valid_to > $2)
	`, id, asOf))
	if errors.Is(err, pgx.ErrNoRows) {
		return Requirement{}, fmt.Errorf("%w: requirement id %s as of %s", ErrNotFound, id, asOf)
	}
	if err != nil {
		return Requirement{}, fmt.Errorf("get requirement as of %s: %w", asOf, err)
	}
	return requirement, nil
}

func (s historyStore) ListRequirementVersions(ctx context.Context, id uuid.UUID) ([]Requirement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+requirementColumns+`
		FROM requirement
		WHERE id = $1
		ORDER BY valid_from
	`, id)
	if err != nil {
		return nil, fmt.Errorf("list requirement versions: %w", err)
	}
	defer rows.Close()

	var versions []Requirement
	for rows.Next() {
		r, err := scanRequirement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requirement version: %w", err)
		}
		versions = append(versions, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: requirement id %s", ErrNotFound, id)
	}
	return versions, nil
}

func (s historyStore) GetLoadBearingDecisionAsOf(ctx context.Context, id uuid.UUID, asOf time.Time) (LoadBearingDecision, error) {
	decision, err := scanLoadBearingDecision(s.pool.QueryRow(ctx, `
		SELECT `+loadBearingDecisionColumns+`
		FROM load_bearing_decision
		WHERE id = $1 AND valid_from <= $2 AND (valid_to IS NULL OR valid_to > $2)
	`, id, asOf))
	if errors.Is(err, pgx.ErrNoRows) {
		return LoadBearingDecision{}, fmt.Errorf("%w: load_bearing_decision id %s as of %s", ErrNotFound, id, asOf)
	}
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("get load_bearing_decision as of %s: %w", asOf, err)
	}
	return decision, nil
}

func (s historyStore) ListLoadBearingDecisionVersions(ctx context.Context, id uuid.UUID) ([]LoadBearingDecision, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+loadBearingDecisionColumns+`
		FROM load_bearing_decision
		WHERE id = $1
		ORDER BY valid_from
	`, id)
	if err != nil {
		return nil, fmt.Errorf("list load_bearing_decision versions: %w", err)
	}
	defer rows.Close()

	var versions []LoadBearingDecision
	for rows.Next() {
		d, err := scanLoadBearingDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan load_bearing_decision version: %w", err)
		}
		versions = append(versions, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: load_bearing_decision id %s", ErrNotFound, id)
	}
	return versions, nil
}
