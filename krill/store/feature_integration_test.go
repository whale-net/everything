//go:build integration

// Real-Postgres coverage for FeatureStore (migration 002, issue #2488),
// including the capability-map decision this task settles: a `Cn` citation
// resolves onto a real Feature id, not a fourth parallel table. See
// store_integration_test.go's package doc for why this file only builds
// under the "integration" build tag.
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func setupFeatureSet(t *testing.T, ctx context.Context, s *store.Store, scopeID uuid.UUID) store.FeatureSet {
	t.Helper()
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)
	return fs
}

func TestFeatureStore_Create_MintsSurrogateIDUnderFeatureSet(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	fs := setupFeatureSet(t, ctx, s, scopeID)

	f, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, f.ID)
	assert.Equal(t, scopeID, f.ScopeID)
	assert.Equal(t, fs.ID, f.FeatureSetID)
}

func TestFeatureStore_Create_UnknownFeatureSet_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	_, err := s.Features().Create(ctx, scopeID, uuid.New(), "Orphan", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM feature`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Create must insert no row")
}

func TestFeatureStore_Create_DuplicateNameSameFeatureSet_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	fs := setupFeatureSet(t, ctx, s, scopeID)

	_, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)

	_, err = s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	assert.Error(t, err, "a second current Feature with the same name under the same feature_set must be rejected (LB1)")
}

// TestFeatureStore_CapabilityMapCitation_ResolvesToRealFeatureID proves the
// capability-map decision krill/ARCHITECTURE.md records: a `Cn` citation
// (here, an FR's own feature_id) resolves to a real Feature row via an FK,
// not a string -- there is no separate "capability" table or column to
// validate against.
func TestFeatureStore_CapabilityMapCitation_ResolvesToRealFeatureID(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	fs := setupFeatureSet(t, ctx, s, scopeID)

	capability, err := s.Features().Create(ctx, scopeID, fs.ID, "SCD2 Store", nil)
	require.NoError(t, err)

	// "FR4 (C3) -- ..." cites capability C3, which the store models as
	// Requirement.FeatureID -- exactly capability.ID, resolvable by
	// GetCurrentByID, not a free-floating string.
	fr, err := s.Requirements().Create(ctx, scopeID, capability.ID, store.RequirementKindFR, "Create Product", nil)
	require.NoError(t, err)
	assert.Equal(t, capability.ID, fr.FeatureID)

	resolved, err := s.Features().GetCurrentByID(ctx, fr.FeatureID)
	require.NoError(t, err, "an FR's citation of a capability must resolve to a real, current Feature row")
	assert.Equal(t, capability.ID, resolved.ID)
}
