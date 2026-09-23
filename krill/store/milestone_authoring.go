// This file (issue #2683, FR1/FR2, C13) is MilestoneAuthoringStore --
// the milestone authoring surface migration 010 adds on top of the bare
// `milestone_ref`/`entity_milestone` rows MilestoneStore (milestone.go)
// already covers: an outcome sentence, an FR budget, the Delivers/Must-
// not-foreclose delivery-axis lists (LB6, both `entity_milestone` rows
// discriminated by Relation), and a deliberately-deferred list
// (`milestone_deferral`). Kept as a sibling accessor
// ((*Store).MilestoneAuthoring()) rather than folded into MilestoneStore
// so the importer's pre-existing GetOrCreateRef/AddAssociation path
// (FR16) stays exactly as it is -- this file is additive.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MilestoneAuthoringStore covers the authoring fields on `milestone_ref`
// plus `milestone_deferral` (migration 010, issue #2683). Every method
// here follows the same LB2 parentage / LB4 subject-pair conventions as
// every other Create in this package -- see feature.go and pointer.go for
// the two precedents this file's Implementation phase will follow.
type MilestoneAuthoringStore interface {
	// CreateMilestone inserts a new `milestone_ref` row under productID
	// with kind='milestone', assigning position via nextSiblingPosition
	// (FR7) and recording the LB4 subject pair. frBudget may be nil (FR2
	// does not require a budget at creation time). Returns ErrNotFound if
	// productID has no current `product` row in scopeID (LB2 parentage).
	CreateMilestone(ctx context.Context, scopeID, productID uuid.UUID, name, outcome string, frBudget *int, acting, onBehalfOf Subject) (MilestoneRef, error)

	// SetOutcome revises milestoneID's outcome sentence (FR1's revise
	// path).
	SetOutcome(ctx context.Context, milestoneID uuid.UUID, outcome string, acting, onBehalfOf Subject) error

	// SetFRBudget revises milestoneID's FR budget (FR2's revise path) --
	// the current value after two calls is always the latest.
	SetFRBudget(ctx context.Context, milestoneID uuid.UUID, budget int, acting, onBehalfOf Subject) error

	// AddDelivers records that entityID (a Feature.ID or
	// LoadBearingDecision.ID) is delivered by milestoneID -- an
	// `entity_milestone` row with Relation=MilestoneRelationDelivers.
	// Idempotent: re-adding the same (entityID, milestoneID) pair is a
	// no-op, mirroring MilestoneStore.AddAssociation.
	AddDelivers(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, acting, onBehalfOf Subject) error

	// AddMustNotForeclose records that milestoneID must not foreclose
	// entityID (to date, always a LoadBearingDecision.ID) -- an
	// `entity_milestone` row with Relation=MilestoneRelationMustNotForeclose.
	// Idempotent, same as AddDelivers.
	AddMustNotForeclose(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, acting, onBehalfOf Subject) error

	// AddDeferral records one deliberately-deferred item under
	// milestoneID. destination must be non-empty (FR1: every deferred
	// entry cites where it went).
	AddDeferral(ctx context.Context, scopeID, milestoneID uuid.UUID, body, destination string, acting, onBehalfOf Subject) (MilestoneDeferral, error)

	// ListDeferrals returns every MilestoneDeferral row for milestoneID,
	// ordered by Position.
	ListDeferrals(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneDeferral, error)

	// GetMilestone returns id's MilestoneRef (authoring fields included)
	// plus its Delivers/Must-not-foreclose association lists and its
	// deferrals, or ErrNotFound.
	GetMilestone(ctx context.Context, id uuid.UUID) (MilestoneRef, []EntityMilestone, []EntityMilestone, []MilestoneDeferral, error)

	// CreateMilepebble inserts a new `milestone_ref` row under
	// parentMilestoneID with kind='milepebble' (migration 011, issue
	// #2684, FR3), assigning position via nextSiblingPositionPlain among
	// siblings sharing the same parent (not the same product -- two
	// milepebbles under different parents may share a position) and
	// recording the LB4 subject pair. Returns ErrNotFound if
	// parentMilestoneID has no `milestone_ref` row in scopeID, or if that
	// row is itself a milepebble (a milepebble's parent must be a
	// milestone, never another milepebble -- FR3's "exactly one
	// milestone").
	CreateMilepebble(ctx context.Context, scopeID, parentMilestoneID uuid.UUID, name, outcome string, acting, onBehalfOf Subject) (MilestoneRef, error)

	// AddMilepebbleDelivers records that entityID is delivered by
	// milepebbleID -- an `entity_milestone` row with
	// Relation=MilestoneRelationDelivers against the milepebble. Rejects
	// (with a named, loud error, never a silent skip) an entityID that is
	// not already a Delivers association of milepebbleID's parent
	// milestone -- FR3's "a milepebble's delivered scope is a subset of
	// that milestone's own delivered features/FRs".
	AddMilepebbleDelivers(ctx context.Context, scopeID, milepebbleID, entityID uuid.UUID, acting, onBehalfOf Subject) error

	// ListMilepebblesByMilestone returns every milepebble cut from
	// milestoneID, in Position order (FR7).
	ListMilepebblesByMilestone(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneRef, error)

	// AddDiscoveredScope is FR4's mid-milestone path: creates a real
	// Feature or Requirement row (via FeatureStore.Create/
	// RequirementStore.Create, respecting that entity's own real parent in
	// the spec chain -- input's FeatureSetID or FeatureID selects which),
	// then associates the new row to milepebbleID and, in the same
	// transaction, to milepebbleID's parent milestone's Delivers set --
	// so the FR3 subset invariant AddMilepebbleDelivers enforces still
	// holds immediately afterward. The parent milestone's own authoring
	// rows (outcome, fr_budget, deferrals) are never touched by this
	// method.
	AddDiscoveredScope(ctx context.Context, scopeID, milepebbleID uuid.UUID, input DiscoveredScopeInput, acting, onBehalfOf Subject) (DiscoveredScopeResult, error)
}

