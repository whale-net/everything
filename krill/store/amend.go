// This file is the SCD2 close-and-open write path AGENTS.md's "SCD2"
// section describes, applied to every spec-axis entity kind: Product,
// FeatureSet, Feature, Requirement, Persona, NonGoal, LoadBearingDecision,
// and Milestone. Migration 002 already shipped every column the seven
// spec-axis tables need; migration 020 gave `milestone_ref` the same shape
// so a milestone's authoring fields are amendable too (FR 39373553).
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AmendStore is the write side of SCD2 supersession: closing an entity's
// current row and opening a new one under the SAME surrogate id (LB2), for
// every spec-axis kind. No method here ever mints a new id, ever
// reparents, ever re-kinds, ever rewrites `position` or `display_number`,
// and never touches a sibling's row -- an amendment supersedes exactly one
// logical entity's own content.
//
// A Milestone's amend is the one that has to say what it does not touch:
// kind, product, parent milestone, position, and FR budget are carried
// forward unchanged, and the delivery axis (status transitions, Delivers,
// must-not-foreclose, deferrals) lives in separate append-only tables
// keyed on the same immutable id, so a supersession never reaches them
// (FR 39373553).
type AmendStore interface {
	// AmendProduct closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, and position, with name and
	// vision as given. Returns ErrNotFound if id has no current row.
	AmendProduct(ctx context.Context, id uuid.UUID, name, vision string) (Product, error)

	// AmendFeatureSet closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, product_id, and position,
	// with name and description as given.
	AmendFeatureSet(ctx context.Context, id uuid.UUID, name string, description *string) (FeatureSet, error)

	// AmendFeature closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, feature_set_id, position,
	// and display_number (the `Cn` a caller already cites, migration 017),
	// with name and description as given.
	AmendFeature(ctx context.Context, id uuid.UUID, name string, description *string) (Feature, error)

	// AmendRequirement closes the current row for id (`valid_to = NOW()`)
	// and inserts a new current row carrying the closed row's id, scope_id,
	// feature_id, kind, and position unchanged, with name and body as
	// given. Returns ErrNotFound if id has no current row.
	AmendRequirement(ctx context.Context, id uuid.UUID, name string, body *string) (Requirement, error)

	// AmendPersona closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, product_id, and position,
	// with name and description as given.
	AmendPersona(ctx context.Context, id uuid.UUID, name string, description *string) (Persona, error)

	// AmendNonGoal closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, product_id, kind, and
	// position, with name and body as given. Kind is carried forward, never
	// re-chosen: a `deferred` Non-Goal becomes `permanent` (or is retired)
	// through the resolution verb, not through an amend.
	AmendNonGoal(ctx context.Context, id uuid.UUID, name string, body *string) (NonGoal, error)

	// AmendLoadBearingDecision closes the current row for id and inserts a
	// new current row carrying the closed row's id, scope_id,
	// feature_set_id, position, and display_number, with name and body as
	// given. Returns ErrNotFound if id has no current row.
	AmendLoadBearingDecision(ctx context.Context, id uuid.UUID, name string, body *string) (LoadBearingDecision, error)

	// AmendMilestone closes the milestone's current row and inserts a
	// successor carrying the closed row's id, scope_id, product_id, kind,
	// parent_milestone_id, position, FR budget, created_at, and LB4 subject
	// pair unchanged, with name and outcome as given. Everything on the
	// delivery axis is left exactly as it was.
	AmendMilestone(ctx context.Context, id uuid.UUID, name string, outcome *string) (MilestoneRef, error)
}

type amendStore struct{ pool *pgxpool.Pool }

var _ AmendStore = amendStore{}

// supersede is the shared body of every Amend* method: it row-locks the
// current row for id, closes it (valid_to = NOW()), and runs open -- which
// inserts the successor revision under the same immutable id -- all inside
// one transaction.
//
// The FOR UPDATE is what makes a concurrent amend of the same id
// serialize rather than race: both would otherwise be eligible to win the
// `(id) WHERE valid_to IS NULL` partial unique index, turning a legitimate
// race into a 500 instead of a pair of amendments applied in order.
//
// table and columns are always this package's own constant names, never
// caller input, so building the statements with fmt.Sprintf carries no
// injection risk (matches currentRowExists' same note in errors.go).
func supersede[T any](
	ctx context.Context,
	pool *pgxpool.Pool,
	table, columns string,
	scan func(pgx.Row) (T, error),
	id uuid.UUID,
	open func(ctx context.Context, q txQuerier, current T) (T, error),
) (T, error) {
	var zero T

	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	current, err := scan(tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE id = $1 AND valid_to IS NULL
		FOR UPDATE
	`, columns, table), id))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("%w: %s id %s", ErrNotFound, table, id)
	}
	if err != nil {
		return zero, fmt.Errorf("get current %s for amend: %w", table, err)
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL`, table), id); err != nil {
		return zero, fmt.Errorf("close current %s: %w", table, err)
	}

	amended, err := open(ctx, tx, current)
	if err != nil {
		return zero, err
	}

	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit: %w", err)
	}
	return amended, nil
}

