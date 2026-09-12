//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag (real Postgres via
// dbtest, requires Docker) and is excluded from `bazel test //...`.
package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
)

// TestActivities_CommitToolLoopIteration_CommitsMessageWithoutUsageRow
// proves "add the inner tool loop"'s own retry-safety/accounting contract:
// a non-final loop iteration's commit appends its assistant-message event
// under assistantMessageEventType(iteration) (never the bare
// EventTypeAssistantMessage CommitTurn uses for a turn's final response)
// and returns its own cost/tokens, but writes no turn_usage row of its own
// -- that row is only ever written once, by CommitTurn, once the loop's
// final iteration commits (CommitToolLoopIteration's doc comment).
func TestActivities_CommitToolLoopIteration_CommitsMessageWithoutUsageRow(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	a := &Activities{Store: store}
	result, err := a.CommitToolLoopIteration(ctx, CommitToolLoopIterationInput{
		SessionID: sess.SessionID,
		Turn:      1,
		Iteration: 0,
		Model:     "test-model",
		Response: llm.Response{
			Message:   llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "search", Arguments: "{}"}},
			Usage:     llm.UsageReport{PromptTokens: 10, CompletionTokens: 5, ProviderCostUSD: costPtr(20000)},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(10), result.PromptTokens)
	assert.Equal(t, int64(5), result.CompletionTokens)
	assert.InDelta(t, 0.02, result.CostUSD, 1e-9)
	assert.False(t, result.CostEstimated)

	var eventType string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT type FROM transcript_event WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&eventType))
	assert.Equal(t, "assistant_message:0", eventType, "a non-final loop iteration must never commit under the bare assistant_message type CommitTurn reserves for a turn's final response")

	var usageRows int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM turn_usage WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&usageRows))
	assert.Equal(t, 0, usageRows, "a non-final loop iteration must not write its own turn_usage row")
}

// TestActivities_CommitTurn_FoldsPriorLoopIterationsIntoOneUsageRow proves
// the other half of the same contract: CommitTurn's Prior* fields (this
// turn's earlier loop iterations' usage, accumulated in-workflow) are added
// into the SAME single turn_usage row as this call's own response, so a
// turn that looped still records its full cost/token total in exactly one
// row -- never a second row, and never silently dropping the earlier
// iterations' cost.
func TestActivities_CommitTurn_FoldsPriorLoopIterationsIntoOneUsageRow(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)

	a := &Activities{Store: store}
	_, err := a.CommitTurn(ctx, CommitTurnInput{
		SessionID: sess.SessionID,
		Turn:      1,
		Model:     "test-model",
		Response: llm.Response{
			Message: llm.Message{Role: llm.RoleAssistant, Content: "final answer"},
			Usage:   llm.UsageReport{PromptTokens: 20, CompletionTokens: 8, ProviderCostUSD: costPtr(30000)},
		},
		PriorPromptTokens:     10,
		PriorCompletionTokens: 5,
		PriorCostUSD:          0.02,
		PriorCostEstimated:    false,
	})
	require.NoError(t, err)

	var eventType string
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT type FROM transcript_event WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&eventType))
	assert.Equal(t, events.EventTypeAssistantMessage, eventType, "the turn's final response must still commit under the bare assistant_message type, unchanged from before the inner tool loop existed")

	var rows int
	var promptTokens, completionTokens int64
	var costUSD float64
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*), sum(prompt_tokens), sum(completion_tokens), sum(cost_usd)
		FROM turn_usage WHERE session_id = $1 AND turn = $2
		GROUP BY session_id, turn
	`, sess.SessionID, 1).Scan(&rows, &promptTokens, &completionTokens, &costUSD))
	assert.Equal(t, 1, rows, "a turn that looped must still produce exactly one turn_usage row")
	assert.Equal(t, int64(30), promptTokens, "prompt tokens must be this call's own plus every prior loop iteration's")
	assert.Equal(t, int64(13), completionTokens, "completion tokens must be this call's own plus every prior loop iteration's")
	assert.InDelta(t, 0.05, costUSD, 1e-9, "cost must be this call's own plus every prior loop iteration's")
}

func costPtr(c llm.CostUSD) *llm.CostUSD { return &c }
