//go:build integration

// Real-Postgres coverage for AbandonStore (abandon.go, issue #2688's
// Testing section, FR6): abandoning a milestone with a mix of shipped and
// unshipped delivered items (item 1), NFR3's byte-identical
// delivery_shipment rows before/after (item 2), FR12's end-to-end status
// history on the abandoned container (item 3), FR9's milepebble-direct
// abandon (item 4), the milestone->milepebble cascade this file's own
// package doc comment documents (item 5), atomicity via a cascade failure
// rolling back the parent's already-applied move and status writes (item
// 6), the already-abandoned rejection writing nothing (item 7), the
// backlog-bucket-itself rejection (item 8), and the post-abandon
// MoveScope recovery path out of the backlog (item 9). Shares
// milestone_authoring_integration_test.go's test-store/test-scope/subject
// helpers (same package, same build tag) rather than duplicating them,
// mirroring recut_integration_test.go's own choice.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:abandon_integration_test --test_output=all
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

// abandonTestFixture bundles a milestone with one shipped and one
// unshipped delivered feature -- the base scenario most of this file's
// tests build on.
type abandonTestFixture struct {
	s         *store.Store
	db        *dbtest.Postgres
	scopeID   uuid.UUID
	product   store.Product
	milestone store.MilestoneRef
	unshipped store.Feature
	shipped   store.Feature
	self      store.Subject
}

func newAbandonTestFixture(t *testing.T) abandonTestFixture {
	t.Helper()
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	self := milestoneAuthoringTestSubject("agent-1")

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "ship it", nil, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	unshipped, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-unshipped", nil)
	require.NoError(t, err)
	shipped, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F-shipped", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, unshipped.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, shipped.ID, self, self))
	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, shipped.ID, nil, self, self))

	return abandonTestFixture{
		s: s, db: db, scopeID: scopeID, product: product,
		milestone: milestone, unshipped: unshipped, shipped: shipped, self: self,
	}
}

// TestAbandonStore_Abandon_SweepsUnshippedKeepsShipped_SetsStatus is issue
// #2688's Testing item 1: abandoning a milestone with a mix of shipped and
// unshipped delivered items sets its status to `abandoned`, moves the
// unshipped item to the backlog bucket, and leaves the shipped item
// delivering the (now abandoned) milestone.
func TestAbandonStore_Abandon_SweepsUnshippedKeepsShipped_SetsStatus(t *testing.T) {
	ctx := context.Background()
	f := newAbandonTestFixture(t)

	result, err := f.s.Abandon().Abandon(ctx, f.scopeID, f.milestone.ID, nil, f.self, f.self)
	require.NoError(t, err)

	assert.Equal(t, store.MilestoneStatusAbandoned, result.StatusEvent.Status)
	assert.ElementsMatch(t, []uuid.UUID{f.unshipped.ID}, result.MovedToBacklogIDs)
	assert.ElementsMatch(t, []uuid.UUID{f.shipped.ID}, result.ShippedIDs)

	status, err := f.s.MilestoneStatus().CurrentStatus(ctx, f.milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusAbandoned, status)

	assert.True(t, deliversAssociation(t, ctx, f.db, f.shipped.ID, f.milestone.ID), "the shipped item must still deliver the abandoned milestone")
	assert.False(t, deliversAssociation(t, ctx, f.db, f.unshipped.ID, f.milestone.ID), "the unshipped item must no longer deliver the milestone")
	assert.True(t, deliversAssociation(t, ctx, f.db, f.unshipped.ID, result.BacklogID), "the unshipped item must now deliver the backlog bucket")

	backlogIDs, err := f.s.Recut().ListBacklog(ctx, f.scopeID, f.product.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{f.unshipped.ID}, backlogIDs)
}

