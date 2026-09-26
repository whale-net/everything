//go:build integration

// Real-Postgres coverage for the in-transaction `milestone_ref` parentage
// guard that migration 020's dropped FKs left behind on two write paths:
// MilestoneStore.AddAssociation (milestone.go) and
// DeliveryShipmentStore.MarkShipped (delivery_shipment.go).
//
// 020 SCD2s `milestone_ref`, so its `id` is no longer unique table-wide and
// Postgres cannot target a FOREIGN KEY at it -- 020 dropped
// entity_milestone_milestone_id_fkey and
// delivery_shipment_milestone_id_fkey. 020's own header promises that
// "parent existence [is] validated by krill/store inside the writing
// transaction (currentRowExists/errParentNotFound)"; these tests are what
// hold the store to that promise on the two paths that did not.
//
// The pair matters: AddAssociation is the bootstrap hole. A dangling
// (entity, milestone) `delivers` row planted there would satisfy
// MarkShipped's own milestone_ref check if MarkShipped only checked the
// association, so the last test here walks the full two-step corruption
// and shows it no longer lands.
//
// Shares milestone_authoring_integration_test.go's test-store/test-scope/
// subject helpers (same package, same build tag) rather than duplicating
// them.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:delivery_shipment_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// TestMilestoneStore_AddAssociation_UnknownMilestone_Refused: entity_milestone
// has no FK to milestone_ref since 020, so AddAssociation must reject an
// unknown milestoneID in transaction and write nothing.
func TestMilestoneStore_AddAssociation_UnknownMilestone_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	unknown := uuid.New()
	err := s.Milestones().AddAssociation(ctx, scopeID, uuid.New(), unknown)
	require.Error(t, err, "AddAssociation must reject a milestone that does not exist")
	assert.ErrorIs(t, err, store.ErrNotFound, "the rejection is the errParentNotFound shape (wrapping ErrNotFound)")

	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_milestone WHERE milestone_id = $1
	`, unknown).Scan(&n))
	assert.Zero(t, n, "a refused AddAssociation writes no entity_milestone row")
}

// TestMilestoneStore_AddAssociation_KnownMilestone_StillInserts: the guard
// must not over-block -- a real milestone still accepts the association, and
// still stays idempotent for the same (entity, milestone, relation) triple.
func TestMilestoneStore_AddAssociation_KnownMilestone_StillInserts(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	entityID := uuid.New()
	require.NoError(t, s.Milestones().AddAssociation(ctx, scopeID, entityID, milestone.ID))
	require.NoError(t, s.Milestones().AddAssociation(ctx, scopeID, entityID, milestone.ID), "AddAssociation stays idempotent")

	associations, err := s.Milestones().ListAssociationsByMilestone(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, associations, 1, "the second call must not duplicate the association")
	assert.Equal(t, entityID, associations[0].EntityID)
}

// TestDeliveryShipmentStore_MarkShipped_UnknownMilestone_Refused:
// delivery_shipment lost its FK to milestone_ref in 020, and MarkShipped
// checked only the `delivers` association -- never the milestone itself.
// With both fixes landed no such row is reachable through the store, so
// MarkShipped must now refuse and write nothing.
func TestDeliveryShipmentStore_MarkShipped_UnknownMilestone_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	self := milestoneAuthoringTestSubject("agent-1")
	unknown := uuid.New()
	err := s.DeliveryShipments().MarkShipped(ctx, scopeID, unknown, uuid.New(), nil, self, self)
	require.Error(t, err, "MarkShipped must reject a milestone that does not exist")
	assert.ErrorIs(t, err, store.ErrNotFound, "the rejection is the errParentNotFound shape (wrapping ErrNotFound)")

	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM delivery_shipment WHERE milestone_id = $1
	`, unknown).Scan(&n))
	assert.Zero(t, n, "a refused MarkShipped writes no delivery_shipment row")
}

// TestDeliveryShipmentStore_MarkShipped_KnownMilestone_StillAccepts: the
// happy path is unchanged -- no over-blocking.
func TestDeliveryShipmentStore_MarkShipped_KnownMilestone_StillAccepts(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))

	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, feature.ID, nil, self, self))

	shipped, err := s.DeliveryShipments().ShippedEntityIDs(ctx, milestone.ID)
	require.NoError(t, err)
	assert.True(t, shipped[feature.ID], "a valid milestone still accepts a shipment")
}

