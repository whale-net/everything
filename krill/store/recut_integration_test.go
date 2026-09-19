//go:build integration

// Real-Postgres coverage for RecutStore (recut.go, migration 014, issue
// #2687's Testing section, FR5): GetOrCreateBacklog's idempotency and
// per-product isolation (item 5), MoveScope's happy paths --
// milestone->milestone with NFR1's identity-stability guarantee (item 1),
// milestone->backlog round-tripping via ListBacklog, milestone->milepebble
// and milepebble->milepebble both landing the FR3 subset invariant (items
// 2 and 6), NFR3's all-or-nothing rejection of a batch naming an
// already-shipped entity with nothing written (item 3), a half-shipped
// container's unshipped half re-cutting cleanly while the shipped half's
// associations and delivery_shipment row stay byte-identical (item 4),
// MoveScope's ErrEntityNotInContainer rejection, the FR3 subset
// invariant's own rejection for a non-sibling source, and the "drop from
// every milepebble cut from a milestone" behavior MoveScope's own doc
// comment chooses for moving scope out of that milestone. Shares
// milestone_authoring_integration_test.go's test-store/test-scope/subject
// helpers (same package, same build tag) rather than duplicating them,
// mirroring milepebble_integration_test.go's own choice.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:recut_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// deliversAssociation reports whether entityID currently carries a
// relation='delivers' entity_milestone row against containerID -- shared
// shorthand for the many before/after assertions in this file, mirroring
// the inline `SELECT EXISTS` queries milestone_authoring_integration_test.go
// and milepebble_integration_test.go already repeat throughout this
// package's other test files.
func deliversAssociation(t *testing.T, ctx context.Context, db *dbtest.Postgres, entityID, containerID uuid.UUID) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM entity_milestone
			WHERE entity_id = $1 AND milestone_id = $2 AND relation = 'delivers'
		)
	`, entityID, containerID).Scan(&exists))
	return exists
}

// TestRecutStore_GetOrCreateBacklog_IdempotentPerProduct is issue #2687's
// Testing item 5: two calls for the same product resolve to the same
// backlog row, and two different products resolve to two different rows
// -- exactly one backlog row per product, never one per call.
func TestRecutStore_GetOrCreateBacklog_IdempotentPerProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	productA, err := s.Products().Create(ctx, scopeID, "Product A", "vision A")
	require.NoError(t, err)
	productB, err := s.Products().Create(ctx, scopeID, "Product B", "vision B")
	require.NoError(t, err)

	self := milestoneAuthoringTestSubject("agent-1")

	backlogA1, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, productA.ID, self, self)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneKindBacklog, backlogA1.Kind)
	assert.Nil(t, backlogA1.ParentMilestoneID, "a backlog row has no parent -- it sits directly under a product, like a milestone")

	backlogA2, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, productA.ID, self, self)
	require.NoError(t, err)
	assert.Equal(t, backlogA1.ID, backlogA2.ID, "two calls for the same product must resolve to the same backlog row")

	backlogB, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, productB.ID, self, self)
	require.NoError(t, err)
	assert.NotEqual(t, backlogA1.ID, backlogB.ID, "two different products must resolve to two different backlog buckets")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_ref WHERE kind = 'backlog'`).Scan(&count))
	assert.Equal(t, 2, count, "exactly one backlog row per product must exist, not one per GetOrCreateBacklog call")
}

