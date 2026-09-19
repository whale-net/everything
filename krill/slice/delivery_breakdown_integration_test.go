//go:build integration

// Real-Postgres integration coverage for GetDeliveryBreakdown (issue
// #2686, krill M3, FR10). Follows query_integration_test.go's harness
// (newTestStore/createScope/seedWorld/world) exactly -- see that file's
// own doc comment for the dbtest/migration/seeding pattern this reuses
// rather than duplicates.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/slice:delivery_breakdown_integration_test --test_output=all
package slice_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// TestGetDeliveryBreakdown_ResolvesShippedAndUnshippedToTypedEntities is
// issue #2686's Testing item 1 (LB7): a milestone delivering FeatureA1
// (shipped) and RequirementA1FR (not shipped) reports each id resolved
// to its own typed entity -- a FeatureEntity and a RequirementEntity,
// never a bare id list -- via the same GetEntitySetSlice granularity
// every other read in this package uses.
func TestGetDeliveryBreakdown_ResolvesShippedAndUnshippedToTypedEntities(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-delivery-breakdown-fr10-test")
	w := seedWorld(t, ctx, entities, scopeID)

	self := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, w.FeatureA1.ID, self, self))
	require.NoError(t, entities.MilestoneAuthoring().AddDelivers(ctx, scopeID, milestone.ID, w.RequirementA1FR.ID, self, self))
	require.NoError(t, entities.DeliveryShipments().MarkShipped(ctx, scopeID, milestone.ID, w.FeatureA1.ID, nil, self, self))

	q := slice.NewQuerier(entities)
	shipped, unshipped, err := q.GetDeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)

	require.Len(t, shipped.Features, 1, "the shipped Document must resolve the shipped id to its full typed FeatureEntity, not a bare id")
	assert.Equal(t, w.FeatureA1.ID, shipped.Features[0].ID)
	assert.Empty(t, shipped.Requirements, "the shipped Document must not carry the unshipped requirement")

	require.Len(t, unshipped.Requirements, 1, "the unshipped Document must resolve the unshipped id to its full typed RequirementEntity, not a bare id")
	assert.Equal(t, w.RequirementA1FR.ID, unshipped.Requirements[0].ID)
	assert.Empty(t, unshipped.Features, "the unshipped Document must not carry the shipped feature")

	assert.Equal(t, slice.SchemaVersion, shipped.SchemaVersion)
	assert.Equal(t, slice.SchemaVersion, unshipped.SchemaVersion)
}

// TestGetDeliveryBreakdown_ZeroDelivers_ReturnsTwoEmptyDocuments is issue
// #2686's Testing item 7 at the slice layer: a container with zero
// `delivers` associations returns two empty Documents, never an error --
// DeliveryBreakdown's own empty-input contract (krill/store), unchanged
// by GetEntitySetSlice's own resolution step.
func TestGetDeliveryBreakdown_ZeroDelivers_ReturnsTwoEmptyDocuments(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-delivery-breakdown-fr10-empty-test")
	w := seedWorld(t, ctx, entities, scopeID)

	self := store.Subject{Iss: "https://keycloak.example.test/realms/humans", Sub: "human-1", Kind: store.SubjectKindHuman}
	milestone, err := entities.MilestoneAuthoring().CreateMilestone(ctx, scopeID, w.Product.ID, "M1", "", nil, self, self)
	require.NoError(t, err)

	q := slice.NewQuerier(entities)
	shipped, unshipped, err := q.GetDeliveryBreakdown(ctx, milestone.ID)
	require.NoError(t, err)

	assert.Equal(t, slice.SchemaVersion, shipped.SchemaVersion)
	assert.Empty(t, shipped.Features)
	assert.Empty(t, shipped.Requirements)
	assert.Equal(t, slice.SchemaVersion, unshipped.SchemaVersion)
	assert.Empty(t, unshipped.Features)
	assert.Empty(t, unshipped.Requirements)
}
