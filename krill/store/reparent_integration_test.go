//go:build integration

// Store-level coverage for ReparentStore (reparent.go): the one spec-axis
// verb that changes WHERE a Feature sits rather than what it says. The
// move is a close-and-open under the same immutable id, so what these
// tests pin is that nothing else moves with the row that should not have
// (the `Cn`, the position, the delivery axis) and that every way the
// move can be wrong is a named refusal writing nothing, not a half-
// applied revision.
//
// Shares milestone_authoring_integration_test.go's test-store/test-scope/
// test-subject helpers and recut_integration_test.go's
// deliversAssociation helper (same package, same build tag) rather than
// duplicating them.
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// reparentFixture is a product whose capability map is split across two
// FeatureSets, with a Feature and its Requirements under the first and a
// LoadBearingDecision on each FeatureSet -- the shape a reparent has to
// leave alone, since the decision is the FeatureSet's and the
// Requirements are the Feature's.
type reparentFixture struct {
	from, to                 store.FeatureSet
	feature                  store.Feature
	requirement              store.Requirement
	fromDecision, toDecision store.LoadBearingDecision
}

func newReparentFixture(t *testing.T, ctx context.Context, s *store.Store, scopeID, productID uuid.UUID) reparentFixture {
	t.Helper()

	from, err := s.FeatureSets().Create(ctx, scopeID, productID, "Now", nil)
	require.NoError(t, err)
	to, err := s.FeatureSets().Create(ctx, scopeID, productID, "Later", nil)
	require.NoError(t, err)

	description := "find things"
	feature, err := s.Features().Create(ctx, scopeID, from.ID, "Search", &description)
	require.NoError(t, err)
	requirement, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "FR-search", nil)
	require.NoError(t, err)
	fromDecision, err := s.Decisions().Create(ctx, scopeID, from.ID, "LB-from", nil)
	require.NoError(t, err)
	toDecision, err := s.Decisions().Create(ctx, scopeID, to.ID, "LB-to", nil)
	require.NoError(t, err)

	return reparentFixture{
		from: from, to: to, feature: feature, requirement: requirement,
		fromDecision: fromDecision, toDecision: toDecision,
	}
}