// TestAbandonStore_Abandon_NFR3_DeliveryShipmentRowsByteIdentical is issue
// #2688's Testing item 2: abandoning a container never alters the
// `delivery_shipment` row for what it already shipped.
func TestAbandonStore_Abandon_NFR3_DeliveryShipmentRowsByteIdentical(t *testing.T) {
	ctx := context.Background()
	f := newAbandonTestFixture(t)

	var idBefore uuid.UUID
	var createdAtBefore any
	require.NoError(t, f.db.Pool.QueryRow(ctx, `
		SELECT id, created_at FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2
	`, f.shipped.ID, f.milestone.ID).Scan(&idBefore, &createdAtBefore))

	_, err := f.s.Abandon().Abandon(ctx, f.scopeID, f.milestone.ID, nil, f.self, f.self)
	require.NoError(t, err)

	var idAfter uuid.UUID
	var createdAtAfter any
	require.NoError(t, f.db.Pool.QueryRow(ctx, `
		SELECT id, created_at FROM delivery_shipment WHERE entity_id = $1 AND milestone_id = $2
	`, f.shipped.ID, f.milestone.ID).Scan(&idAfter, &createdAtAfter))

	assert.Equal(t, idBefore, idAfter, "NFR3: the delivery_shipment row's id must be byte-identical before and after an abandon")
	assert.Equal(t, createdAtBefore, createdAtAfter, "NFR3: the delivery_shipment row's created_at must be byte-identical before and after an abandon")

	var shipmentCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM delivery_shipment WHERE milestone_id = $1`, f.milestone.ID).Scan(&shipmentCount))
	assert.Equal(t, 1, shipmentCount, "abandon must never insert or remove a delivery_shipment row")
}

// TestAbandonStore_Abandon_FR12_HistoryReadsEndToEnd is issue #2688's
// Testing item 3: the abandoned container's status history reads
// planned -> in progress -> abandoned end-to-end, each entry with its own
// actor and timestamp.
func TestAbandonStore_Abandon_FR12_HistoryReadsEndToEnd(t *testing.T) {
	ctx := context.Background()
	f := newAbandonTestFixture(t)

	_, err := f.s.MilestoneStatus().RecordTransition(ctx, f.scopeID, f.milestone.ID, store.MilestoneStatusPlanned, nil, f.self, f.self)
	require.NoError(t, err)
	_, err = f.s.MilestoneStatus().RecordTransition(ctx, f.scopeID, f.milestone.ID, store.MilestoneStatusInProgress, nil, f.self, f.self)
	require.NoError(t, err)

	note := "stalled, sweeping remaining scope"
	result, err := f.s.Abandon().Abandon(ctx, f.scopeID, f.milestone.ID, &note, f.self, f.self)
	require.NoError(t, err)
	assert.Equal(t, &note, result.StatusEvent.Note)

	history, err := f.s.MilestoneStatus().ListTransitions(ctx, f.milestone.ID)
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Equal(t, store.MilestoneStatusPlanned, history[0].Status)
	assert.Equal(t, store.MilestoneStatusInProgress, history[1].Status)
	assert.Equal(t, store.MilestoneStatusAbandoned, history[2].Status)
	for _, e := range history {
		assert.Equal(t, f.self, e.CreatedByActing)
		assert.Equal(t, f.self, e.CreatedByOnBehalfOf)
		assert.False(t, e.CreatedAt.IsZero())
	}
}

// TestAbandonStore_Abandon_FR9_MilepebbleDirect works the same way against
// a milepebble's own narrower Delivers subset -- issue #2688's Testing
// item 4.
func TestAbandonStore_Abandon_FR9_MilepebbleDirect(t *testing.T) {
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
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, feature.ID, self, self))

	result, err := s.Abandon().Abandon(ctx, scopeID, milepebble.ID, nil, self, self)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusAbandoned, result.StatusEvent.Status)
	assert.ElementsMatch(t, []uuid.UUID{feature.ID}, result.MovedToBacklogIDs)
	assert.Empty(t, result.MilepebbleResults, "a milepebble target has no children to cascade into")

	status, err := s.MilestoneStatus().CurrentStatus(ctx, milepebble.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusAbandoned, status)

	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milepebble.ID), "the feature must no longer deliver the abandoned milepebble")
	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milestone.ID), "abandoning a milepebble alone must not touch its parent milestone's own Delivers association")

	parentStatus, err := s.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusNotStarted, parentStatus, "abandoning a milepebble alone must not cascade upward onto its parent milestone")
}

// TestAbandonStore_Abandon_CascadesToLiveMilepebbles is issue #2688's
// Testing item 5: abandoning a milestone with a live milepebble also
// abandons the milepebble, sweeping its own not-yet-shipped scope and
// appending its own `abandoned` transition row.
func TestAbandonStore_Abandon_CascadesToLiveMilepebbles(t *testing.T) {
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
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, milepebble.ID, feature.ID, self, self))

	result, err := s.Abandon().Abandon(ctx, scopeID, milestone.ID, nil, self, self)
	require.NoError(t, err)
	require.Len(t, result.MilepebbleResults, 1, "the milestone's one live milepebble must be cascaded")
	assert.Equal(t, milepebble.ID, result.MilepebbleResults[0].ContainerID)
	assert.Equal(t, store.MilestoneStatusAbandoned, result.MilepebbleResults[0].StatusEvent.Status)

	milepebbleStatus, err := s.MilestoneStatus().CurrentStatus(ctx, milepebble.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusAbandoned, milepebbleStatus, "cascade must append the milepebble's own abandoned transition")

	milestoneHistory, err := s.MilestoneStatus().ListTransitions(ctx, milestone.ID)
	require.NoError(t, err)
	require.Len(t, milestoneHistory, 1)
	milepebbleHistory, err := s.MilestoneStatus().ListTransitions(ctx, milepebble.ID)
	require.NoError(t, err)
	require.Len(t, milepebbleHistory, 1, "the milepebble's own history must carry its own row, not merely inherit the parent's")

	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milestone.ID))
	assert.False(t, deliversAssociation(t, ctx, db, feature.ID, milepebble.ID))
}

// TestAbandonStore_Abandon_CascadeFailure_RollsBackParentMoveAndStatus is
// issue #2688's Testing item 6 (atomicity): a milestone with one live and
// one already-abandoned milepebble fails the cascade step loudly
// (ErrAlreadyAbandoned) -- proving neither the parent's own already-applied
// move (step 4) nor its own already-applied status transition (step 5)
// survives the rollback, since both ran before the cascade step that fails.
func TestAbandonStore_Abandon_CascadeFailure_RollsBackParentMoveAndStatus(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "spec-of-record")
	require.NoError(t, err)
	self := milestoneAuthoringTestSubject("agent-1")

	milestone, err := s.MilestoneAuthoring().CreateMilestone(ctx, scopeID, product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)
	alreadyAbandoned, err := s.MilestoneAuthoring().CreateMilepebble(ctx, scopeID, milestone.ID, "cut 1", "", nil, self, self)
	require.NoError(t, err)
	featureSet, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	feature, err := s.Features().Create(ctx, scopeID, featureSet.ID, "F1", nil)
	require.NoError(t, err)
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, feature.ID, self, self))

	// Pre-abandon the milepebble directly, before it ever carried any
	// Delivers association (so it needs no sweep of its own) -- the
	// cascade step will still reject it on sight, purely on its status.
	_, err = s.Abandon().Abandon(ctx, scopeID, alreadyAbandoned.ID, nil, self, self)
	require.NoError(t, err)

	_, err = s.Abandon().Abandon(ctx, scopeID, milestone.ID, nil, self, self)
	require.ErrorIs(t, err, store.ErrAlreadyAbandoned)

	milestoneStatus, err := s.MilestoneStatus().CurrentStatus(ctx, milestone.ID)
	require.NoError(t, err)
	assert.Equal(t, store.MilestoneStatusNotStarted, milestoneStatus, "the parent's own status transition (step 5) must roll back when the cascade step (step 6) fails")

	assert.True(t, deliversAssociation(t, ctx, db, feature.ID, milestone.ID), "the parent's own move (step 4) must roll back too -- the feature must still deliver the milestone")

	var eventCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event WHERE milestone_id = $1`, milestone.ID).Scan(&eventCount))
	assert.Equal(t, 0, eventCount, "no status_event row for the parent milestone may survive a rolled-back cascade")
}

