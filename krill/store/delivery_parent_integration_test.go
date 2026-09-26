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
	"github.com/whale-net/everything/libs/go/dbtest"
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

// milepbbleParentFixture is M1 plus two cuts cut from it, and a competing
// milestone M2 of the same product -- the exact shape that made
// MoveScope(E, from=MP, to=M2) leave E delivered by both M1 and M2.
type milepebbleParentFixture struct {
	parent     uuid.UUID // M1
	cut1, cut2 uuid.UUID // two milepebbles of M1
	competing  uuid.UUID // M2
	entity     uuid.UUID
}

func newMilepebbleParentFixture(t *testing.T, ctx context.Context, s *store.Store, scopeID, productID uuid.UUID) milepebbleParentFixture {
	t.Helper()
	self := milestoneAuthoringTestSubject("agent-1")

	parent, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, productID, "M1", "", nil, self, self)
	require.NoError(t, err)
	cut1, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, parent.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)
	cut2, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, parent.ID, "cut 2", "", nil, self, self)
	require.NoError(t, err)
	competing, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, productID, "M2", "", nil, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, productID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)

	// M1 delivers E, and the subset invariant means so does every cut
	// that narrows it -- the state the move has to unwind cleanly.
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, parent.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, cut1.ID, feature.ID, self, self))

	return milepebbleParentFixture{
		parent: parent.ID, cut1: cut1.ID, cut2: cut2.ID,
		competing: competing.ID, entity: feature.ID,
	}
}

