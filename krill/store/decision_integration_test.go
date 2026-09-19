//go:build integration

// Real-Postgres coverage for LoadBearingDecisionStore (migration 002, issue
// #2488) -- FR4's persistence, attached to the FeatureSet it constrains
// (C2). See store_integration_test.go's package doc for why this file only
// builds under the "integration" build tag.
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func TestLoadBearingDecisionStore_Create_AttachesToFeatureSetAndIsReadableBack(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	fs := setupFeatureSet(t, ctx, s, scopeID)

	body := "id is stable across every supersession"
	decision, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB2 identity", &body)
	require.NoError(t, err)
	assert.NotEqual(t, uuid.Nil, decision.ID)
	assert.Equal(t, fs.ID, decision.FeatureSetID)

	got, err := s.Decisions().GetCurrentByID(ctx, decision.ID)
	require.NoError(t, err)
	assert.Equal(t, decision, got)

	list, err := s.Decisions().ListCurrentByFeatureSet(ctx, fs.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "the decision must be readable back from the FeatureSet it was attached to")
	assert.Equal(t, decision.ID, list[0].ID)
}

func TestLoadBearingDecisionStore_Create_UnknownFeatureSet_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)

	_, err := s.Decisions().Create(ctx, scopeID, uuid.New(), "Orphan", nil)
	assert.ErrorIs(t, err, store.ErrNotFound)

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM load_bearing_decision`).Scan(&count))
	assert.Equal(t, 0, count, "a rejected Create must insert no row")
}

func TestLoadBearingDecisionStore_Create_DuplicateNameSameFeatureSet_Rejected(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	fs := setupFeatureSet(t, ctx, s, scopeID)

	_, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB2 identity", nil)
	require.NoError(t, err)

	_, err = s.Decisions().Create(ctx, scopeID, fs.ID, "LB2 identity", nil)
	assert.Error(t, err, "a second current LoadBearingDecision with the same name under the same feature_set must be rejected (LB1)")
}

// TestLoadBearingDecisionStore_ListCurrentByFeatureSet_NeverLeaksAnotherFeatureSets
// proves a decision attached to one FeatureSet does not surface when
// listing a different FeatureSet's decisions -- C2's "attached to the area
// it constrains" is only meaningful if scoping actually isolates.
func TestLoadBearingDecisionStore_ListCurrentByFeatureSet_NeverLeaksAnotherFeatureSets(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "Krill", "")
	require.NoError(t, err)
	fsA, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS A", nil)
	require.NoError(t, err)
	fsB, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS B", nil)
	require.NoError(t, err)

	_, err = s.Decisions().Create(ctx, scopeID, fsA.ID, "Decision on A", nil)
	require.NoError(t, err)

	listB, err := s.Decisions().ListCurrentByFeatureSet(ctx, fsB.ID)
	require.NoError(t, err)
	assert.Empty(t, listB, "FeatureSet B must not see FeatureSet A's LoadBearingDecision")
}

// TestLoadBearingDecisionStore_ListCurrentByFeatureSet_OrdersByCreationPosition
// is this issue's Testing case 2 (FR7): three decisions attached to one
// FeatureSet in order C, A, B (not alphabetical) must list back C, A, B.
func TestLoadBearingDecisionStore_ListCurrentByFeatureSet_OrdersByCreationPosition(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	scopeID := newScope(t, ctx, db)
	fs := setupFeatureSet(t, ctx, s, scopeID)

	c, err := s.Decisions().Create(ctx, scopeID, fs.ID, "C Decision", nil)
	require.NoError(t, err)
	a, err := s.Decisions().Create(ctx, scopeID, fs.ID, "A Decision", nil)
	require.NoError(t, err)
	b, err := s.Decisions().Create(ctx, scopeID, fs.ID, "B Decision", nil)
	require.NoError(t, err)

	got, err := s.Decisions().ListCurrentByFeatureSet(ctx, fs.ID)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []uuid.UUID{c.ID, a.ID, b.ID}, []uuid.UUID{got[0].ID, got[1].ID, got[2].ID},
		"ListCurrentByFeatureSet must reflect creation order (C, A, B), not alphabetical order (A, B, C)")
}