// TestMilestoneParentGuard_DanglingShipmentIsUnreachable walks the full
// two-step corruption this pair of guards closes: plant a dangling
// (entity, milestone) `delivers` row via AddAssociation, then ship against
// it. Before the fixes both steps succeeded; now the first is refused and
// the second is refused independently, so no delivery_shipment row can be
// planted against a milestone that does not exist.
func TestMilestoneParentGuard_DanglingShipmentIsUnreachable(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")

	unknown := uuid.New()
	entityID := uuid.New()

	// Step 1: the bootstrap hole. Refused.
	require.ErrorIs(t, s.Milestones().AddAssociation(ctx, scopeID, entityID, unknown), store.ErrNotFound)

	// Step 2: the shipment itself. Refused, and independently so -- the
	// milestone_ref check fires before the delivers-association check.
	err := s.DeliveryShipments().MarkShipped(ctx, scopeID, unknown, entityID, nil, self, self)
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.NotErrorIs(t, err, store.ErrEntityNotDelivered, "the milestone_ref guard fires first, not the association guard")

	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM delivery_shipment WHERE milestone_id = $1
	`, unknown).Scan(&n))
	assert.Zero(t, n)
}

// TestMilestoneStore_AddAssociation_CrossScopeMilestone_Refused:
// currentRowExists filters `id AND scope_id AND valid_to IS NULL`, so the
// scope term is doing real work in both parentage guards, not just
// decorative tenant hygiene. A milestone that is perfectly real but belongs
// to another scope is not a parent of anything in this one (LB1: every
// spec-axis row is reachable only within its own scope), so AddAssociation
// must refuse it and write nothing.
//
// The id is what the caller supplies, so dropping `AND scope_id = $2` from
// the guard's query would make this test -- and nothing else in the file --
// go red. That is the whole point of stating it.
func TestMilestoneStore_AddAssociation_CrossScopeMilestone_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	otherScopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	self := milestoneAuthoringTestSubject("agent-1")
	otherProduct, err := s.Products().Create(ctx, otherScopeID, "Krill", "another scope's product")
	require.NoError(t, err)
	foreign, err := s.MilestoneAuthoring().CreateMilestone(ctx, otherScopeID, otherProduct.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	err = s.Milestones().AddAssociation(ctx, scopeID, uuid.New(), foreign.ID)
	require.Error(t, err, "a milestone from another scope is not a valid parent here")
	assert.ErrorIs(t, err, store.ErrNotFound)

	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_milestone WHERE milestone_id = $1
	`, foreign.ID).Scan(&n))
	assert.Zero(t, n, "a refused cross-scope AddAssociation writes no entity_milestone row")
}

// TestDeliveryShipmentStore_MarkShipped_CrossScopeMilestone_Refused: the
// same scope term on the shipment side. A milestone that exists, but in
// another scope, must be refused and no delivery_shipment row written.
func TestDeliveryShipmentStore_MarkShipped_CrossScopeMilestone_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	otherScopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	self := milestoneAuthoringTestSubject("agent-1")
	otherProduct, err := s.Products().Create(ctx, otherScopeID, "Krill", "another scope's product")
	require.NoError(t, err)
	foreign, err := s.MilestoneAuthoring().CreateMilestone(ctx, otherScopeID, otherProduct.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	err = s.DeliveryShipments().MarkShipped(ctx, scopeID, foreign.ID, uuid.New(), nil, self, self)
	require.Error(t, err, "a milestone from another scope must not accept a shipment")
	assert.ErrorIs(t, err, store.ErrNotFound)

	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM delivery_shipment WHERE milestone_id = $1
	`, foreign.ID).Scan(&n))
	assert.Zero(t, n, "a refused cross-scope MarkShipped writes no delivery_shipment row")
}

// TestMilestoneStore_AddAssociation_CompetingMilestone_Refused: the importer
// is a Delivers write path like any other, so it owes the same
// single-delivery-parent rule add_delivers enforces (LB6). Without the
// guard, re-importing a brief that names the same entity under two
// milestones would silently give the entity two milestone-level owners --
// the exact state the authoring path refuses.
func TestMilestoneStore_AddAssociation_CompetingMilestone_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	m1, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	m2, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M2", "", nil, self, self)
	require.NoError(t, err)

	entityID := uuid.New()
	require.NoError(t, s.Milestones().AddAssociation(ctx, scopeID, entityID, m1.ID))

	// Re-importing M1's OWN association is not a conflict -- a brief
	// re-imported unchanged must stay idempotent.
	require.NoError(t, s.Milestones().AddAssociation(ctx, scopeID, entityID, m1.ID),
		"re-asserting the same milestone's own association stays idempotent")

	err = s.Milestones().AddAssociation(ctx, scopeID, entityID, m2.ID)
	require.Error(t, err, "an entity already delivered by M1 must not be delivered by M2")
	assert.ErrorIs(t, err, store.ErrEntityDeliveredByCompetingMilestone)
	assert.Contains(t, err.Error(), "move_delivery_scope", "the refusal must name the re-cut that does work")

	var n int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM entity_milestone WHERE entity_id = $1 AND relation = $2
	`, entityID, string(store.MilestoneRelationDelivers)).Scan(&n))
	assert.Equal(t, 1, n, "the refused association is written nowhere -- the entity still has exactly one milestone-level owner")

	// The re-cut the refusal names still resolves it, so the advice the
	// importer gives is advice that works.
	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{entityID}, m1.ID, m2.ID, self, self))
	require.NoError(t, s.Milestones().AddAssociation(ctx, scopeID, entityID, m2.ID),
		"after the re-cut, the importer may assert M2's association")
}
