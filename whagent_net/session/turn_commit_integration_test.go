//go:build integration

// See store_integration_test.go's package doc comment for why this file
// only builds under the "integration" tag.
package session_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/session"
)

// testCommitTurnParams builds a CommitTurnParams fixture for sess's turn 1
// -- the shape worker's CommitTurn activity (issue #2114) passes through
// to Store.CommitTurn every call.
func testCommitTurnParams(sess *session.Session) session.CommitTurnParams {
	return session.CommitTurnParams{
		SessionID: sess.SessionID,
		Turn:      1,
		EventType: events.EventTypeAssistantMessage,
		Payload:   []byte(`{"role":"assistant","content":"hello"}`),
		Usage: session.TurnUsage{
			Model:            "test-model",
			PromptTokens:     10,
			CompletionTokens: 5,
			CostUSD:          0.001,
			CostEstimated:    false,
		},
	}
}

// TestStore_CommitTurn_RetriedCall_CommitsExactlyOnce proves the
// retry-safety CommitTurn's doc comment promises (issue #2114's Testing
// phase: "a turn whose commit activity fails once and is retried commits
// exactly one set of events, no duplicated turn"): Temporal's
// at-least-once activity execution can re-invoke CommitTurn after a prior
// attempt's write already durably committed but whose result was lost in
// transit (worker crash, RPC timeout) -- simulated here by simply calling
// Store.CommitTurn twice with identical params, exactly as a retried
// activity invocation would.
func TestStore_CommitTurn_RetriedCall_CommitsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)
	params := testCommitTurnParams(sess)

	first, err := s.CommitTurn(ctx, params)
	require.NoError(t, err)

	second, err := s.CommitTurn(ctx, params)
	require.NoError(t, err, "a retried CommitTurn call must succeed, not error")

	assert.Equal(t, first.EventID, second.EventID, "a retry must return the same event, not mint a new one")
	assert.Equal(t, first.Seq, second.Seq)

	var eventCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM transcript_event WHERE session_id = $1 AND turn = $2 AND type = $3
	`, sess.SessionID, params.Turn, params.EventType).Scan(&eventCount))
	assert.Equal(t, 1, eventCount, "a retried commit must not duplicate the transcript event")

	var usageCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM turn_usage WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, params.Turn).Scan(&usageCount))
	assert.Equal(t, 1, usageCount, "a retried commit must not duplicate the usage row")
}

// TestStore_CommitTurn_DifferentTurns_EachCommitsOwnEvent proves
// CommitTurn's retry-safety is scoped to (session, turn, event type) --
// two distinct turns for the same session each commit their own event
// rather than the second being mistaken for a retry of the first (the
// same "no duplication across turns" property the workflow-level
// TestSessionWorkflow_TwoSequentialTurns_OrderedNoDuplication test proves
// at the workflow layer).
func TestStore_CommitTurn_DifferentTurns_EachCommitsOwnEvent(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	sess := createTestSession(t, ctx, s)

	turn1 := testCommitTurnParams(sess)
	turn2 := testCommitTurnParams(sess)
	turn2.Turn = 2

	ev1, err := s.CommitTurn(ctx, turn1)
	require.NoError(t, err)
	ev2, err := s.CommitTurn(ctx, turn2)
	require.NoError(t, err)

	assert.NotEqual(t, ev1.EventID, ev2.EventID)
	assert.Less(t, ev1.Seq, ev2.Seq, "turn 2's event must come after turn 1's in commit order")

	var eventCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM transcript_event WHERE session_id = $1`, sess.SessionID).Scan(&eventCount))
	assert.Equal(t, 2, eventCount)
}
