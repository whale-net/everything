//go:build integration

// Real-Postgres coverage for DeliveryShipmentStore (delivery_shipment.go,
// migration 013, issue #2686's Testing section, FR10): MarkShipped's
// happy path and its ErrEntityNotDelivered rejection (item 2, writing
// nothing on rejection), ShippedEntityIDs' dedup behavior,
// DeliveryBreakdown's shipped/unshipped partition against a real
// milestone with a mix of shipped/unshipped delivers (item 1's store
// half -- see krill/slice's own integration test for the typed-entity
// half, LB7), a zero-delivers container returning two empty slices
// rather than an error (item 7), shipped-ness keyed per-(entity,
// container) rather than per-entity (item 6), and the same breakdown
// scoped correctly to a milepebble's own narrower delivers subset (item
// 4, FR9). See delivery_shipment_nfr2_test.go for item 3's structural
// append-only guarantee. Shares milestone_authoring_integration_test.go's
// test-store/test-scope/subject helpers (same package, same build tag)
// rather than duplicating them, mirroring milestone_status_integration_test.go's
// own choice.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:delivery_shipment_integration_test --test_output=all
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

// TestDeliveryShipmentStore_MarkShipped_HappyPath is issue #2686's Testing
// "Also cover" item: MarkShipped against a real delivers association
// succeeds, and the entity reads back as shipped via both
// ShippedEntityIDs and DeliveryBreakdown.
func TestDeliveryShipmentStore_MarkShipped_HappyPath(t *testing.T) {
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

	note := "shipped in v1.2"
	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, feature.ID, &note, self, self))

	shipped, err := s.DeliveryShipments().ShippedEntityIDs(ctx, milestone.ID)
	require.NoError(t, err)
	assert.True(t, shipped[feature.ID], "feature must report shipped after MarkShipped")

	shippedIDs, unshippedIDs, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{feature.ID}, shippedIDs)
	assert.Empty(t, unshippedIDs)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2`, feature.ID, milestone.ID).Scan(&count))
	assert.Equal(t, 1, count)
}

// TestDeliveryShipmentStore_MarkShipped_RejectsEntityNotDelivered_WritesNothing
// is issue #2686's Testing item 2: marking an entity shipped that the
// container does not deliver fails loudly (ErrEntityNotDelivered), and
// no delivery_shipment row is written for the rejected call.
func TestDeliveryShipmentStore_MarkShipped_RejectsEntityNotDelivered_WritesNothing(t *testing.T) {
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
	// notDelivered is never associated to milestone as a `delivers` --
	// exactly the case MarkShipped must reject.
	notDelivered, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-not-delivered", nil)
	require.NoError(t, err)

	err = s.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, notDelivered.ID, nil, self, self)
	require.ErrorIs(t, err, store.ErrEntityNotDelivered)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM delivery_shipment`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected MarkShipped call must write no delivery_shipment row at all")

	shippedIDs, unshippedIDs, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Empty(t, shippedIDs)
	assert.Empty(t, unshippedIDs, "milestone delivers nothing, so the rejected entity must not leak into the breakdown either")
}

