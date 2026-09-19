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
//
// Method bodies are scaffold stubs; the Implementation phase of issue
// #2683 fills them in against this same interface.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
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
}

// milestoneAuthoringStore is the pgx-backed MilestoneAuthoringStore
// implementation.
type milestoneAuthoringStore struct{ pool *pgxpool.Pool }

var _ MilestoneAuthoringStore = milestoneAuthoringStore{}

func (s milestoneAuthoringStore) CreateMilestone(ctx context.Context, scopeID, productID uuid.UUID, name, outcome string, frBudget *int, acting, onBehalfOf Subject) (MilestoneRef, error) {
	return MilestoneRef{}, fmt.Errorf("store: MilestoneAuthoringStore.CreateMilestone not implemented -- see issue #2683's Implementation phase")
}

func (s milestoneAuthoringStore) SetOutcome(ctx context.Context, milestoneID uuid.UUID, outcome string, acting, onBehalfOf Subject) error {
	return fmt.Errorf("store: MilestoneAuthoringStore.SetOutcome not implemented -- see issue #2683's Implementation phase")
}

func (s milestoneAuthoringStore) SetFRBudget(ctx context.Context, milestoneID uuid.UUID, budget int, acting, onBehalfOf Subject) error {
	return fmt.Errorf("store: MilestoneAuthoringStore.SetFRBudget not implemented -- see issue #2683's Implementation phase")
}

func (s milestoneAuthoringStore) AddDelivers(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, acting, onBehalfOf Subject) error {
	return fmt.Errorf("store: MilestoneAuthoringStore.AddDelivers not implemented -- see issue #2683's Implementation phase")
}

func (s milestoneAuthoringStore) AddMustNotForeclose(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, acting, onBehalfOf Subject) error {
	return fmt.Errorf("store: MilestoneAuthoringStore.AddMustNotForeclose not implemented -- see issue #2683's Implementation phase")
}

func (s milestoneAuthoringStore) AddDeferral(ctx context.Context, scopeID, milestoneID uuid.UUID, body, destination string, acting, onBehalfOf Subject) (MilestoneDeferral, error) {
	return MilestoneDeferral{}, fmt.Errorf("store: MilestoneAuthoringStore.AddDeferral not implemented -- see issue #2683's Implementation phase")
}

func (s milestoneAuthoringStore) ListDeferrals(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneDeferral, error) {
	return nil, fmt.Errorf("store: MilestoneAuthoringStore.ListDeferrals not implemented -- see issue #2683's Implementation phase")
}

func (s milestoneAuthoringStore) GetMilestone(ctx context.Context, id uuid.UUID) (MilestoneRef, []EntityMilestone, []EntityMilestone, []MilestoneDeferral, error) {
	return MilestoneRef{}, nil, nil, nil, fmt.Errorf("store: MilestoneAuthoringStore.GetMilestone not implemented -- see issue #2683's Implementation phase")
}