func (s amendStore) AmendProduct(ctx context.Context, id uuid.UUID, name, vision string) (Product, error) {
	return supersede(ctx, s.pool, "product", productColumns, scanProduct, id,
		func(ctx context.Context, q txQuerier, current Product) (Product, error) {
			amended, err := scanProduct(q.QueryRow(ctx, `
				INSERT INTO product (id, scope_id, name, vision, position)
				VALUES ($1, $2, $3, $4, $5)
				RETURNING `+productColumns,
				current.ID, current.ScopeID, name, vision, current.Position))
			return amended, errNameConflict("product", "insert amended product", err)
		})
}

func (s amendStore) AmendFeatureSet(ctx context.Context, id uuid.UUID, name string, description *string) (FeatureSet, error) {
	return supersede(ctx, s.pool, "feature_set", featureSetColumns, scanFeatureSet, id,
		func(ctx context.Context, q txQuerier, current FeatureSet) (FeatureSet, error) {
			amended, err := scanFeatureSet(q.QueryRow(ctx, `
				INSERT INTO feature_set (id, scope_id, product_id, name, description, position)
				VALUES ($1, $2, $3, $4, $5, $6)
				RETURNING `+featureSetColumns,
				current.ID, current.ScopeID, current.ProductID, name, description, current.Position))
			return amended, errNameConflict("feature_set", "insert amended feature_set", err)
		})
}

func (s amendStore) AmendFeature(ctx context.Context, id uuid.UUID, name string, description *string) (Feature, error) {
	return supersede(ctx, s.pool, "feature", featureColumns, scanFeature, id,
		func(ctx context.Context, q txQuerier, current Feature) (Feature, error) {
			amended, err := scanFeature(q.QueryRow(ctx, `
				INSERT INTO feature (id, scope_id, feature_set_id, name, description, position, display_number)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+featureColumns,
				current.ID, current.ScopeID, current.FeatureSetID, name, description, current.Position, current.DisplayNumber))
			return amended, errNameConflict("feature", "insert amended feature", err)
		})
}

func (s amendStore) AmendRequirement(ctx context.Context, id uuid.UUID, name string, body *string) (Requirement, error) {
	return supersede(ctx, s.pool, "requirement", requirementColumns, scanRequirement, id,
		func(ctx context.Context, q txQuerier, current Requirement) (Requirement, error) {
			amended, err := scanRequirement(q.QueryRow(ctx, `
				INSERT INTO requirement (id, scope_id, feature_id, kind, name, body, position)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+requirementColumns,
				current.ID, current.ScopeID, current.FeatureID, string(current.Kind), name, body, current.Position))
			return amended, errNameConflict("requirement", "insert amended requirement", err)
		})
}

func (s amendStore) AmendPersona(ctx context.Context, id uuid.UUID, name string, description *string) (Persona, error) {
	return supersede(ctx, s.pool, "persona", personaColumns, scanPersona, id,
		func(ctx context.Context, q txQuerier, current Persona) (Persona, error) {
			amended, err := scanPersona(q.QueryRow(ctx, `
				INSERT INTO persona (id, scope_id, product_id, name, description, position)
				VALUES ($1, $2, $3, $4, $5, $6)
				RETURNING `+personaColumns,
				current.ID, current.ScopeID, current.ProductID, name, description, current.Position))
			return amended, errNameConflict("persona", "insert amended persona", err)
		})
}

