//go:build integration

// Real-Postgres coverage for ProductStore (migration 002, issue #2488) --
// the top of the spec chain, with no parent-existence check of its own. See
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

func TestProductStore_Create_MintsSurrogateIDAndScope(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	p, err := s.Products().Create(ctx, scopeID, "Krill", "Spec-of-record for agent swarms")
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, p.ID)
	assert.NotEqual(t, uuid.Nil, p.RevisionID)
	assert.Equal(t, scopeID, p.ScopeID)
	assert.Equal(t, "Krill", p.Name)
	assert.Equal(t, "Spec-of-record for agent swarms", p.Vision)
	assert.Nil(t, p.ValidTo, "a newly-created row must be current")

	got, err := s.Products().GetCurrentByID(ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, p, got)
}

func TestProductStore_Create_DuplicateNameSameScope_RejectedByScopeQualifiedUniqueness(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	_, err := s.Products().Create(ctx, scopeID, "Krill", "V1")
	require.NoError(t, err)

	_, err = s.Products().Create(ctx, scopeID, "Krill", "V2")
	assert.Error(t, err, "a second current Product with the same name in the same scope must be rejected (LB1)")

	// Case-insensitive: product_scope_name_current_idx is on lower(name).
	_, err = s.Products().Create(ctx, scopeID, "KRILL", "V3")
	assert.Error(t, err, "the scope-qualified uniqueness constraint must be case-insensitive")
}

func TestProductStore_Create_SameNameDifferentScope_BothSucceed(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeA := newScope(t, ctx, db)
	scopeB := newScope(t, ctx, db)

	_, err := s.Products().Create(ctx, scopeA, "Krill", "V1")
	require.NoError(t, err)

	_, err = s.Products().Create(ctx, scopeB, "Krill", "V1")
	assert.NoError(t, err, "the same name in a DIFFERENT scope must not collide -- every uniqueness constraint is scope-qualified (LB1)")
}

func TestProductStore_GetCurrentByID_UnknownID_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)

	_, err := s.Products().GetCurrentByID(ctx, uuid.New())
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestProductStore_ListCurrentByScope_OrdersByPositionThenName(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	b, err := s.Products().Create(ctx, scopeID, "B Product", "")
	require.NoError(t, err)
	a, err := s.Products().Create(ctx, scopeID, "A Product", "")
	require.NoError(t, err)

	got, err := s.Products().ListCurrentByScope(ctx, scopeID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Create assigns position in append (creation) order -- one past the
	// current max among scopeID's own current Products, never the
	// column's own DEFAULT 0 -- so "B Product" (created first) sorts
	// before "A Product" (created second) despite the name order, proving
	// position (not name) is the primary sort key.
	assert.Equal(t, b.ID, got[0].ID)
	assert.Equal(t, a.ID, got[1].ID)
}