// TestAbandonStore_Abandon_AlreadyAbandoned_RejectsWritesNothing is issue
// #2688's Testing item 7: abandoning an already-abandoned container fails
// loudly and writes nothing -- the backlog bucket's contents are
// unchanged (no double sweep).
func TestAbandonStore_Abandon_AlreadyAbandoned_RejectsWritesNothing(t *testing.T) {
	ctx := context.Background()
	f := newAbandonTestFixture(t)

	_, err := f.s.Abandon().Abandon(ctx, f.scopeID, f.milestone.ID, nil, f.self, f.self)
	require.NoError(t, err)

	_, err = f.s.Abandon().Abandon(ctx, f.scopeID, f.milestone.ID, nil, f.self, f.self)
	assert.ErrorIs(t, err, store.ErrAlreadyAbandoned)

	var eventCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event WHERE milestone_id = $1`, f.milestone.ID).Scan(&eventCount))
	assert.Equal(t, 1, eventCount, "a rejected double-abandon must append no second status_event row")

	backlogIDs, err := f.s.Recut().ListBacklog(ctx, f.scopeID, f.product.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []uuid.UUID{f.unshipped.ID}, backlogIDs, "a rejected double-abandon must not sweep anything a second time")
}

// TestAbandonStore_Abandon_BacklogBucket_Rejected is issue #2688's Testing
// item 8: abandoning the backlog bucket itself is rejected.
func TestAbandonStore_Abandon_BacklogBucket_Rejected(t *testing.T) {
	ctx := context.Background()
	f := newAbandonTestFixture(t)

	backlog, err := f.s.Recut().GetOrCreateBacklog(ctx, f.scopeID, f.product.ID, f.self, f.self)
	require.NoError(t, err)

	_, err = f.s.Abandon().Abandon(ctx, f.scopeID, backlog.ID, nil, f.self, f.self)
	assert.ErrorIs(t, err, store.ErrCannotAbandonBacklog)

	var eventCount int
	require.NoError(t, f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM milestone_status_event WHERE milestone_id = $1`, backlog.ID).Scan(&eventCount))
	assert.Equal(t, 0, eventCount)
}

