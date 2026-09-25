//go:build integration

// Store-level coverage for the two halves of the single-delivery-parent
// rule: one AddDeliversMany call spans every FeatureSet a milestone's
// scope reaches, and no second milestone of the same product can claim
// an entity another one already delivers without a re-cut first.
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
)

// deliveryParentFixture is a product whose scope deliberately straddles
// two FeatureSets named for lanes (`Now` and `Next`) -- the shape that
// used to force one plan invocation per FeatureSet. The naming is
// cosmetic; what these tests pin is that the batch and the single-owner
// rule treat it as such.
type deliveryParentFixture struct {
	nowFeature, nextFeature        uuid.UUID
	nowRequirement, nextRequirement uuid.UUID
	untouched                      uuid.UUID
}

func newDeliveryParentFixture(t *testing.T, ctx context.Context, s *store.Store, scopeID, productID uuid.UUID) deliveryParentFixture {
	t.Helper()

	now, err := s.FeatureSets().Create(ctx, scopeID, productID, "Now", nil)
	require.NoError(t, err)
	next, err := s.FeatureSets().Create(ctx, scopeID, productID, "Next", nil)
	require.NoError(t, err)

	nowFeature, err := s.Features().Create(ctx, scopeID, now.ID, "C-now", nil)
	require.NoError(t, err)
	nextFeature, err := s.Features().Create(ctx, scopeID, next.ID, "C-next", nil)
	require.NoError(t, err)
	nowRequirement, err := s.Requirements().Create(ctx, scopeID, nowFeature.ID, store.RequirementKindFR, "FR-now", nil)
	require.NoError(t, err)
	nextRequirement, err := s.Requirements().Create(ctx, scopeID, nextFeature.ID, store.RequirementKindFR, "FR-next", nil)
	require.NoError(t, err)
	untouched, err := s.Features().Create(ctx, scopeID, next.ID, "C-untouched", nil)
	require.NoError(t, err)

	return deliveryParentFixture{
		nowFeature:        nowFeature.ID,
		nextFeature:       nextFeature.ID,
		nowRequirement:    nowRequirement.ID,
		nextRequirement:   nextRequirement.ID,
		untouched:         untouched.ID,
	}
}

func (f deliveryParentFixture) spanningBatch() []uuid.UUID {
	return []uuid.UUID{f.nowFeature, f.nowRequirement, f.nextFeature, f.nextRequirement}
}

// TestAddDeliversMany_SpansMultipleFeatureSets_InOneCall is the
// single-plan-op half: a milestone whose Delivers reach two FeatureSets
// is delivered in one invocation, not one invocation per FeatureSet, and
// every entity in the batch lands.
func TestAddDeliversMany_SpansMultipleFeatureSets_InOneCall(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newDeliveryParentFixture(t, ctx, s, scopeID, product.ID)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "spans Now and Next", nil, self, self)
	require.NoError(t, err)

	// One call, four entities, two FeatureSets.
	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, milestone.ID, f.spanningBatch(), self, self))

	_, delivers, _, _, err := s.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)

	got := map[uuid.UUID]bool{}
	for _, m := range delivers {
		got[m.EntityID] = true
	}
	for _, id := range f.spanningBatch() {
		assert.True(t, got[id], "one batched call must deliver every entity it was given, whatever FeatureSet parents it")
	}
	assert.Len(t, delivers, 4, "the batch must add exactly the four entities asked for, no more")
	assert.False(t, got[f.untouched], "an entity outside the batch must not be delivered as a side effect")
}

// TestAddDeliversMany_EmptyBatch_Rejected pins the one thing a
// zero-length batch cannot mean: silently succeeding.
func TestAddDeliversMany_EmptyBatch_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	assert.Error(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, milestone.ID, nil, self, self))
}

