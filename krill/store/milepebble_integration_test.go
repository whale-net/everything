//go:build integration

// Real-Postgres coverage for the milepebble surface of
// MilestoneAuthoringStore (milestone_authoring.go, migration 011, issue
// #2684's Testing section, FR3/FR4): CreateMilepebble scoped to exactly
// one milestone and rejecting a non-milestone parent, ListMilepebblesByMilestone's
// FR7 position order, AddMilepebbleDelivers' FR3 subset enforcement
// (ErrMilepebbleDeliversNotSubset, a loud rejection rather than a silent
// skip), and AddDiscoveredScope's FR4 path -- a real Feature or
// Requirement row created and atomically associated to both the
// milepebble and its parent milestone's Delivers set, with the parent
// milestone's own authoring rows (outcome/fr_budget/deferrals) left
// untouched. See milestone_authoring_integration_test.go's package doc
// for why this file only builds under the "integration" build tag, and
// for the shared newMilestoneAuthoringTestStore/newMilestoneAuthoringTestScope/
// milestoneAuthoringTestSubject helpers this file reuses (same package).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:milepebble_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestMilepebbleStore_CreateMilepebble_CutIntoThree_ListedInPositionOrder
// is issue #2684's Testing section item 1: cutting a milestone into three
// milepebbles and listing them via ListMilepebblesByMilestone returns them
// in creation/position order (FR7).
func TestMilepebbleStore_CreateMilepebble_CutIntoThree_ListedInPositionOrder(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship it incrementally", nil, self, self)
	require.NoError(t, err)

	p1, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "first slice", nil, self, self)
	require.NoError(t, err)
	p2, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 2", "second slice", nil, self, self)
	require.NoError(t, err)
	p3, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 3", "third slice", nil, self, self)
	require.NoError(t, err)

	for _, p := range []store.MilestoneRef{p1, p2, p3} {
		assert.Equal(t, store.MilestoneKindMilepebble, p.Kind)
		require.NotNil(t, p.ParentMilestoneID)
		assert.Equal(t, milestone.ID, *p.ParentMilestoneID)
	}

	milepebbles, err := s.MilestoneAuthoring().ListMilepebblesByMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, milepebbles, 3)
	assert.Equal(t, []uuid.UUID{p1.ID, p2.ID, p3.ID}, []uuid.UUID{milepebbles[0].ID, milepebbles[1].ID, milepebbles[2].ID},
		"ListMilepebblesByMilestone must return milepebbles in Position order (FR7)")
	assert.Less(t, milepebbles[0].Position, milepebbles[1].Position)
	assert.Less(t, milepebbles[1].Position, milepebbles[2].Position)
}

// TestMilepebbleStore_CreateMilepebble_FRBudget_PerMilepebble proves the FR
// budget lives on each milepebble: two milepebbles of one budgetless
// milestone each carry their own budget, and SetFRBudget revises one alone.
func TestMilepebbleStore_CreateMilepebble_FRBudget_PerMilepebble(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "a milestone too big for one budget", nil, self, self)
	require.NoError(t, err)

	budget := 12
	p1, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "first slice", &budget, self, self)
	require.NoError(t, err)
	p2, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 2", "second slice", &budget, self, self)
	require.NoError(t, err)
	require.NotNil(t, p1.FRBudget)
	assert.Equal(t, 12, *p1.FRBudget)

	require.NoError(t, s.MilestoneAuthoring().SetFRBudget(ctx, p2.ID, 8, self, self))

	milepebbles, err := s.MilestoneAuthoring().ListMilepebblesByMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, milepebbles, 2)
	require.NotNil(t, milepebbles[0].FRBudget)
	require.NotNil(t, milepebbles[1].FRBudget)
	assert.Equal(t, 12, *milepebbles[0].FRBudget)
	assert.Equal(t, 8, *milepebbles[1].FRBudget)

	parent, _, _, _, err := s.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Nil(t, parent.FRBudget, "a milepebble's budget never writes through to its parent milestone")
}