// TestAbandonStore_Abandon_PostAbandonScopeReCutsOutOfBacklog is issue
// #2688's Testing item 9: the documented recovery path -- scope an
// abandon swept into the backlog can be re-cut into a new milestone via
// RecutStore.MoveScope.
func TestAbandonStore_Abandon_PostAbandonScopeReCutsOutOfBacklog(t *testing.T) {
	ctx := context.Background()
	f := newAbandonTestFixture(t)

	result, err := f.s.Abandon().Abandon(ctx, f.scopeID, f.milestone.ID, nil, f.self, f.self)
	require.NoError(t, err)

	revived, err := f.s.MilestoneAuthoring().CreateMilestone(ctx, f.scopeID, f.product.ID, "M2", "revived commitment", nil, f.self, f.self)
	require.NoError(t, err)

	require.NoError(t, f.s.Recut().MoveScope(ctx, f.scopeID, []uuid.UUID{f.unshipped.ID}, result.BacklogID, revived.ID, f.self, f.self))

	assert.True(t, deliversAssociation(t, ctx, f.db, f.unshipped.ID, revived.ID))
	backlogIDs, err := f.s.Recut().ListBacklog(ctx, f.scopeID, f.product.ID)
	require.NoError(t, err)
	assert.Empty(t, backlogIDs)
}