// TestRecutStore_GetOrCreateBacklog_UnknownProduct_ReturnsErrNotFound
// proves LB2 parentage: GetOrCreateBacklog rejects a product_id with no
// current `product` row and inserts no row.
func TestRecutStore_GetOrCreateBacklog_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)

	self := milestoneAuthoringTestSubject("agent-1")
	_, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, uuid.New(), self, self)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_ref WHERE kind = 'backlog'`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected GetOrCreateBacklog must insert no row")
}

// TestRecutStore_MoveScope_MilestoneToMilestone_NFR1IdentityUnchanged is
// issue #2687's Testing item 1: moving an unshipped feature from milestone
// A to milestone B makes it deliver B and no longer deliver A, and the
// entity's own id is untouched (NFR1).
func TestRecutStore_MoveScope_MilestoneToMilestone_NFR1IdentityUnchanged(t *testing.T) {
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
	capturedID := feature.ID

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{feature.ID}, milestoneA.ID, milestoneB.ID, self, self))

	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milestoneB.ID), "the feature must deliver milestoneB after the move")
	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milestoneA.ID), "the feature must no longer deliver milestoneA after the move")

	gotFeature, err := s.Features().GetCurrentByID(ctx, capturedID)
	require.NoError(t, err)
	assert.Equal(t, capturedID, gotFeature.ID, "NFR1: MoveScope must never change an entity's own id")
}

// TestRecutStore_MoveScope_MilestoneToBacklog_RoundTripsViaListBacklog is
// issue #2687's Testing item 2's backlog half: moving an item to the
// backlog bucket works, and it round-trips via ListBacklog.
func TestRecutStore_MoveScope_MilestoneToBacklog_RoundTripsViaListBacklog(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	self := milestoneAuthoringTestSubject("agent-1")

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	backlog, err := s.Recut().GetOrCreateBacklog(ctx, scopeID, product.ID, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{feature.ID}, milestone.ID, backlog.ID, self, self))

	ids, err := s.Recut().ListBacklog(ctx, scopeID, product.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{feature.ID}, ids)

	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milestone.ID), "the feature must no longer deliver the milestone once it is in the backlog bucket")
}

// TestRecutStore_MoveScope_MilestoneToMilepebble_SubsetInvariantHolds is
// issue #2687's Testing items 2 and 6: cutting scope from a milestone
// directly into one of its own milepebbles succeeds, and afterward the
// FR3 subset invariant holds -- the entity delivers both the milepebble
// and its parent milestone.
func TestRecutStore_MoveScope_MilestoneToMilepebble_SubsetInvariantHolds(t *testing.T) {
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
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{feature.ID}, milestone.ID, milepebble.ID, self, self))

	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milepebble.ID), "the feature must deliver the milepebble after the move")
	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milestone.ID), "FR3 subset invariant: the parent milestone must still deliver the feature after the move")
}

// TestRecutStore_MoveScope_MilepebbleToMilepebbleSiblings_SubsetInvariantHolds
// is issue #2687's Testing items 2 and 6: moving an item between two
// milepebbles cut from the same parent milestone succeeds, and the FR3
// subset invariant holds afterward -- the parent milestone's own Delivers
// association is left untouched by a sibling-to-sibling move.
func TestRecutStore_MoveScope_MilepebbleToMilepebbleSiblings_SubsetInvariantHolds(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	self := milestoneAuthoringTestSubject("agent-1")

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	milepebble1, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", self, self)
	require.NoError(t, err)
	milepebble2, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 2", "", self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble1.ID, feature.ID, self, self))

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{feature.ID}, milepebble1.ID, milepebble2.ID, self, self))

	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milepebble2.ID), "the feature must deliver milepebble2 after the sibling move")
	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milepebble1.ID), "the feature must no longer deliver milepebble1 after the sibling move")
	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milestone.ID), "FR3 subset invariant: the shared parent milestone must still deliver the feature, untouched by a sibling-to-sibling move")
}

// TestRecutStore_MoveScope_IntoMilepebble_NonSiblingSourceNotInParentDelivers_Rejected
// is issue #2687's Testing item 6's rejection half: moving an item into a
// milepebble whose parent does not yet deliver it is rejected
// (ErrMilepebbleDeliversNotSubset) when the source is not a sibling
// milepebble of that same parent, and nothing is written.
func TestRecutStore_MoveScope_IntoMilepebble_NonSiblingSourceNotInParentDelivers_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	self := milestoneAuthoringTestSubject("agent-1")

	milestoneA, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "MA", "", nil, self, self)
	require.NoError(t, err)
	milepebbleA1, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestoneA.ID, "cut 1", "", self, self)
	require.NoError(t, err)
	milestoneB, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "MB", "", nil, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	// feature delivers milestoneB only -- never milestoneA, so it is not in
	// milepebbleA1's parent's Delivers set.
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneB.ID, feature.ID, self, self))

	err = s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{feature.ID}, milestoneB.ID, milepebbleA1.ID, self, self)
	assert.ErrorIs(t, err, store.ErrMilepebbleDeliversNotSubset)

	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milestoneB.ID), "a rejected move must leave the feature's existing association untouched")
	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milepebbleA1.ID), "a rejected move must write no association into the target milepebble")
}

// TestRecutStore_MoveScope_RejectsEntityNotInFromContainer is issue
// #2687's Testing "Also cover" item: MoveScope rejects an entity that is
// not currently a Delivers association of the from container
// (ErrEntityNotInContainer) and writes nothing.
func TestRecutStore_MoveScope_RejectsEntityNotInFromContainer(t *testing.T) {
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
	// feature is never associated to milestoneA at all.

	err = s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{feature.ID}, milestoneA.ID, milestoneB.ID, self, self)
	assert.ErrorIs(t, err, store.ErrEntityNotInContainer)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM entity_milestone WHERE entity_id = $1`, feature.ID).Scan(&count))
	assert.Equal(t, 0, count, "a rejected MoveScope must insert no association")
}