// TestMilepebbleStore_CreateMilepebble_UnknownParent_ReturnsErrNotFound is
// issue #2684's Testing section item 2's first half: a milepebble cannot
// be created without a real parent milestone.
func TestMilepebbleStore_CreateMilepebble_UnknownParent_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	self := milestoneAuthoringTestSubject("agent-1")
	_, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, uuid.New(), "cut 1", "", nil, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_ref WHERE kind = 'milepebble'`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected CreateMilepebble must insert no row")
}

// TestMilepebbleStore_CreateMilepebble_ParentIsAnotherMilepebble_Rejected
// is issue #2684's Testing section item 2's second half: a milepebble's
// parent must be a milestone, never another milepebble -- FR3's "exactly
// one milestone" (a milepebble cannot itself become a second parent).
func TestMilepebbleStore_CreateMilepebble_ParentIsAnotherMilepebble_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)

	_, err = s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milepebble.ID, "cut 1 of cut 1", "", nil, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound, "a milepebble's parent must be a milestone, never another milepebble")

	milepebbles, err := s.MilestoneAuthoring().ListMilepebblesByMilestone(ctx, milepebble.ID)
	require.NoError(t, err)
	assert.Empty(t, milepebbles, "a rejected CreateMilepebble must insert no row, including under the rejected parent")
}

// TestMilepebbleStore_AddMilepebbleDelivers_SucceedsForSubsetEntity_RejectsNonSubset
// is issue #2684's Testing section item 3: AddMilepebbleDelivers succeeds
// for an entity in the parent's Delivers set and fails loudly (a named
// error, not a silent skip) for one that is not.
func TestMilepebbleStore_AddMilepebbleDelivers_SucceedsForSubsetEntity_RejectsNonSubset(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	inSet, err := s.Features().Create(ctx, scopeID, featureSet.ID, "in-set", nil)
	require.NoError(t, err)
	notInSet, err := s.Features().Create(ctx, scopeID, featureSet.ID, "not-in-set", nil)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, inSet.ID, self, self),
		"seed the parent milestone's own Delivers set with exactly one entity")

	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, inSet.ID, self, self),
		"an entity already in the parent milestone's Delivers set must be accepted")

	err = s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, notInSet.ID, self, self)
	assert.ErrorIs(t, err, store.ErrMilepebbleDeliversNotSubset,
		"an entity NOT in the parent milestone's Delivers set must be rejected loudly, never a silent skip")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_milestone WHERE milestone_id = $1
	`, milepebble.ID).Scan(&count))
	assert.Equal(t, 1, count, "the rejected AddMilepebbleDelivers call must insert no row -- only the accepted one landed")
}

// TestMilepebbleStore_AddMilepebbleDelivers_UnknownMilepebble_ReturnsErrNotFound
// proves AddMilepebbleDelivers rejects a target id that is not a real
// milepebble row (unknown id, or a plain milestone's own id) rather than
// silently associating against the wrong row.
func TestMilepebbleStore_AddMilepebbleDelivers_UnknownMilepebble_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	err = s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, uuid.New(), feature.ID, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)

	// A plain milestone's own id is not a milepebble either.
	err = s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound, "a plain milestone id is not a milepebble")
}