// DiscoveredScopeEntityKind discriminates which spec entity table
// AddDiscoveredScope wrote to (FR4) -- DiscoveredScopeResult's own
// discriminator, mirroring EntityDeltaChange's shape in spirit but for a
// different table pair.
type DiscoveredScopeEntityKind string

const (
	DiscoveredScopeEntityKindFeature     DiscoveredScopeEntityKind = "feature"
	DiscoveredScopeEntityKindRequirement DiscoveredScopeEntityKind = "requirement"
)

// DiscoveredScopeInput is AddDiscoveredScope's input (FR4). Exactly one of
// FeatureSetID (creating a Feature) or FeatureID (creating a Requirement)
// must be set -- mirroring FeatureStore.Create's and
// RequirementStore.Create's own parent shapes; AddDiscoveredScope invents
// no new entity kind of its own. RequirementKind/Body apply only when
// FeatureID is set; Description applies only when FeatureSetID is set.
type DiscoveredScopeInput struct {
	FeatureSetID    *uuid.UUID
	FeatureID       *uuid.UUID
	RequirementKind RequirementKind
	Name            string
	Description     *string
	Body            *string
}

// DiscoveredScopeResult is AddDiscoveredScope's result -- the newly
// created entity's immutable id and which table it landed in.
type DiscoveredScopeResult struct {
	EntityID uuid.UUID
	Kind     DiscoveredScopeEntityKind
}

// milestoneAuthoringStore is the pgx-backed MilestoneAuthoringStore
// implementation.
type milestoneAuthoringStore struct{ pool *pgxpool.Pool }

var _ MilestoneAuthoringStore = milestoneAuthoringStore{}

const milestoneDeferralColumns = `id, scope_id, milestone_id, body, destination, position, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanMilestoneDeferral(row pgx.Row) (MilestoneDeferral, error) {
	var d MilestoneDeferral
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&d.ID, &d.ScopeID, &d.MilestoneID, &d.Body, &d.Destination, &d.Position,
		&d.CreatedByActing.Iss, &d.CreatedByActing.Sub, &actingKind,
		&d.CreatedByOnBehalfOf.Iss, &d.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&d.CreatedAt,
	)
	if err != nil {
		return MilestoneDeferral{}, err
	}
	d.CreatedByActing.Kind = SubjectKind(actingKind)
	d.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return d, nil
}

func (s milestoneAuthoringStore) CreateMilestone(ctx context.Context, scopeID, productID uuid.UUID, name, outcome string, frBudget *int, acting, onBehalfOf Subject) (MilestoneRef, error) {
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

	position, err := nextSiblingPositionPlain(ctx, tx, "milestone_ref", "product_id", productID, scopeID)
	if err != nil {
		return MilestoneRef{}, err
	}

	ref, err := scanMilestoneRef(tx.QueryRow(ctx, `
		INSERT INTO milestone_ref (
			scope_id, product_id, name, kind, outcome, fr_budget, position,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+milestoneRefColumns,
		scopeID, productID, name, string(MilestoneKindMilestone), outcome, frBudget, position,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)))
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("insert milestone_ref: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return MilestoneRef{}, fmt.Errorf("commit: %w", err)
	}
	return ref, nil
}

