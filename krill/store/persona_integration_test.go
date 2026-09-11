//go:build integration

// Real-Postgres coverage for PersonaStore (migration 002, issue #2488) --
// one of the two product-level brief-document entity kinds this task
// settles. See store_integration_test.go's package doc for why this file
// only builds under the "integration" build tag.
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func TestPersonaStore_Create_MintsSurrogateIDUnderProduct(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	p, err := s.Personas().Create(ctx, scopeID, product.ID, "Agent Swarm Operator", nil)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, p.ID)
	assert.Equal(t, scopeID, p.ScopeID)
	assert.Equal(t, product.ID, p.ProductID)
}

func TestPersonaStore_Create_UnknownProduct_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	_, err := s.Personas().Create(ctx, scopeID, uuid.New(), "Orphan", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM persona`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Create must insert no row")
}

func TestPersonaStore_Create_DuplicateNameSameProduct_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)

	_, err = s.Personas().Create(ctx, scopeID, product.ID, "Agent Swarm Operator", nil)
	require.NoError(t, err)

	_, err = s.Personas().Create(ctx, scopeID, product.ID, "Agent Swarm Operator", nil)
	assert.Error(t, err, "a second current Persona with the same name under the same product must be rejected (LB1)")
}