// milestoneLevelDelivers returns every `kind='milestone'` row of this
// product that delivers the entity -- the level LB6 says there can be at
// most one of.
func milestoneLevelDelivers(t *testing.T, ctx context.Context, db *dbtest.Postgres, scopeID, productID, entityID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT em.milestone_id FROM entity_milestone em
		JOIN milestone_ref mr ON mr.id = em.milestone_id
		WHERE em.entity_id = $1 AND em.relation = 'delivers'
		  AND mr.kind = 'milestone' AND mr.scope_id = $2 AND mr.product_id = $3
	`, entityID, scopeID, productID)
	require.NoError(t, err)
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// TestMoveScope_MilepebbleToCompetingMilestone_LeavesOneMilestoneOwner is
// the CRITICAL case the re-cut got wrong: moving an entity out of a
// milepebble into a competing milestone must not leave the milepebble's
// parent milestone still delivering it. Before the fix the parent row
// survived the move, E was delivered by both M1 and M2 -- the exact state
// addDeliversTx refuses -- and M2's own later AddDelivers of E tripped
// that refusal for a stale M1 association nobody could see.
func TestMoveScope_MilepebbleToCompetingMilestone_LeavesOneMilestoneOwner(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.entity}, f.cut1, f.competing, self, self))

	assert.Equal(t, []uuid.UUID{f.competing}, milestoneLevelDelivers(t, ctx, db, scopeID, product.ID, f.entity),
		"after the move the entity must be delivered by exactly one milestone -- the destination, not the milepebble's parent")
	assert.False(t, deliversAssociation(t, ctx, db, f.entity, f.cut1), "the source cut must not keep delivering the entity")
	assert.True(t, deliversAssociation(t, ctx, db, f.entity, f.competing), "the destination must deliver the entity")
	assert.False(t, deliversAssociation(t, ctx, db, f.entity, f.parent),
		"the milepebble's parent milestone must not survive as a second milestone-level owner")

	// And the stale M1 row must not be what makes the next plan fail.
	require.NoError(t, s.MilestoneAuthoring().AddDeliversMany(ctx, scopeID, f.competing, []uuid.UUID{f.entity}, self, self),
		"a milestone that already holds the entity through the re-cut must be able to re-deliver it idempotently, not be refused against a stale row")
}

// TestMoveScope_MilepebbleOut_ParentRowSurvivesWhileSiblingCutDelivers is
// the sibling case: the parent milestone's association is only ever a
// consequence of some cut delivering the entity, so it may only be
// dropped once the last cut lets go. While a sibling cut of M1 still
// delivers the entity, moving the entity out of MP1 must leave M1's row
// in place -- the FR3 subset invariant demands exactly that.
func TestMoveScope_MilepebbleOut_ParentRowSurvivesWhileSiblingCutDelivers(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, f.cut2, f.entity, self, self))

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.entity}, f.cut1, f.cut2, self, self))

	assert.False(t, deliversAssociation(t, ctx, db, f.entity, f.cut1), "the entity leaves the cut it was moved out of")
	assert.True(t, deliversAssociation(t, ctx, db, f.entity, f.cut2), "the entity lands in the sibling cut")
	assert.True(t, deliversAssociation(t, ctx, db, f.entity, f.parent),
		"the shared parent must keep delivering the entity while cut 2 still delivers it -- FR3's subset invariant")
}

// TestMoveScope_MilepebbleOut_SiblingCut_CompetingMilestone_Refused pins
// the one move that cannot be made cleanly. M1's association has to
// survive (cut 2 still delivers the entity) and the destination is a
// competing milestone, so applying it would give the entity two
// milestone-level owners -- the state LB6 forbids. The move is refused
// loudly, writing nothing, rather than half-applied.
func TestMoveScope_MilepebbleOut_SiblingCut_CompetingMilestone_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, f.cut2, f.entity, self, self))

	err = s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.entity}, f.cut1, f.competing, self, self)
	require.ErrorIs(t, err, store.ErrEntityDeliveredBySiblingCut)
	assert.Contains(t, err.Error(), f.cut2.String(), "the refusal must name the sibling cut still delivering the entity")

	assert.True(t, deliversAssociation(t, ctx, db, f.entity, f.cut1), "a refused move must leave the source cut's association untouched")
	assert.False(t, deliversAssociation(t, ctx, db, f.entity, f.competing), "a refused move must write nothing into the destination")
	assert.Equal(t, []uuid.UUID{f.parent}, milestoneLevelDelivers(t, ctx, db, scopeID, product.ID, f.entity),
		"a refused move must not change which milestone owns the entity")
}

// TestMoveScope_LastCutLetsGo_ParentMilestoneRowIsDropped closes the
// lifecycle from the other side: the parent's association survives only
// while some cut of that parent delivers the entity, so the move that
// takes the entity out of the LAST cut of M1 and into a competing
// milestone is the one that leaves M1 with no Delivers row for it. The
// same re-cut into the backlog bucket deliberately does not -- abandoning
// a cut is a narrower act than re-cutting it, and FR9 pins that the
// parent milestone's own claim survives it.
func TestMoveScope_LastCutLetsGo_ParentMilestoneRowIsDropped(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, f.cut2, f.entity, self, self))

	// One cut lets go, the sibling still delivers it: the parent keeps it.
	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.entity}, f.cut1, f.cut2, self, self))
	assert.True(t, deliversAssociation(t, ctx, db, f.entity, f.parent),
		"cut 2 still delivers the entity, so the parent milestone keeps its own claim on it")

	// The last cut lets go, to a competing milestone: the parent is left
	// with no Delivers row for the entity at all.
	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.entity}, f.cut2, f.competing, self, self))
	assert.False(t, deliversAssociation(t, ctx, db, f.entity, f.parent),
		"once the last cut of the parent stops delivering the entity to a competing milestone, the parent must be left with no Delivers row for it")
	assert.Equal(t, []uuid.UUID{f.competing}, milestoneLevelDelivers(t, ctx, db, scopeID, product.ID, f.entity),
		"the destination is the entity's only milestone-level owner")
}

// TestMoveScope_MilepebbleToBacklog_ParentMilestoneKeepsItsClaim pins
// the other side of the same rule, so a later change does not widen the
// parent-release to destinations it must not touch. Sweeping a cut's
// scope into the backlog is what Abandon does (FR9), and abandoning a
// milepebble alone leaves the parent milestone's own Delivers
// association exactly where it is.
func TestMoveScope_MilepebbleToBacklog_ParentMilestoneKeepsItsClaim(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	f := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	backlog, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, product.ID, self, self)
	require.NoError(t, err)
	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{f.entity}, f.cut1, backlog.ID, self, self))

	assert.False(t, deliversAssociation(t, ctx, db, f.entity, f.cut1), "the entity leaves the abandoned cut")
	assert.True(t, deliversAssociation(t, ctx, db, f.entity, backlog.ID), "the entity lands in the backlog bucket")
	assert.True(t, deliversAssociation(t, ctx, db, f.entity, f.parent),
		"FR9: abandoning a milepebble alone must not touch its parent milestone's own Delivers association")
}