func (s milestoneAuthoringStore) SetOutcome(ctx context.Context, milestoneID uuid.UUID, outcome string, acting, onBehalfOf Subject) error {
	// milestone_ref's LB4 subject-pair columns record only the original
	// Create (see models.go's MilestoneRef doc comment and this
	// migration's ARCHITECTURE.md note) -- there is no per-revise audit
	// column to write acting/onBehalfOf into here. Both are still accepted
	// (mirroring every other revise-shaped method in this package) so a
	// caller cannot construct a revise with no attributable subject, even
	// though this table does not yet persist it per-revision.
	tag, err := s.pool.Exec(ctx, `UPDATE milestone_ref SET outcome = $1 WHERE id = $2`, outcome, milestoneID)
	if err != nil {
		return fmt.Errorf("update milestone_ref outcome: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: milestone id %s", ErrNotFound, milestoneID)
	}
	return nil
}

func (s milestoneAuthoringStore) SetFRBudget(ctx context.Context, milestoneID uuid.UUID, budget int, acting, onBehalfOf Subject) error {
	tag, err := s.pool.Exec(ctx, `UPDATE milestone_ref SET fr_budget = $1 WHERE id = $2`, budget, milestoneID)
	if err != nil {
		return fmt.Errorf("update milestone_ref fr_budget: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: milestone id %s", ErrNotFound, milestoneID)
	}
	return nil
}

// addRelation is the shared body of AddDelivers/AddMustNotForeclose --
// both write an `entity_milestone` row for (entityID, milestoneID),
// differing only in relation (LB6's discriminator). acting/onBehalfOf are
// accepted for signature symmetry with every other write in this file --
// entity_milestone (migration 004) carries no subject-pair columns of its
// own, same as the pre-existing importer-facing AddAssociation
// (milestone.go).
func (s milestoneAuthoringStore) addRelation(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, relation MilestoneRelation, acting, onBehalfOf Subject) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	exists, err := plainRowExists(ctx, tx, "milestone_ref", milestoneID, scopeID)
	if err != nil {
		return err
	}
	if !exists {
		return errParentNotFound("milestone_ref", milestoneID)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (entity_id, milestone_id, relation) DO NOTHING
	`, scopeID, entityID, milestoneID, string(relation)); err != nil {
		return fmt.Errorf("insert entity_milestone: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s milestoneAuthoringStore) AddDelivers(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, acting, onBehalfOf Subject) error {
	return s.addRelation(ctx, scopeID, milestoneID, entityID, MilestoneRelationDelivers, acting, onBehalfOf)
}

func (s milestoneAuthoringStore) AddMustNotForeclose(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, acting, onBehalfOf Subject) error {
	return s.addRelation(ctx, scopeID, milestoneID, entityID, MilestoneRelationMustNotForeclose, acting, onBehalfOf)
}

func (s milestoneAuthoringStore) AddDeferral(ctx context.Context, scopeID, milestoneID uuid.UUID, body, destination string, acting, onBehalfOf Subject) (MilestoneDeferral, error) {
	if destination == "" {
		return MilestoneDeferral{}, fmt.Errorf("destination: required -- every deferred entry must cite where it went (FR1)")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MilestoneDeferral{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	exists, err := plainRowExists(ctx, tx, "milestone_ref", milestoneID, scopeID)
	if err != nil {
		return MilestoneDeferral{}, err
	}
	if !exists {
		return MilestoneDeferral{}, errParentNotFound("milestone_ref", milestoneID)
	}

	position, err := nextSiblingPositionPlain(ctx, tx, "milestone_deferral", "milestone_id", milestoneID, scopeID)
	if err != nil {
		return MilestoneDeferral{}, err
	}

	deferral, err := scanMilestoneDeferral(tx.QueryRow(ctx, `
		INSERT INTO milestone_deferral (
			scope_id, milestone_id, body, destination, position,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+milestoneDeferralColumns,
		scopeID, milestoneID, body, destination, position,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)))
	if err != nil {
		return MilestoneDeferral{}, fmt.Errorf("insert milestone_deferral: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return MilestoneDeferral{}, fmt.Errorf("commit: %w", err)
	}
	return deferral, nil
}