// featureRevisionCount is the number of `feature` rows carrying id,
// current or closed. A reparent must add exactly one; every refusal must
// add none.
func featureRevisionCount(t *testing.T, ctx context.Context, db *dbtest.Postgres, id uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM feature WHERE id = $1`, id).Scan(&n))
	return n
}

// TestReparentFeature_KeepsIdNumberAndHistory is the core of the verb: a
// move leaves exactly one current Feature row, under the same immutable
// id and the same `Cn` a caller already cites, with only feature_set_id
// changed -- and the row it displaced is closed, not deleted.
func TestReparentFeature_KeepsIdNumberAndHistory(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	moved, err := s.Reparent().ReparentFeature(ctx, f.feature.ID, f.to.ID)
	require.NoError(t, err)

	assert.Equal(t, f.feature.ID, moved.ID, "a move must never mint a new surrogate id (LB2)")
	assert.Equal(t, f.to.ID, moved.FeatureSetID)
	assert.Equal(t, f.feature.DisplayNumber, moved.DisplayNumber,
		"the `Cn` a caller already cites must survive a move or every citation of it breaks (migration 017)")
	assert.Equal(t, f.feature.Position, moved.Position)
	assert.Equal(t, f.feature.ScopeID, moved.ScopeID)
	assert.Equal(t, f.feature.Name, moved.Name)
	assert.Equal(t, f.feature.Description, moved.Description)
	assert.NotEqual(t, f.feature.RevisionID, moved.RevisionID, "a move opens a new revision row")

	current, err := s.Features().GetCurrentByID(ctx, f.feature.ID)
	require.NoError(t, err)
	assert.Equal(t, f.to.ID, current.FeatureSetID, "the current row must be the one under the new parent")
	assert.Nil(t, current.ValidTo)

	assert.Equal(t, 2, featureRevisionCount(t, ctx, db, f.feature.ID),
		"the prior revision must be closed and still present, never deleted")
}

// TestReparentFeature_RequirementsFollowAndDecisionsStay is the child
// question a reparent has to answer: the Feature's own Requirements key
// on its immutable id and follow it, while a LoadBearingDecision is
// FeatureSet-scoped and belongs to the FeatureSet the Feature left behind.
func TestReparentFeature_RequirementsFollowAndDecisionsStay(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, f.to.ID)
	require.NoError(t, err)

	underNew, err := s.Slices().ListRequirementsByFeatureSet(ctx, f.to.ID)
	require.NoError(t, err)
	require.Len(t, underNew, 1)
	assert.Equal(t, f.requirement.ID, underNew[0].ID,
		"the moved Feature's Requirements must resolve under its new FeatureSet")

	underOld, err := s.Slices().ListRequirementsByFeatureSet(ctx, f.from.ID)
	require.NoError(t, err)
	assert.Empty(t, underOld, "no Requirement may be left behind under the FeatureSet the Feature left")

	assert.Equal(t, f.feature.ID, underNew[0].FeatureID, "a Requirement's feature_id is unchanged by a move")

	oldDecisions, err := s.Decisions().ListCurrentByFeatureSet(ctx, f.from.ID)
	require.NoError(t, err)
	require.Len(t, oldDecisions, 1)
	assert.Equal(t, f.fromDecision.ID, oldDecisions[0].ID,
		"a decision belongs to the FeatureSet, so it stays with the FeatureSet the Feature moved out of")

	newDecisions, err := s.Decisions().ListCurrentByFeatureSet(ctx, f.to.ID)
	require.NoError(t, err)
	require.Len(t, newDecisions, 1)
	assert.Equal(t, f.toDecision.ID, newDecisions[0].ID,
		"a move must never pull the target FeatureSet's decision with it or evict the target's own")
}

// TestReparentFeature_DeliveredFeature_KeepsItsSingleDeliveryOwner is the
// branch that mattered most to check: a Feature a milestone already
// delivers can be moved between FeatureSets without disturbing the
// delivery axis. `entity_milestone` keys on the entity's immutable id and
// a move reuses that id, so the association -- and the whole of the
// milestone's Delivers and must-not-foreclose sets -- is untouched, and
// the Feature is still delivered by exactly one milestone afterwards.
func TestReparentFeature_DeliveredFeature_KeepsItsSingleDeliveryOwner(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "", nil, self, self)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, f.feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMustNotForeclose(ctx, scopeID, milestone.ID, f.fromDecision.ID, self, self))

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, f.to.ID)
	require.NoError(t, err)

	assert.True(t, deliversAssociation(t, ctx, db, f.feature.ID, milestone.ID),
		"a move must not touch the delivery axis: the Delivers association hangs off the immutable id")

	_, delivers, mustNot, _, err := s.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, delivers, 1, "the milestone must still deliver exactly the one Feature it delivered before the move")
	assert.Equal(t, f.feature.ID, delivers[0].EntityID)
	require.Len(t, mustNot, 1, "must-not-foreclose is a separate relation and must survive the move too")
	assert.Equal(t, f.fromDecision.ID, mustNot[0].EntityID)
}

// TestReparentFeature_CompetingDeliveryStillRefusedAfterMove is the other
// half of the same question: a move must not weaken the single-delivery-
// parent rule by making the moved Feature look undelivered. A competing
// milestone is refused exactly as it was before the move.
func TestReparentFeature_CompetingDeliveryStillRefusedAfterMove(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	first, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "", nil, self, self)
	require.NoError(t, err)
	second, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M7", "", nil, self, self)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, first.ID, f.feature.ID, self, self))

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, f.to.ID)
	require.NoError(t, err)

	err = s.MilestoneAuthoring().AddDelivers(ctx, scopeID, second.ID, f.feature.ID, self, self)
	require.ErrorIs(t, err, store.ErrEntityDeliveredByCompetingMilestone,
		"the moved Feature is still delivered -- a reparent must not read as a release")
	assert.False(t, deliversAssociation(t, ctx, db, f.feature.ID, second.ID))
}

// TestReparentFeature_CurrentParent_RefusedAsNoOp: a move to the parent
// the Feature already has is a named refusal, not a revision that changes
// nothing and not a silent success.
func TestReparentFeature_CurrentParent_RefusedAsNoOp(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, f.from.ID)
	require.ErrorIs(t, err, store.ErrReparentNoOp)

	assert.Equal(t, 1, featureRevisionCount(t, ctx, db, f.feature.ID),
		"a refused no-op must write nothing, not an identical second revision")
}

// TestReparentFeature_UnknownAndCrossScopeTargets_Refused: the target
// must be a current FeatureSet of the Feature's own scope, and an id
// that is not one is refused by name rather than written.
func TestReparentFeature_UnknownAndCrossScopeTargets_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	otherScopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	otherProduct, err := s.Products().Create(ctx, otherScopeID, "Other", "another scope's product")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	foreignSet, err := s.FeatureSets().Create(ctx, otherScopeID, otherProduct.ID, "Theirs", nil)
	require.NoError(t, err)

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, uuid.New())
	require.ErrorIs(t, err, store.ErrNotFound, "an id no FeatureSet row carries is not a parent")

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, foreignSet.ID)
	require.ErrorIs(t, err, store.ErrNotFound,
		"another scope's FeatureSet is not a parent either, however real the id is")

	assert.Equal(t, 1, featureRevisionCount(t, ctx, db, f.feature.ID), "a refused target must write nothing")

	_, err = s.Reparent().ReparentFeature(ctx, uuid.New(), f.to.ID)
	require.ErrorIs(t, err, store.ErrNotFound, "an unknown Feature id has no current row to move")
}

// TestReparentFeature_CrossProductTarget_Refused is the FeatureSet-level
// constraint a search for "feature_set" in the delivery-axis code would
// miss: a scope holds many Products, and a Feature's `Cn` is numbered
// product-wide while the renderer resolves a milestone's `Delivers: Cn`
// through the Product's own slice. Moving a Feature across a Product
// boundary would therefore carry the number into a second numbering space
// and drop the entity out of the milestone that delivers it, so the move
// is refused and names the boundary rather than being allowed.
func TestReparentFeature_CrossProductTarget_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	otherProduct, err := s.Products().Create(ctx, scopeID, "Other", "a sibling product in the same scope")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	otherSet, err := s.FeatureSets().Create(ctx, scopeID, otherProduct.ID, "Theirs", nil)
	require.NoError(t, err)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "", nil, self, self)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, f.feature.ID, self, self))

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, otherSet.ID)
	require.ErrorIs(t, err, store.ErrReparentAcrossProduct)
	assert.Contains(t, err.Error(), otherProduct.ID.String(),
		"the refusal must name the product the move would have crossed into")

	assert.Equal(t, 1, featureRevisionCount(t, ctx, db, f.feature.ID), "a refused cross-product move must write nothing")
	assert.True(t, deliversAssociation(t, ctx, db, f.feature.ID, milestone.ID))
}

// TestReparentFeature_NameCollisionInTarget_Refused: the name index is
// scoped by (scope_id, feature_set_id, lower(name)), so a target
// FeatureSet that already holds a live Feature of the same name is
// refused with the same ErrNameConflict a create's collision gets.
func TestReparentFeature_NameCollisionInTarget_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	_, err = s.Features().Create(ctx, scopeID, f.to.ID, f.feature.Name, nil)
	require.NoError(t, err)

	_, err = s.Reparent().ReparentFeature(ctx, f.feature.ID, f.to.ID)
	require.ErrorIs(t, err, store.ErrNameConflict)

	assert.Equal(t, 1, featureRevisionCount(t, ctx, db, f.feature.ID),
		"a refused collision must roll the close back with the rest of the transaction")

	current, err := s.Features().GetCurrentByID(ctx, f.feature.ID)
	require.NoError(t, err)
	assert.Equal(t, f.from.ID, current.FeatureSetID, "the Feature must still be where it started")
}

// TestReparentFeature_PositionCarriedForward pins the documented choice
// about position: the Feature keeps the position it had under its
// previous parent rather than being renumbered into the target set, and a
// collision there is resolved by the (position, name) ordering every read
// already uses rather than by an error.
func TestReparentFeature_PositionCarriedForward(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newReparentFixture(t, ctx, s, scopeID, product.ID)

	// A second Feature under the target FeatureSet at the same position,
	// so the tie is real rather than hypothetical.
	collider, err := s.Features().Create(ctx, scopeID, f.to.ID, "Zebra", nil)
	require.NoError(t, err)

	moved, err := s.Reparent().ReparentFeature(ctx, f.feature.ID, f.to.ID)
	require.NoError(t, err)
	assert.Equal(t, collider.Position, moved.Position, "the fixture's positions must actually collide for this test to mean anything")

	siblings, err := s.Features().ListCurrentByFeatureSet(ctx, f.to.ID)
	require.NoError(t, err)
	require.Len(t, siblings, 2)
	// Both are present exactly once, in the (position, name) order every
	// read uses -- no error, no dropped row.
	assert.Equal(t, []string{"Search", "Zebra"}, []string{siblings[0].Name, siblings[1].Name},
		"a position tie must fall through to name deterministically, not reorder arbitrarily")
}