// TestDeliveryShipmentStore_ShippedEntityIDs_DedupsRepeatedMarks is issue
// #2686's Testing "Also cover" item: MarkShipped-ing the same
// (entity, milestone) pair twice appends a second row (NFR2/NFR3), but
// ShippedEntityIDs still reports exactly one entity id -- its DISTINCT
// read dedups the two rows.
func TestDeliveryShipmentStore_ShippedEntityIDs_DedupsRepeatedMarks(t *testing.T) {
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
	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, feature.ID, nil, self, self))

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2`, feature.ID, milestone.ID).Scan(&count))
	assert.Equal(t, 2, count, "a second MarkShipped call for the same pair must append a second row, not collapse or reject as a duplicate")

	shipped, err := s.DeliveryShipments().ShippedEntityIDs(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Len(t, shipped, 1, "ShippedEntityIDs must dedup the two rows into one entity id")
	assert.True(t, shipped[feature.ID])
}

// TestDeliveryShipmentStore_DeliveryBreakdown_PartitionsShippedAndUnshipped
// is issue #2686's Testing item 1's store half: a milestone delivering
// four features with two marked shipped reports exactly those two as
// shipped and the other two as unshipped.
func TestDeliveryShipmentStore_DeliveryBreakdown_PartitionsShippedAndUnshipped(t *testing.T) {
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

	var features []store.Feature
	for i := 0; i < 4; i++ {
		f, err := s.Features().Create(ctx, scopeID, featureSet.ID, uuid.NewString(), nil)
		require.NoError(t, err)
		require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, f.ID, self, self))
		features = append(features, f)
	}

	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, features[0].ID, nil, self, self))
	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, features[2].ID, nil, self, self))

	shippedIDs, unshippedIDs, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{features[0].ID, features[2].ID}, shippedIDs)
	assert.ElementsMatch(t, []uuid.UUID{features[1].ID, features[3].ID}, unshippedIDs)
}

// TestDeliveryShipmentStore_DeliveryBreakdown_ZeroDelivers_ReturnsTwoEmptySlices
// is issue #2686's Testing item 7: a container with zero `delivers`
// associations returns two empty slices, never an error.
func TestDeliveryShipmentStore_DeliveryBreakdown_ZeroDelivers_ReturnsTwoEmptySlices(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	shippedIDs, unshippedIDs, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Empty(t, shippedIDs)
	assert.Empty(t, unshippedIDs)
}

// TestDeliveryShipmentStore_ShippedPerEntityContainerPair_NotPerEntity is
// issue #2686's Testing item 6: the same Feature delivered by two
// containers, shipped in only one, reports shipped only in that one --
// shipped-ness hangs off the (entity, container) association, never off
// the spec entity itself.
func TestDeliveryShipmentStore_ShippedPerEntityContainerPair_NotPerEntity(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestoneA, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "MA", "", nil, self, self)
	require.NoError(t, err)
	milestoneB, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "MB", "", nil, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)

	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneA.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneB.ID, feature.ID, self, self))

	// Ship the feature against milestoneA only.
	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestoneA.ID, feature.ID, nil, self, self))

	shippedA, unshippedA, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milestoneA.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{feature.ID}, shippedA)
	assert.Empty(t, unshippedA)

	shippedB, unshippedB, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milestoneB.ID)
	require.NoError(t, err)
	assert.Empty(t, shippedB, "the same Feature must not report shipped against milestoneB just because it shipped against milestoneA")
	assert.ElementsMatch(t, []uuid.UUID{feature.ID}, unshippedB)
}

// TestDeliveryShipmentStore_Milepebble_ScopedToOwnDeliversSubset is issue
// #2686's Testing item 4 (FR9): the breakdown for a milepebble is scoped
// to its own (narrower) delivers subset, not the parent milestone's full
// delivered set.
func TestDeliveryShipmentStore_Milepebble_ScopedToOwnDeliversSubset(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")
	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", self, self)
	require.NoError(t, err)

	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	inCut, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-in-cut", nil)
	require.NoError(t, err)
	outOfCut, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-out-of-cut", nil)
	require.NoError(t, err)

	// Both features are delivered by the parent milestone, but only inCut
	// is delivered by the milepebble.
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, inCut.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, outOfCut.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, inCut.ID, self, self))

	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milepebble.ID, inCut.ID, nil, self, self))

	shippedPebble, unshippedPebble, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milepebble.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{inCut.ID}, shippedPebble)
	assert.Empty(t, unshippedPebble, "the milepebble's own breakdown must never include outOfCut -- it is not one of the milepebble's own delivers associations")

	// The parent milestone's own breakdown still sees both features, and
	// marking shipped against the milepebble does not shipped-mark the
	// parent milestone's own (entity, milestone) association.
	shippedMilestone, unshippedMilestone, err := s.DeliveryShipments().DeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Empty(t, shippedMilestone, "shipping inCut against the milepebble must not shipped-mark it against the parent milestone -- shipped-ness is per (entity, container)")
	assert.ElementsMatch(t, []uuid.UUID{inCut.ID, outOfCut.ID}, unshippedMilestone)
}
