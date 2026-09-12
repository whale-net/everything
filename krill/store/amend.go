// This file (issue #2493, FR12) is the SCD2 close-and-open write path
// AGENTS.md's "SCD2" section describes, applied to the two entity kinds
// this task scopes for amendment: Requirement (FR/NFR) and
// LoadBearingDecision. Migration 002 already shipped every column this
// write needs (krill/ARCHITECTURE.md's "What this task does not build"
// note, now resolved) -- no new migration lands with this task.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AmendStore is the write side of FR12: closing an entity's current row
// and opening a new one under the SAME surrogate id (LB2). Neither method
// here ever mints a new id, ever rewrites `position`, or ever touches a
// sibling's row -- an amendment is a supersession of exactly one logical
// entity, not a reparent or a reorder. Reparenting (changing
// feature_id/feature_set_id) is deliberately out of scope: see
// store/decision.go's Create doc comment and migration 002's LB3 note for
// why that would be a different, still-unbuilt operation.
type AmendStore interface {
	// AmendRequirement closes the current row for id (`valid_to = NOW()`)
	// and inserts a new current row carrying the closed row's id, scope_id,
	// feature_id, kind, and position unchanged, with name and body as
	// given. Returns ErrNotFound if id has no current row.
	AmendRequirement(ctx context.Context, id uuid.UUID, name string, body *string) (Requirement, error)

	// AmendLoadBearingDecision closes the current row for id and inserts a
	// new current row carrying the closed row's id, scope_id,
	// feature_set_id, and position unchanged, with name and body as given.
	// Returns ErrNotFound if id has no current row.
	AmendLoadBearingDecision(ctx context.Context, id uuid.UUID, name string, body *string) (LoadBearingDecision, error)
}

type amendStore struct{ pool *pgxpool.Pool }

var _ AmendStore = amendStore{}

func (s amendStore) AmendRequirement(ctx context.Context, id uuid.UUID, name string, body *string) (Requirement, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Requirement{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// Lock the current row for the duration of this transaction so a
	// concurrent amend of the same id cannot close it twice or race the
	// INSERT below against another amend's INSERT (both would otherwise be
	// eligible to win the `(id) WHERE valid_to IS NULL` partial unique
	// index, turning a legitimate race into a 500 instead of a serialized
	// pair of amendments).
	current, err := scanRequirement(tx.QueryRow(ctx, `
		SELECT `+requirementColumns+`
		FROM requirement
		WHERE id = $1 AND valid_to IS NULL
		FOR UPDATE
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Requirement{}, fmt.Errorf("%w: requirement id %s", ErrNotFound, id)
	}
	if err != nil {
		return Requirement{}, fmt.Errorf("get current requirement for amend: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE requirement SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL
	`, id); err != nil {
		return Requirement{}, fmt.Errorf("close current requirement: %w", err)
	}

	amended, err := scanRequirement(tx.QueryRow(ctx, `
		INSERT INTO requirement (id, scope_id, feature_id, kind, name, body, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+requirementColumns,
		current.ID, current.ScopeID, current.FeatureID, string(current.Kind), name, body, current.Position))
	if err != nil {
		return Requirement{}, fmt.Errorf("insert amended requirement: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Requirement{}, fmt.Errorf("commit: %w", err)
	}
	return amended, nil
}

func (s amendStore) AmendLoadBearingDecision(ctx context.Context, id uuid.UUID, name string, body *string) (LoadBearingDecision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	current, err := scanLoadBearingDecision(tx.QueryRow(ctx, `
		SELECT `+loadBearingDecisionColumns+`
		FROM load_bearing_decision
		WHERE id = $1 AND valid_to IS NULL
		FOR UPDATE
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return LoadBearingDecision{}, fmt.Errorf("%w: load_bearing_decision id %s", ErrNotFound, id)
	}
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("get current load_bearing_decision for amend: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE load_bearing_decision SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL
	`, id); err != nil {
		return LoadBearingDecision{}, fmt.Errorf("close current load_bearing_decision: %w", err)
	}

	amended, err := scanLoadBearingDecision(tx.QueryRow(ctx, `
		INSERT INTO load_bearing_decision (id, scope_id, feature_set_id, name, body, position)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+loadBearingDecisionColumns,
		current.ID, current.ScopeID, current.FeatureSetID, name, body, current.Position))
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("insert amended load_bearing_decision: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return LoadBearingDecision{}, fmt.Errorf("commit: %w", err)
	}
	return amended, nil
}