// TestAddDeliversMany_CompetingMilestone_Refused_BatchWritesNothing is
// the single-delivery-parent half's refusal: an entity another milestone
// of the same product already delivers cannot be claimed by a second
// one, and the refusal is all-or-nothing over the whole batch -- the
// legal entities in the same call are not written either.
func TestAddDeliversMany_CompetingMilestone_Refused_BatchWritesNothing(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newDeliveryParentFixture(t, ctx, s, scopeID, product.ID)

	first, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "", nil, self, self)
	require.NoError(t, err)
	second, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M7", "", nil, self, self)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, first.ID, []uuid.UUID{f.nextFeature}, self, self))

	// The batch deliberately leads with a legal entity so the assertion
	// below distinguishes "the whole batch was refused" from "the batch
	// applied until it hit the conflict".
	err = s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, second.ID,
		[]uuid.UUID{f.nowFeature, f.nextFeature}, self, self)
	require.ErrorIs(t, err, store.ErrEntityDeliveredByCompetingMilestone)
	assert.Contains(t, err.Error(), first.ID.String(), "the refusal must name the competing milestone so the caller knows what to re-cut from")

	assert.False(t, deliversAssociation(t, ctx, db, f.nextFeature, second.ID),
		"the competing entity must not gain a second delivery parent")
	assert.False(t, deliversAssociation(t, ctx, db, f.nowFeature, second.ID),
		"a refused batch must write nothing at all, not even the entities it could legally have taken")
}

// TestAddDeliversMany_SameMilestoneReAdd_Idempotent: re-planning a
// milestone whose scope has not changed is the common case, and it must
// stay a silent no-op rather than tripping the single-owner rule against
// the milestone itself.
func TestAddDeliversMany_SameMilestoneReAdd_Idempotent(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newDeliveryParentFixture(t, ctx, s, scopeID, product.ID)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "", nil, self, self)
	require.NoError(t, err)

	batch := f.spanningBatch()
	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, milestone.ID, batch, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, milestone.ID, batch, self, self))

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM entity_milestone WHERE milestone_id = $1`, milestone.ID).Scan(&count))
	assert.Equal(t, len(batch), count, "re-delivering the same batch to the same milestone must not duplicate rows")
}

// TestAddDeliversMany_ReCut_FreesTheEntityForTheCompetingMilestone is
// the whole rule end to end: the re-cut is the only thing that hands an
// already-delivered entity to a competing milestone, and once it has
// moved, the second milestone can deliver it cleanly.
func TestAddDeliversMany_ReCut_FreesTheEntityForTheCompetingMilestone(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newDeliveryParentFixture(t, ctx, s, scopeID, product.ID)

	from, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "", nil, self, self)
	require.NoError(t, err)
	to, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M7", "", nil, self, self)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, from.ID, []uuid.UUID{f.nextFeature}, self, self))
	require.ErrorIs(t,
		s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, to.ID, []uuid.UUID{f.nextFeature}, self, self),
		store.ErrEntityDeliveredByCompetingMilestone)

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.nextFeature}, from.ID, to.ID, self, self))
	assert.False(t, deliversAssociation(t, ctx, db, f.nextFeature, from.ID), "the re-cut must drop the old parent")
	assert.True(t, deliversAssociation(t, ctx, db, f.nextFeature, to.ID), "the re-cut must land the association on the new parent")

	// And the re-cut really did resolve the conflict rather than merely
	// getting in the way of it.
	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, to.ID, []uuid.UUID{f.nextFeature}, self, self))
}

// TestAddDeliversMany_MilepebbleAndBacklog_AreNotCompetingOwners pins
// the scope of the rule. A milepebble only narrows its parent
// milestone's claim (FR3's subset invariant deliberately associates both
// with one entity), and the backlog bucket is the un-delivered state --
// neither is a second claim on the same lane, so neither may be refused.
func TestAddDeliversMany_MilepebbleAndBacklog_AreNotCompetingOwners(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newDeliveryParentFixture(t, ctx, s, scopeID, product.ID)

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M6", "", nil, self, self)
	require.NoError(t, err)
	cut, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, milestone.ID, []uuid.UUID{f.nextFeature}, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, cut.ID, f.nextFeature, self, self),
		"a milepebble narrowing its own parent milestone's claim is not a competing delivery parent")

	backlog, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, product.ID, self, self)
	require.NoError(t, err)
	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.nextFeature}, milestone.ID, backlog.ID, self, self))

	// The entity is now un-delivered, so a milestone can take it again.
	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, milestone.ID, []uuid.UUID{f.nextFeature}, self, self))
	assert.False(t, deliversAssociation(t, ctx, db, f.nextFeature, cut.ID),
		"the milestone-level re-cut drops the milepebble's association too -- a cut cannot outlive its parent's claim")
}
