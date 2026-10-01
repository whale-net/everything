//go:build integration

// Real-Postgres coverage for LBProtectsStore (lb_protects.go, migration 033).
//
//	bazel test //krill/store:lb_protects_integration_test --test_output=all
package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

func TestLBProtects_AddWithdrawReAdd(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneTestStore(t)
	scopeID := newMilestoneTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "P", "v")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	f, err := s.Features().Create(ctx, scopeID, fs.ID, "F", nil)
	require.NoError(t, err)
	lb, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB", nil)
	require.NoError(t, err)
	edges := s.LBProtects()
	who := store.Subject{Iss: "iss", Sub: "sub", Kind: store.SubjectKindHuman}
	agent := store.Subject{Iss: "iss", Sub: "bot", Kind: store.SubjectKindAgent}

	// Uncovered until an edge is recorded.
	got, err := edges.ListActiveByFeatures(ctx, []uuid.UUID{f.ID})
	require.NoError(t, err)
	assert.Empty(t, got)

	e1, err := edges.Add(ctx, scopeID, lb.ID, f.ID, "")
	require.NoError(t, err)
	again, err := edges.Add(ctx, scopeID, lb.ID, f.ID, "ignored on duplicate")
	require.NoError(t, err)
	assert.Equal(t, e1.ID, again.ID, "adding an active edge is idempotent")
	got, err = edges.ListActiveByFeatures(ctx, []uuid.UUID{f.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, lb.ID, got[0].DecisionID)
	assert.Empty(t, got[0].Rationale, "no rationale reads back empty")

	require.NoError(t, edges.Withdraw(ctx, scopeID, lb.ID, f.ID, agent, who))
	got, err = edges.ListActiveByFeatures(ctx, []uuid.UUID{f.ID})
	require.NoError(t, err)
	assert.Empty(t, got, "withdrawn edge is no longer active")
	assert.ErrorIs(t, edges.Withdraw(ctx, scopeID, lb.ID, f.ID, agent, who), store.ErrNotFound, "no active edge left to withdraw")

	var rows int
	var by string
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*), max(withdrawn_by_acting_sub) FROM lb_protects_feature`).Scan(&rows, &by))
	assert.Equal(t, 1, rows, "withdrawal keeps the row")
	assert.Equal(t, "bot", by)

	e2, err := edges.Add(ctx, scopeID, lb.ID, f.ID, "")
	require.NoError(t, err)
	assert.NotEqual(t, e1.ID, e2.ID, "re-add opens a fresh edge")
	got, err = edges.ListActiveByFeatures(ctx, []uuid.UUID{f.ID})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM lb_protects_feature`).Scan(&rows))
	assert.Equal(t, 2, rows)

	// Unknown decision / feature are rejected.
	_, err = edges.Add(ctx, scopeID, uuid.New(), f.ID, "")
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = edges.Add(ctx, scopeID, lb.ID, uuid.New(), "")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestLBProtects_Rationale(t *testing.T) {
	ctx := context.Background()
	s, db := newMilestoneTestStore(t)
	scopeID := newMilestoneTestScope(t, ctx, db)
	product, err := s.Products().Create(ctx, scopeID, "P", "v")
	require.NoError(t, err)
	fs, err := s.FeatureSets().Create(ctx, scopeID, product.ID, "FS", nil)
	require.NoError(t, err)
	f, err := s.Features().Create(ctx, scopeID, fs.ID, "F", nil)
	require.NoError(t, err)
	lb, err := s.Decisions().Create(ctx, scopeID, fs.ID, "LB", nil)
	require.NoError(t, err)
	edges := s.LBProtects()
	who := store.Subject{Iss: "iss", Sub: "sub", Kind: store.SubjectKindHuman}

	e, err := edges.Add(ctx, scopeID, lb.ID, f.ID, "needs LB so the person is authorized")
	require.NoError(t, err)
	assert.Equal(t, "needs LB so the person is authorized", e.Rationale)

	// A duplicate add keeps the original rationale.
	again, err := edges.Add(ctx, scopeID, lb.ID, f.ID, "different")
	require.NoError(t, err)
	assert.Equal(t, e.ID, again.ID)
	assert.Equal(t, e.Rationale, again.Rationale)

	got, err := edges.ListActiveByFeatures(ctx, []uuid.UUID{f.ID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, e.Rationale, got[0].Rationale)

	// Withdraw then re-add takes the new rationale.
	require.NoError(t, edges.Withdraw(ctx, scopeID, lb.ID, f.ID, who, who))
	e2, err := edges.Add(ctx, scopeID, lb.ID, f.ID, "different")
	require.NoError(t, err)
	assert.Equal(t, "different", e2.Rationale)
}