func (s milestoneAuthoringStore) ListDeferrals(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneDeferral, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+milestoneDeferralColumns+`
		FROM milestone_deferral
		WHERE milestone_id = $1
		ORDER BY position
	`, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("list milestone_deferral: %w", err)
	}
	defer rows.Close()

	var deferrals []MilestoneDeferral
	for rows.Next() {
		d, err := scanMilestoneDeferral(rows)
		if err != nil {
			return nil, fmt.Errorf("scan milestone_deferral: %w", err)
		}
		deferrals = append(deferrals, d)
	}
	return deferrals, rows.Err()
}

func (s milestoneAuthoringStore) GetMilestone(ctx context.Context, id uuid.UUID) (MilestoneRef, []EntityMilestone, []EntityMilestone, []MilestoneDeferral, error) {
	ref, err := scanMilestoneRef(s.pool.QueryRow(ctx, `
		SELECT `+milestoneRefColumns+`
		FROM milestone_ref
		WHERE id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return MilestoneRef{}, nil, nil, nil, fmt.Errorf("%w: milestone id %s", ErrNotFound, id)
	}
	if err != nil {
		return MilestoneRef{}, nil, nil, nil, fmt.Errorf("get milestone_ref: %w", err)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, scope_id, entity_id, milestone_id, relation, created_at
		FROM entity_milestone
		WHERE milestone_id = $1
		ORDER BY created_at
	`, id)
	if err != nil {
		return MilestoneRef{}, nil, nil, nil, fmt.Errorf("list entity_milestone: %w", err)
	}
	defer rows.Close()

	var delivers, mustNotForeclose []EntityMilestone
	for rows.Next() {
		var m EntityMilestone
		var relation string
		if err := rows.Scan(&m.ID, &m.ScopeID, &m.EntityID, &m.MilestoneID, &relation, &m.CreatedAt); err != nil {
			return MilestoneRef{}, nil, nil, nil, fmt.Errorf("scan entity_milestone: %w", err)
		}
		m.Relation = MilestoneRelation(relation)
		switch m.Relation {
		case MilestoneRelationMustNotForeclose:
			mustNotForeclose = append(mustNotForeclose, m)
		default:
			delivers = append(delivers, m)
		}
	}
	if err := rows.Err(); err != nil {
		return MilestoneRef{}, nil, nil, nil, fmt.Errorf("list entity_milestone: %w", err)
	}

	deferrals, err := s.ListDeferrals(ctx, id)
	if err != nil {
		return MilestoneRef{}, nil, nil, nil, err
	}

	return ref, delivers, mustNotForeclose, deferrals, nil
}

// ErrMilepebbleDeliversNotSubset is AddMilepebbleDelivers' named, loud
// rejection (FR3: "a milepebble's delivered scope is a subset of that
// milestone's own delivered features/FRs") -- returned instead of a
// silent skip whenever the caller names an entity that is not already a
// Relation=MilestoneRelationDelivers association of the milepebble's
// parent milestone.
var ErrMilepebbleDeliversNotSubset = errors.New("krill/store: entity is not in the parent milestone's delivers set")

// errNotAMilepebble/errNotAMilestone report a milestone_ref row of the
// wrong Kind for the operation attempted -- CreateMilepebble's parent must
// be MilestoneKindMilestone (never another milepebble, FR3's "exactly one
// milestone"), while AddMilepebbleDelivers/AddDiscoveredScope's target
// must be MilestoneKindMilepebble.
func errNotAMilepebble(id uuid.UUID) error {
	return fmt.Errorf("%w: milestone_ref id %s is not a milepebble", ErrNotFound, id)
}

func errNotAMilestone(id uuid.UUID) error {
	return fmt.Errorf("%w: milestone_ref id %s is not a milestone (a milepebble's parent must be a milestone, never another milepebble)", ErrNotFound, id)
}

func (s milestoneAuthoringStore) CreateMilepebble(ctx context.Context, scopeID, parentMilestoneID uuid.UUID, name, outcome string, acting, onBehalfOf Subject) (MilestoneRef, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var parentKind string
	var productID uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT kind, product_id FROM milestone_ref WHERE id = $1 AND scope_id = $2
	`, parentMilestoneID, scopeID).Scan(&parentKind, &productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MilestoneRef{}, errParentNotFound("milestone_ref", parentMilestoneID)
	}
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("get parent milestone_ref: %w", err)
	}
	if MilestoneKind(parentKind) != MilestoneKindMilestone {
		return MilestoneRef{}, errNotAMilestone(parentMilestoneID)
	}

	// Siblings are scoped by parent_milestone_id, not product_id -- two
	// milepebbles cut from different parent milestones under the same
	// product must not compete for the same position (see this method's
	// doc comment on MilestoneAuthoringStore).
	position, err := nextSiblingPositionPlain(ctx, tx, "milestone_ref", "parent_milestone_id", parentMilestoneID, scopeID)
	if err != nil {
		return MilestoneRef{}, err
	}

	ref, err := scanMilestoneRef(tx.QueryRow(ctx, `
		INSERT INTO milestone_ref (
			scope_id, product_id, name, kind, outcome, position, parent_milestone_id,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING `+milestoneRefColumns,
		scopeID, productID, name, string(MilestoneKindMilepebble), outcome, position, parentMilestoneID,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)))
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("insert milestone_ref: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return MilestoneRef{}, fmt.Errorf("commit: %w", err)
	}
	return ref, nil
}

// getMilepebbleParent looks up milepebbleID's Kind and ParentMilestoneID
// inside q, returning errNotAMilepebble if the row is missing or is not
// itself a milepebble -- the shared parentage check AddMilepebbleDelivers
// and AddDiscoveredScope both need before touching entity_milestone.
func getMilepebbleParent(ctx context.Context, q txQuerier, scopeID, milepebbleID uuid.UUID) (uuid.UUID, error) {
	var kind string
	var parentMilestoneID uuid.NullUUID
	err := q.QueryRow(ctx, `
		SELECT kind, parent_milestone_id FROM milestone_ref WHERE id = $1 AND scope_id = $2
	`, milepebbleID, scopeID).Scan(&kind, &parentMilestoneID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.UUID{}, errParentNotFound("milestone_ref", milepebbleID)
	}
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("get milepebble milestone_ref: %w", err)
	}
	if MilestoneKind(kind) != MilestoneKindMilepebble || !parentMilestoneID.Valid {
		return uuid.UUID{}, errNotAMilepebble(milepebbleID)
	}
	return parentMilestoneID.UUID, nil
}

func (s milestoneAuthoringStore) AddMilepebbleDelivers(ctx context.Context, scopeID, milepebbleID, entityID uuid.UUID, acting, onBehalfOf Subject) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	parentMilestoneID, err := getMilepebbleParent(ctx, tx, scopeID, milepebbleID)
	if err != nil {
		return err
	}

	var inParentDelivers bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM entity_milestone
			WHERE entity_id = $1 AND milestone_id = $2 AND relation = $3
		)
	`, entityID, parentMilestoneID, string(MilestoneRelationDelivers)).Scan(&inParentDelivers); err != nil {
		return fmt.Errorf("check parent milestone delivers: %w", err)
	}
	if !inParentDelivers {
		return fmt.Errorf("%w: entity %s, milepebble %s, parent milestone %s", ErrMilepebbleDeliversNotSubset, entityID, milepebbleID, parentMilestoneID)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (entity_id, milestone_id, relation) DO NOTHING
	`, scopeID, entityID, milepebbleID, string(MilestoneRelationDelivers)); err != nil {
		return fmt.Errorf("insert entity_milestone: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s milestoneAuthoringStore) ListMilepebblesByMilestone(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneRef, error) {
	return listMilepebblesByMilestone(ctx, s.pool, milestoneID)
}

// milestoneRefQueryer is the one method listMilepebblesByMilestone needs
// -- satisfied by both *pgxpool.Pool (ListMilepebblesByMilestone's own
// pool-backed read) and pgx.Tx (Abandon's cascade step, abandon.go, issue
// #2688, which lists a milestone's live milepebbles inside its own
// transaction rather than through a second, standalone read).
type milestoneRefQueryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func listMilepebblesByMilestone(ctx context.Context, q milestoneRefQueryer, milestoneID uuid.UUID) ([]MilestoneRef, error) {
	rows, err := q.Query(ctx, `
		SELECT `+milestoneRefColumns+`
		FROM milestone_ref
		WHERE parent_milestone_id = $1
		ORDER BY position
	`, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("list milepebbles by milestone: %w", err)
	}
	defer rows.Close()

	var refs []MilestoneRef
	for rows.Next() {
		ref, err := scanMilestoneRef(rows)
		if err != nil {
			return nil, fmt.Errorf("scan milestone_ref: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// addDiscoveredScopeAssociation writes one entity_milestone Delivers row
// inside tx -- AddDiscoveredScope calls this twice (milepebble, then
// parent milestone) so the FR3 subset invariant AddMilepebbleDelivers
// enforces holds again the instant the transaction commits.
func addDiscoveredScopeAssociation(ctx context.Context, tx pgx.Tx, scopeID, entityID, milestoneID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (entity_id, milestone_id, relation) DO NOTHING
	`, scopeID, entityID, milestoneID, string(MilestoneRelationDelivers))
	if err != nil {
		return fmt.Errorf("insert entity_milestone: %w", err)
	}
	return nil
}

// acting/onBehalfOf are accepted for NFR4/LB4 signature symmetry with
// every other write in this package, but -- like addRelation above --
// are not persisted: neither `feature`/`requirement` (migration 002) nor
// `entity_milestone` (migration 004) carries a subject-pair column, and
// FR3/LB6 forbid adding one (a milepebble is never a new entity kind or a
// new association mechanism).
func (s milestoneAuthoringStore) AddDiscoveredScope(ctx context.Context, scopeID, milepebbleID uuid.UUID, input DiscoveredScopeInput, acting, onBehalfOf Subject) (DiscoveredScopeResult, error) {
	if (input.FeatureSetID == nil) == (input.FeatureID == nil) {
		return DiscoveredScopeResult{}, fmt.Errorf("discovered scope: exactly one of feature_set_id or feature_id must be set")
	}
	if input.Name == "" {
		return DiscoveredScopeResult{}, fmt.Errorf("name: required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DiscoveredScopeResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	parentMilestoneID, err := getMilepebbleParent(ctx, tx, scopeID, milepebbleID)
	if err != nil {
		return DiscoveredScopeResult{}, err
	}

	var result DiscoveredScopeResult
	if input.FeatureSetID != nil {
		feature, err := createFeatureTx(ctx, tx, scopeID, *input.FeatureSetID, input.Name, input.Description, nil)
		if err != nil {
			return DiscoveredScopeResult{}, err
		}
		result = DiscoveredScopeResult{EntityID: feature.ID, Kind: DiscoveredScopeEntityKindFeature}
	} else {
		if input.RequirementKind != RequirementKindFR && input.RequirementKind != RequirementKindNFR {
			return DiscoveredScopeResult{}, fmt.Errorf("requirement_kind: must be FR or NFR")
		}
		requirement, err := createRequirementTx(ctx, tx, scopeID, *input.FeatureID, input.RequirementKind, input.Name, input.Body)
		if err != nil {
			return DiscoveredScopeResult{}, err
		}
		result = DiscoveredScopeResult{EntityID: requirement.ID, Kind: DiscoveredScopeEntityKindRequirement}
	}

	// Both associations land in the same transaction as the entity Create
	// above -- the parent milestone's own authoring rows (outcome,
	// fr_budget, milestone_deferral) are never touched by any statement
	// here, so this method cannot regress them even by accident.
	if err := addDiscoveredScopeAssociation(ctx, tx, scopeID, result.EntityID, milepebbleID); err != nil {
		return DiscoveredScopeResult{}, err
	}
	if err := addDiscoveredScopeAssociation(ctx, tx, scopeID, result.EntityID, parentMilestoneID); err != nil {
		return DiscoveredScopeResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return DiscoveredScopeResult{}, fmt.Errorf("commit: %w", err)
	}
	return result, nil
}