// TestMilepebbleStore_AddDiscoveredScope_Requirement is issue #2684's
// Testing section item 4: adding a discovered Requirement to an existing
// milepebble creates a real `requirement` row with a correct parent
// feature_id, an entity_milestone row for both the milepebble and the
// parent milestone, and leaves the parent milestone's outcome/FR
// budget/deferral rows unchanged.
func TestMilepebbleStore_AddDiscoveredScope_Requirement(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	budget := 4
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship the outcome", &budget, self, self)
	require.NoError(t, err)
	deferral, err := s.MilestoneAuthoring().AddDeferral(ctx, scopeID, milestone.ID, "cut for later", "M4", self, self)
	require.NoError(t, err)

	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)

	body := "the system must do X"
	result, err := s.MilestoneAuthoring().AddDiscoveredScope(ctx, scopeID, milepebble.ID, store.DiscoveredScopeInput{
		FeatureID:       &feature.ID,
		RequirementKind: store.RequirementKindFR,
		Name:            "discovered FR",
		Body:            &body,
	}, self, self)
	require.NoError(t, err)
	assert.Equal(t, store.DiscoveredScopeEntityKindRequirement, result.Kind)
	assert.NotEqual(t, uuid.Nil, result.EntityID)

	// A real requirement row exists with the correct parent feature_id.
	var gotFeatureID uuid.UUID
	var gotName string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT feature_id, name FROM requirement WHERE id = $1 AND valid_to IS NULL
	`, result.EntityID).Scan(&gotFeatureID, &gotName))
	assert.Equal(t, feature.ID, gotFeatureID)
	assert.Equal(t, "discovered FR", gotName)

	// entity_milestone rows exist for both the milepebble and the parent
	// milestone (FR3's subset invariant holds again the instant this
	// transaction commits).
	var inMilepebble, inParent bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM entity_milestone WHERE entity_id = $1 AND milestone_id = $2 AND relation = 'delivers')
	`, result.EntityID, milepebble.ID).Scan(&inMilepebble))
	assert.True(t, inMilepebble, "the discovered requirement must be associated to the milepebble")
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM entity_milestone WHERE entity_id = $1 AND milestone_id = $2 AND relation = 'delivers')
	`, result.EntityID, milestone.ID).Scan(&inParent))
	assert.True(t, inParent, "the discovered requirement must also be associated to the parent milestone's Delivers set")

	// AddMilepebbleDelivers' own subset check now accepts this entity
	// against the milepebble again -- proving the invariant holds, not
	// merely that both rows happen to exist.
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, result.EntityID, self, self))

	// The parent milestone's own authoring rows are untouched.
	gotMilestone, _, _, deferrals, err := s.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.NotNil(t, gotMilestone.Outcome)
	assert.Equal(t, "ship the outcome", *gotMilestone.Outcome)
	require.NotNil(t, gotMilestone.FRBudget)
	assert.Equal(t, 4, *gotMilestone.FRBudget)
	require.Len(t, deferrals, 1)
	assert.Equal(t, deferral.ID, deferrals[0].ID)
}

// TestMilepebbleStore_AddDiscoveredScope_OneOffFeature is issue #2684's
// Testing section item 5: adding a discovered Feature -- a one-off fix not
// cited by any existing FR -- behaves the same way as the Requirement
// case above.
func TestMilepebbleStore_AddDiscoveredScope_OneOffFeature(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship the outcome", nil, self, self)
	require.NoError(t, err)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)

	description := "a fix nobody wrote down ahead of time"
	result, err := s.MilestoneAuthoring().AddDiscoveredScope(ctx, scopeID, milepebble.ID, store.DiscoveredScopeInput{
		FeatureSetID: &featureSet.ID,
		Name:         "one-off fix",
		Description:  &description,
	}, self, self)
	require.NoError(t, err)
	assert.Equal(t, store.DiscoveredScopeEntityKindFeature, result.Kind)

	var gotFeatureSetID uuid.UUID
	var gotName string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT feature_set_id, name FROM feature WHERE id = $1 AND valid_to IS NULL
	`, result.EntityID).Scan(&gotFeatureSetID, &gotName))
	assert.Equal(t, featureSet.ID, gotFeatureSetID)
	assert.Equal(t, "one-off fix", gotName)

	var inMilepebble, inParent bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM entity_milestone WHERE entity_id = $1 AND milestone_id = $2 AND relation = 'delivers')
	`, result.EntityID, milepebble.ID).Scan(&inMilepebble))
	assert.True(t, inMilepebble)
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM entity_milestone WHERE entity_id = $1 AND milestone_id = $2 AND relation = 'delivers')
	`, result.EntityID, milestone.ID).Scan(&inParent))
	assert.True(t, inParent)

	gotMilestone, _, _, deferrals, err := s.MilestoneAuthoring().GetMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.NotNil(t, gotMilestone.Outcome)
	assert.Equal(t, "ship the outcome", *gotMilestone.Outcome)
	assert.Empty(t, deferrals)
}

// TestMilepebbleStore_AddDiscoveredScope_UnknownMilepebble_ReturnsErrNotFound_NoRowInserted
// proves AddDiscoveredScope inserts neither the entity nor an association
// when the target milepebble does not exist -- the Create-then-associate
// sequence must be fully transactional.
func TestMilepebbleStore_AddDiscoveredScope_UnknownMilepebble_ReturnsErrNotFound_NoRowInserted(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	_, err = s.MilestoneAuthoring().AddDiscoveredScope(ctx, scopeID, uuid.New(), store.DiscoveredScopeInput{
		FeatureSetID: &featureSet.ID,
		Name:         "should not land",
	}, self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM feature WHERE name = 'should not land'`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected AddDiscoveredScope must insert no entity row")
}