// TestRecutStore_MoveScope_RejectsShippedEntity_WritesNothing is issue
// #2687's Testing item 3 (NFR3): a batch naming an already-shipped entity
// is rejected in full (ErrEntityShipped) -- the unshipped entity in the
// same batch is not moved either, and the delivery_shipment row for the
// shipped entity is untouched.
func TestRecutStore_MoveScope_RejectsShippedEntity_WritesNothing(t *testing.T) {
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
	unshipped, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-unshipped", nil)
	require.NoError(t, err)
	shipped, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-shipped", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneA.ID, unshipped.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneA.ID, shipped.ID, self, self))
	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestoneA.ID, shipped.ID, nil, self, self))

	err = s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{unshipped.ID, shipped.ID}, milestoneA.ID, milestoneB.ID, self, self)
	assert.ErrorIs(t, err, store.ErrEntityShipped)

	assert.True(t, deliversAssociation(t, ctx, db, unshipped.ID, milestoneA.ID), "the unshipped entity in the same rejected batch must stay put too -- all-or-nothing")
	assert.False(t, deliversAssociation(t, ctx, db, unshipped.ID, milestoneB.ID), "the unshipped entity must not have moved to milestoneB")
	assert.True(t, deliversAssociation(t, ctx, db, shipped.ID, milestoneA.ID), "the shipped entity must stay put")
	assert.False(t, deliversAssociation(t, ctx, db, shipped.ID, milestoneB.ID))

	var shipmentCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2`, shipped.ID, milestoneA.ID).Scan(&shipmentCount))
	assert.Equal(t, 1, shipmentCount, "a rejected MoveScope must never touch the append-only delivery_shipment history (NFR3)")
}

// TestRecutStore_MoveScope_HalfShippedContainer_UnshippedHalfReCuts_ShippedHalfUntouched
// is issue #2687's Testing item 4: a container that is half shipped can
// still have its unshipped half re-cut, and the shipped half's
// association and delivery_shipment row are byte-identical before and
// after.
func TestRecutStore_MoveScope_HalfShippedContainer_UnshippedHalfReCuts_ShippedHalfUntouched(t *testing.T) {
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
	unshipped, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-unshipped", nil)
	require.NoError(t, err)
	shipped, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-shipped", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneA.ID, unshipped.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneA.ID, shipped.ID, self, self))
	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestoneA.ID, shipped.ID, nil, self, self))

	var shipmentIDBefore uuid.UUID
	var shipmentCreatedAtBefore any
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id, created_at FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2`, shipped.ID, milestoneA.ID).Scan(&shipmentIDBefore, &shipmentCreatedAtBefore))

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{unshipped.ID}, milestoneA.ID, milestoneB.ID, self, self))

	assert.True(t, deliversAssociation(t, ctx, db, unshipped.ID, milestoneB.ID), "the unshipped half must have moved")
	assert.False(t, deliversAssociation(t, ctx, db, unshipped.ID, milestoneA.ID))
	assert.True(t, deliversAssociation(t, ctx, db, shipped.ID, milestoneA.ID), "the shipped half's association must be untouched")
	assert.False(t, deliversAssociation(t, ctx, db, shipped.ID, milestoneB.ID))

	var shipmentIDAfter uuid.UUID
	var shipmentCreatedAtAfter any
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT id, created_at FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2`, shipped.ID, milestoneA.ID).Scan(&shipmentIDAfter, &shipmentCreatedAtAfter))
	assert.Equal(t, shipmentIDBefore, shipmentIDAfter, "the shipped half's delivery_shipment row must be byte-identical before and after (NFR3)")
	assert.Equal(t, shipmentCreatedAtBefore, shipmentCreatedAtAfter)
}

// TestRecutStore_MoveScope_OutOfMilestone_DropsMilepebbleAssociations is
// issue #2687's Testing "Also cover" item and MoveScope's own documented
// design choice: moving an item out of a milestone that has a milepebble
// delivering it also drops that milepebble's own Delivers association,
// rather than rejecting the move.
func TestRecutStore_MoveScope_OutOfMilestone_DropsMilepebbleAssociations(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	self := milestoneAuthoringTestSubject("agent-1")

	milestoneA, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "MA", "", nil, self, self)
	require.NoError(t, err)
	milepebble, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestoneA.ID, "cut 1", "", self, self)
	require.NoError(t, err)
	milestoneB, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "MB", "", nil, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestoneA.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, feature.ID, self, self))

	require.NoError(t, s.Recut().MoveScope(ctx, scopeID, []uuid.UUID{feature.ID}, milestoneA.ID, milestoneB.ID, self, self))

	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milestoneB.ID), "the feature must deliver milestoneB after the move")
	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milestoneA.ID), "the feature must no longer deliver milestoneA after the move")
	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milepebble.ID), "moving out of milestoneA must also drop the milepebble's own Delivers association -- a milepebble's set cannot outlive its parent's no-longer-true claim")
}