func (s amendStore) AmendNonGoal(ctx context.Context, id uuid.UUID, name string, body *string) (NonGoal, error) {
	return supersede(ctx, s.pool, "non_goal", nonGoalColumns, scanNonGoal, id,
		func(ctx context.Context, q txQuerier, current NonGoal) (NonGoal, error) {
			amended, err := scanNonGoal(q.QueryRow(ctx, `
				INSERT INTO non_goal (id, scope_id, product_id, kind, name, body, position)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+nonGoalColumns,
				current.ID, current.ScopeID, current.ProductID, string(current.Kind), name, body, current.Position))
			return amended, errNameConflict("non_goal", "insert amended non_goal", err)
		})
}

func (s amendStore) AmendLoadBearingDecision(ctx context.Context, id uuid.UUID, name string, body *string) (LoadBearingDecision, error) {
	return supersede(ctx, s.pool, "load_bearing_decision", loadBearingDecisionColumns, scanLoadBearingDecision, id,
		func(ctx context.Context, q txQuerier, current LoadBearingDecision) (LoadBearingDecision, error) {
			amended, err := scanLoadBearingDecision(q.QueryRow(ctx, `
				INSERT INTO load_bearing_decision (id, scope_id, feature_set_id, name, body, position, display_number)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+loadBearingDecisionColumns,
				current.ID, current.ScopeID, current.FeatureSetID, name, body, current.Position, current.DisplayNumber))
			return amended, errNameConflict("load_bearing_decision", "insert amended load_bearing_decision", err)
		})
}

func (s amendStore) AmendMilestone(ctx context.Context, id uuid.UUID, name string, outcome *string) (MilestoneRef, error) {
	return supersede(ctx, s.pool, "milestone_ref", milestoneRefColumns, scanMilestoneRef, id,
		func(ctx context.Context, q txQuerier, current MilestoneRef) (MilestoneRef, error) {
			args := []any{
				current.ID, current.ScopeID, current.ProductID, name, string(current.Kind), outcome,
				current.FRBudget, current.Position, current.ParentMilestoneID,
			}
			args = append(args, subjectArgs(current.CreatedByActing)...)
			args = append(args, subjectArgs(current.CreatedByOnBehalfOf)...)
			args = append(args, current.CreatedAt)

			amended, err := scanMilestoneRef(q.QueryRow(ctx, `
				INSERT INTO milestone_ref (
					id, scope_id, product_id, name, kind, outcome, fr_budget, position, parent_milestone_id,
					created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
					created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind,
					created_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
				RETURNING `+milestoneRefColumns, args...))
			return amended, errNameConflict("milestone_ref", "insert amended milestone_ref", err)
		})
}

// subjectArgs renders a milestone_ref row's nullable LB4 subject pair into
// the three query arguments its columns take. A row written by the
// importer records no subject at all (migration 010's LB4 note), so a nil
// Subject becomes three NULLs rather than a zero-value triple.
func subjectArgs(s *Subject) []any {
	if s == nil {
		return []any{nil, nil, nil}
	}
	return []any{s.Iss, s.Sub, string(s.Kind)}
}

// AmendPlacementChange is the reparent/re-kind guard an amend request body
// may carry (FR f0f6bc18). Amend replaces an entity's content under its
// unchanged id, parent, and kind; none of these fields is ever applied.
// They exist so that a caller attempting a move or a re-kind gets the named
// refusal Refuse reports, naming the operation to use instead, rather than
// a generic unknown-field decode error.
type AmendPlacementChange struct {
	ProductID         *string `json:"product_id,omitempty"`
	FeatureSetID      *string `json:"feature_set_id,omitempty"`
	FeatureID         *string `json:"feature_id,omitempty"`
	ParentMilestoneID *string `json:"parent_milestone_id,omitempty"`
	Kind              *string `json:"kind,omitempty"`
}

// ErrPlacementChange is the named refusal an amend returns when a caller
// asks to reparent or re-kind. Moving an entity is a create/move
// operation and re-kinding is a resolution operation; amend never is one.
var ErrPlacementChange = errors.New("krill/store: amend cannot reparent or re-kind an entity")

// Refuse reports ErrPlacementChange naming entityKind and whichever field
// the caller tried to change, or nil when the caller changed none of them.
func (p AmendPlacementChange) Refuse(entityKind string) error {
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"product_id", p.ProductID},
		{"feature_set_id", p.FeatureSetID},
		{"feature_id", p.FeatureID},
		{"parent_milestone_id", p.ParentMilestoneID},
		{"kind", p.Kind},
	} {
		if field.value != nil {
			return fmt.Errorf("%w: %s cannot change %s on amend -- reparent through the create/move path and re-kind through the resolution path",
				ErrPlacementChange, entityKind, field.name)
		}
	}
	return nil
}
