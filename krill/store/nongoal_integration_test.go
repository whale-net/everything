//go:build integration

// Real-Postgres coverage for NonGoalStore (migration 002, issue #2488) --
// the other product-level brief-document entity kind this task settles,
// discriminated by Kind (permanent vs deferred). See
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

func TestNonGoalStore_Create_PermanentAndDeferred_MintDistinctSurrogateIDs(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	permanent, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "Multi-tenant scope", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, permanent.ID)
	assert.Equal(t, store.NonGoalKindPermanent, permanent.Kind)

	deferred, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindDeferred, "Cross-product decisions (C23)", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, deferred.ID)
	assert.Equal(t, store.NonGoalKindDeferred, deferred.Kind)
	assert.NotEqual(t, permanent.ID, deferred.ID)
}

func TestNonGoalStore_Create_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	_, err := s.NonGoals().Create(ctx, scopeID, uuid.New(), store.NonGoalKindPermanent, "Orphan", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM non_goal`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Create must insert no row")
}

func TestNonGoalStore_Create_DuplicateNameSameProduct_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	_, err = s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "Multi-tenant scope", nil)
	require.NoError(t, err)

	// Uniqueness is on name alone, not (kind, name) -- a permanent and a
	// deferred non-goal with the same name under the same product must
	// also collide.
	_, err = s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindDeferred, "Multi-tenant scope", nil)
	assert.Error(t, err, "a second current NonGoal with the same name under the same product must be rejected (LB1), regardless of kind")
}
