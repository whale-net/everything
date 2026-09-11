//go:build integration

// Real-Postgres coverage for FeatureSetStore (migration 002, issue #2488).
// See store_integration_test.go's package doc for why this file only builds
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

func TestFeatureSetStore_Create_MintsSurrogateIDUnderProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	desc := "the spec entity model"
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", &desc)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, fs.ID)
	assert.Equal(t, scopeID, fs.ScopeID)
	assert.Equal(t, product.ID, fs.ProductID)
	require.NotNil(t, fs.Description)
	assert.Equal(t, desc, *fs.Description)
}

func TestFeatureSetStore_Create_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	_, err := s.FeatureSets().Create(ctx, scopeID, uuid.New(), "Orphan", nil)
	assert.ErrorIs(t, err, store.ErrNotFound, "Create must reject a featureSet whose parent product has no current row (LB2 parentage)")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM feature_set`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Create must insert no row")
}

func TestFeatureSetStore_Create_DuplicateNameSameProduct_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	_, err = s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	require.NoError(t, err)

	_, err = s.FeatureSets().Create(ctx, scopeID, product.ID, "Spec Entities", nil)
	assert.Error(t, err, "a second current FeatureSet with the same name under the same product must be rejected (LB1)")
}

func TestFeatureSetStore_Create_SameNameDifferentProduct_BothSucceed(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	productA, err := s.Products().Create(ctx, scopeID, "Product A", "")
	require.NoError(t, err)
	productB, err := s.Products().Create(ctx, scopeID, "Product B", "")
	require.NoError(t, err)

	_, err = s.FeatureSets().Create(ctx, scopeID, productA.ID, "Shared Name", nil)
	require.NoError(t, err)
	_, err = s.FeatureSets().Create(ctx, scopeID, productB.ID, "Shared Name", nil)
	assert.NoError(t, err, "uniqueness is scoped to (scope_id, product_id, name) -- a different product must not collide")
}
