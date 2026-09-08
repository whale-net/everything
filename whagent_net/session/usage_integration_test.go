//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/session"
)

// TestUsageStore_SumCost_SumsAcrossTurnsIncludingEstimated proves SumCost
// is a running total across every turn_usage row for the session,
// including rows with CostEstimated true (FR7: never a separately-mutated
// counter, and an estimated cost still counts).
func TestUsageStore_SumCost_SumsAcrossTurnsIncludingEstimated(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	require.NoError(t, s.Usage().RecordTurn(ctx, session.TurnUsage{
		SessionID: sess.SessionID, Turn: 1, Model: "test-model",
		PromptTokens: 100, CompletionTokens: 50, CostUSD: 0.01, CostEstimated: false,
	}))
	require.NoError(t, s.Usage().RecordTurn(ctx, session.TurnUsage{
		SessionID: sess.SessionID, Turn: 2, Model: "test-model",
		PromptTokens: 200, CompletionTokens: 100, CostUSD: 0.02, CostEstimated: true,
	}))

	total, err := s.Usage().SumCost(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.InDelta(t, 0.03, total, 0.0000001, "SumCost must include estimated-cost rows in the running total")
}

// TestUsageStore_SumCost_NoRows_ReturnsZero proves SumCost never errors on
// a session with no turn_usage rows -- COALESCE(SUM(...), 0), not a NULL
// propagating out.
func TestUsageStore_SumCost_NoRows_ReturnsZero(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	sess := createTestSession(t, ctx, s)

	total, err := s.Usage().SumCost(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, 0.0, total)
}

// TestUsageStore_CostUSD_RejectsNullInsert proves cost_usd is a real NOT
// NULL column (FR7: unknown cost is never zero and never null) -- a raw
// INSERT with it explicitly NULL must be rejected.
func TestUsageStore_CostUSD_RejectsNullInsert(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	_, err := db.Pool.Exec(ctx, `
		INSERT INTO turn_usage (session_id, turn, model, prompt_tokens, completion_tokens, cost_usd, cost_estimated)
		VALUES ($1, 1, 'test-model', 10, 5, NULL, true)
	`, sess.SessionID)
	assert.Error(t, err, "a NULL cost_usd must be rejected by the NOT NULL constraint (FR7)")

	var count int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM turn_usage WHERE session_id = $1`, sess.SessionID).Scan(&count))
	assert.Equal(t, 0, count, "the rejected insert must not leave a row behind")
}
