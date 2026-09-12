//go:build integration

// Real-Postgres coverage for RequirementStore (migration 002, issue #2488)
// -- an FR or an NFR discriminated by kind, sharing one table and one
// constraint shape. See store_integration_test.go's package doc for why
// this file only builds under the "integration" build tag.
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func setupFeature(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID) store.Feature {
	t.Helper()
	fs := setupFeatureSet(t, ctx, s, scopeID)
	f, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	return f
}

func TestRequirementStore_Create_FRAndNFR_MintDistinctSurrogateIDs(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	feature := setupFeature(t, ctx, s, scopeID)

	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Create Product", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, fr.ID)
	assert.Equal(t, store.RequirementKindFR, fr.Kind)
	assert.Equal(t, feature.ID, fr.FeatureID)

	nfr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Store layer only", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, nfr.ID)
	assert.Equal(t, store.RequirementKindNFR, nfr.Kind)
	assert.NotEqual(t, fr.ID, nfr.ID)
}

func TestRequirementStore_Create_UnknownFeature_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	_, err := s.Requirements().Create(ctx, scopeID, uuid.New(), store.RequirementKindFR, "Orphan", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM requirement`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Create must insert no row")
}

func TestRequirementStore_Create_DuplicateNameSameFeature_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	feature := setupFeature(t, ctx, s, scopeID)

	_, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Create Product", nil)
	require.NoError(t, err)

	// The uniqueness index is on name alone (scope_id, feature_id, name),
	// not (scope_id, feature_id, kind, name) -- an FR and an NFR with the
	// same name under the same Feature must also collide.
	_, err = s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Create Product", nil)
	assert.Error(t, err, "a second current Requirement with the same name under the same feature must be rejected (LB1), regardless of kind")
}

func TestRequirementStore_ListCurrentByFeature_OrdersByKindThenPositionThenName(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	feature := setupFeature(t, ctx, s, scopeID)

	nfr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindNFR, "Perf", nil)
	require.NoError(t, err)
	fr, err := s.Requirements().Create(ctx, scopeID, feature.ID, store.RequirementKindFR, "Feature", nil)
	require.NoError(t, err)

	got, err := s.Requirements().ListCurrentByFeature(ctx, feature.ID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, fr.ID, got[0].ID, "FR sorts before NFR")
	assert.Equal(t, nfr.ID, got[1].ID)
}
