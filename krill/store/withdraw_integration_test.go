//go:build integration

// Store-level coverage for WithdrawalStore (withdraw.go). Shares the
// fixtures of delivery_parent_integration_test.go (same package and tag).
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

func countEdges(t *testing.T, ctx context.Context, db *dbtest.Postgres, milestoneID, entityID uuid.UUID, relation string) (total, active int) {
	t.Helper()
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE withdrawn_at IS NULL)
		FROM entity_milestone WHERE milestone_id = $1 AND entity_id = $2 AND relation = $3
	`, milestoneID, entityID, relation).Scan(&total, &active))
	return total, active
}

func TestWithdrawMustNotForeclose_StampsEdgeAndRecordsReason(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "v")
	require.NoError(t, err)
	fx := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	require.NoError(t, s.MilestoneAuthoring().AddMustNotForeclose(ctx, scopeID, fx.parent, fx.entity, self, self))
	require.NoError(t, s.Withdrawal().WithdrawMustNotForeclose(ctx, scopeID, fx.parent, fx.entity, "wrong edge", self, self))

	total, active := countEdges(t, ctx, db, fx.parent, fx.entity, "must_not_foreclose")
	assert.Equal(t, 1, total, "row kept")
	assert.Equal(t, 0, active)
	var reason string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT withdrawal_reason FROM entity_milestone WHERE milestone_id=$1 AND entity_id=$2 AND relation='must_not_foreclose'`, fx.parent, fx.entity).Scan(&reason))
	assert.Equal(t, "wrong edge", reason)
}

func TestWithdrawDelivers_MilepebbleEdge(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "v")
	require.NoError(t, err)
	fx := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	require.NoError(t, s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.cut1, fx.entity, "", self, self))
	_, active := countEdges(t, ctx, db, fx.cut1, fx.entity, "delivers")
	assert.Equal(t, 0, active)
}

func TestWithdrawDelivers_Shipped_Refused(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "v")
	require.NoError(t, err)
	fx := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	require.NoError(t, s.DeliveryShipments().MarkShipped(ctx, scopeID, fx.cut1, fx.entity, nil, self, self))
	err = s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.cut1, fx.entity, "x", self, self)
	require.ErrorIs(t, err, store.ErrWithdrawShipped)
	_, active := countEdges(t, ctx, db, fx.cut1, fx.entity, "delivers")
	assert.Equal(t, 1, active)
}

func TestWithdrawDelivers_MilestoneWithActiveMilepebbleEdge_NamesMilepebble(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "v")
	require.NoError(t, err)
	fx := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	err = s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.parent, fx.entity, "x", self, self)
	require.ErrorIs(t, err, store.ErrWithdrawMilepebbles)
	assert.Contains(t, err.Error(), fx.cut1.String())
	_, active := countEdges(t, ctx, db, fx.parent, fx.entity, "delivers")
	assert.Equal(t, 1, active)

	// Once the milepebble edge is withdrawn the milestone edge can go.
	require.NoError(t, s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.cut1, fx.entity, "x", self, self))
	require.NoError(t, s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.parent, fx.entity, "x", self, self))
}

func TestWithdraw_AbsentOrAlreadyWithdrawn_ErrorsNamingIds(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "v")
	require.NoError(t, err)
	fx := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	err = s.Withdrawal().WithdrawMustNotForeclose(ctx, scopeID, fx.parent, fx.entity, "x", self, self)
	require.ErrorIs(t, err, store.ErrEdgeNotActive)
	assert.Contains(t, err.Error(), fx.parent.String())
	assert.Contains(t, err.Error(), fx.entity.String())

	require.NoError(t, s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.cut1, fx.entity, "x", self, self))
	err = s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.cut1, fx.entity, "x", self, self)
	require.ErrorIs(t, err, store.ErrEdgeNotActive)
	assert.Contains(t, err.Error(), fx.cut1.String())
	assert.Contains(t, err.Error(), fx.entity.String())
	total, _ := countEdges(t, ctx, db, fx.cut1, fx.entity, "delivers")
	assert.Equal(t, 1, total, "second withdraw wrote nothing")
}

func TestWithdrawnDelivers_NoLongerBlocksAddDeliversElsewhere(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "v")
	require.NoError(t, err)
	fx := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	err = s.MilestoneAuthoring().AddDelivers(ctx, scopeID, fx.competing, fx.entity, self, self)
	require.ErrorIs(t, err, store.ErrEntityDeliveredByCompetingMilestone)

	require.NoError(t, s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.cut1, fx.entity, "x", self, self))
	require.NoError(t, s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.parent, fx.entity, "x", self, self))
	require.NoError(t, s.MilestoneAuthoring().AddDelivers(ctx, scopeID, fx.competing, fx.entity, self, self))
}

func TestReAddAfterWithdraw_CreatesFreshActiveRow_KeepsOld(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneAuthoringTestStore(t)
	scopeID := newMilestoneAuthoringTestScope(t, ctx, db)
	self := milestoneAuthoringTestSubject("agent-1")
	product, err := s.Products().Create(ctx, scopeID, "Krill", "v")
	require.NoError(t, err)
	fx := newMilepebbleParentFixture(t, ctx, s, scopeID, product.ID)

	require.NoError(t, s.Withdrawal().WithdrawDelivers(ctx, scopeID, fx.cut1, fx.entity, "x", self, self))
	require.NoError(t, s.MilestoneAuthoring().AddMilepebbleDelivers(ctx, scopeID, fx.cut1, fx.entity, self, self))
	total, active := countEdges(t, ctx, db, fx.cut1, fx.entity, "delivers")
	assert.Equal(t, 2, total, "withdrawn row kept")
	assert.Equal(t, 1, active)
}
