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

// TestNonGoalStore_ListCurrentByProduct_SameKind_OrdersByCreationPosition is
// this issue's Testing case 2 (FR7): three permanent non-goals created
// under one product in order C, A, B (not alphabetical) must list back C,
// A, B -- with kind held constant, ordering is purely position-then-name.
func TestNonGoalStore_ListCurrentByProduct_SameKind_OrdersByCreationPosition(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	c, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "C Non-Goal", nil)
	require.NoError(t, err)
	a, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "A Non-Goal", nil)
	require.NoError(t, err)
	b, err := s.NonGoals().Create(ctx, scopeID, product.ID, store.NonGoalKindPermanent, "B Non-Goal", nil)
	require.NoError(t, err)

	got, err := s.NonGoals().ListCurrentByProduct(ctx, product.ID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []uuid.UUID{c.ID, a.ID, b.ID}, []uuid.UUID{got[0].ID, got[1].ID, got[2].ID},
		"ListCurrentByProduct must reflect creation order (C, A, B) within the same kind, not alphabetical order (A, B, C)")
}
