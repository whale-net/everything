//go:build integration

// Real-Postgres integration coverage for GetEntitySetSlice (issue #2544,
// krill M2, FR5: the fifth slice.Querier granularity). Follows
// query_integration_test.go's harness (newTestStore/createScope/
// seedWorld/world) exactly -- see that file's own doc comment for the
// dbtest/migration/seeding pattern this reuses rather than duplicates.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/slice:entity_set_query_integration_test --test_output=all
package slice_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
)

// TestGetEntitySetSlice_AssemblesHeterogeneousSet is this issue's Testing
// bullet 1: an id set spanning a FeatureSet, a Feature, a Requirement, and
// a LoadBearingDecision assembles one Document carrying all four, each
// with a correct EntityRef.
func TestGetEntitySetSlice_AssemblesHeterogeneousSet(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-entity-set-fr5-test")
	w := seedWorld(t, ctx, entities, scopeID)

	q := slice.NewQuerier(entities)
	doc, err := q.GetEntitySetSlice(ctx, []uuid.UUID{
		w.FeatureSetA.ID,
		w.FeatureA1.ID,
		w.RequirementA1FR.ID,
		w.DecisionA.ID,
	})
	require.NoError(t, err)

	require.Len(t, doc.FeatureSets, 1)
	assert.Equal(t, w.FeatureSetA.ID, doc.FeatureSets[0].ID)
	assert.Equal(t, w.FeatureSetA.RevisionID, doc.FeatureSets[0].RevisionID)

	require.Len(t, doc.Features, 1)
	assert.Equal(t, w.FeatureA1.ID, doc.Features[0].ID)
	assert.Equal(t, w.FeatureA1.RevisionID, doc.Features[0].RevisionID)

	require.Len(t, doc.Requirements, 1)
	assert.Equal(t, w.RequirementA1FR.ID, doc.Requirements[0].ID)
	assert.Equal(t, w.RequirementA1FR.RevisionID, doc.Requirements[0].RevisionID)

	require.Len(t, doc.Decisions, 1)
	assert.Equal(t, w.DecisionA.ID, doc.Decisions[0].ID)
	assert.Equal(t, w.DecisionA.RevisionID, doc.Decisions[0].RevisionID)

	assert.Nil(t, doc.Product, "GetEntitySetSlice never populates Product")
}

// TestGetEntitySetSlice_SchemaVersionMatchesOtherGranularities is bullet
// 2: the shape does not vary by granularity (LB7) -- SchemaVersion here
// must be identical to what GetProductSlice returns for the same store.
func TestGetEntitySetSlice_SchemaVersionMatchesOtherGranularities(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-entity-set-fr5-schema-test")
	w := seedWorld(t, ctx, entities, scopeID)

	q := slice.NewQuerier(entities)

	productDoc, err := q.GetProductSlice(ctx, w.Product.ID)
	require.NoError(t, err)

	entitySetDoc, err := q.GetEntitySetSlice(ctx, []uuid.UUID{w.FeatureA1.ID})
	require.NoError(t, err)

	assert.Equal(t, slice.SchemaVersion, entitySetDoc.SchemaVersion)
	assert.Equal(t, productDoc.SchemaVersion, entitySetDoc.SchemaVersion)
}

// TestGetEntitySetSlice_UnknownIDIsSkippedNotError is bullet 3: an unknown
// id in the set is skipped, the other entities still come back, no error.
func TestGetEntitySetSlice_UnknownIDIsSkippedNotError(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-entity-set-fr5-unknown-test")
	w := seedWorld(t, ctx, entities, scopeID)

	q := slice.NewQuerier(entities)
	unknown := uuid.New()

	doc, err := q.GetEntitySetSlice(ctx, []uuid.UUID{w.FeatureA1.ID, unknown})
	require.NoError(t, err)

	require.Len(t, doc.Features, 1)
	assert.Equal(t, w.FeatureA1.ID, doc.Features[0].ID)
}

// TestGetEntitySetSlice_EmptyInput_ReturnsEmptyDocument is bullet 4: empty
// input returns an empty Document with SchemaVersion populated, no error.
func TestGetEntitySetSlice_EmptyInput_ReturnsEmptyDocument(t *testing.T) {
	ctx := context.Background()
	entities, _ := newTestStore(t)

	q := slice.NewQuerier(entities)
	doc, err := q.GetEntitySetSlice(ctx, nil)
	require.NoError(t, err)

	assert.Equal(t, slice.SchemaVersion, doc.SchemaVersion)
	assert.Empty(t, doc.FeatureSets)
	assert.Empty(t, doc.Features)
	assert.Empty(t, doc.Requirements)
	assert.Empty(t, doc.Decisions)
	assert.Nil(t, doc.Product)
}

// TestGetEntitySetSlice_LiveStateNotReplay is bullet 5, the test that pins
// FR5's central claim: re-querying an id set after Amend must return the
// amended contents and the new RevisionID, never the revision that was
// current when the id was first recorded -- proving this method reads
// current rows, not a replay through HistoryStore.
func TestGetEntitySetSlice_LiveStateNotReplay(t *testing.T) {
	ctx := context.Background()
	entities, pool := newTestStore(t)
	scopeID := createScope(t, ctx, pool, "whale-net/slice-entity-set-fr5-live-state-test")
	w := seedWorld(t, ctx, entities, scopeID)

	q := slice.NewQuerier(entities)

	beforeAmend, err := q.GetEntitySetSlice(ctx, []uuid.UUID{w.RequirementA1FR.ID})
	require.NoError(t, err)
	require.Len(t, beforeAmend.Requirements, 1)
	assert.Equal(t, "FR1", beforeAmend.Requirements[0].Name)
	assert.Equal(t, w.RequirementA1FR.RevisionID, beforeAmend.Requirements[0].RevisionID)

	amended, err := entities.Amend().AmendRequirement(ctx, w.RequirementA1FR.ID, "FR1-amended", strPtr("do the amended thing"))
	require.NoError(t, err)

	afterAmend, err := q.GetEntitySetSlice(ctx, []uuid.UUID{w.RequirementA1FR.ID})
	require.NoError(t, err)
	require.Len(t, afterAmend.Requirements, 1)
	assert.Equal(t, "FR1-amended", afterAmend.Requirements[0].Name, "re-querying the same id set after Amend must return the amended, live contents")
	assert.Equal(t, amended.RevisionID, afterAmend.Requirements[0].RevisionID)
	assert.NotEqual(t, beforeAmend.Requirements[0].RevisionID, afterAmend.Requirements[0].RevisionID,
		"the revision metadata must move forward with the amendment -- a replay would keep returning the original revision")
}
