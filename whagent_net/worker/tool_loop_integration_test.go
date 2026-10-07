//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag (real Postgres via
// dbtest, requires Docker) and is excluded from `bazel test //...`.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
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

// TestActivities_CallModel_PinnedContextIsSecondSystemMessageEveryCall proves
// every CallModel (turn 1, turn 2, a tool-loop iteration) sends the pinned
// text verbatim as the second system message, and that it never lands in the
// transcript.
func TestActivities_CallModel_PinnedContextIsSecondSystemMessageEveryCall(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)

	pinned := "the secret word is X"
	prompt := "definition prompt"
	subject := session.Subject{Iss: "https://issuer.example.com", Sub: "user-pinned", Kind: session.SubjectKindHuman}
	sess := &session.Session{
		SessionID: uuid.New(), Subject: subject, OnBehalfOf: subject,
		AgentID: "test-agent", Model: "test-model", Status: session.StatusRunning,
		PinnedContext: &pinned,
	}
	require.NoError(t, store.Sessions().Create(ctx, sess))

	var mu sync.Mutex
	var bodies []struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		bodies = append(bodies, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"g","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(ts.Close)

	a := &Activities{Store: store, LLM: llm.NewClient("k", ts.URL)}

	// turn 1, turn 2, then a tool-loop iteration of turn 2 (same activity,
	// later call) -- each with a growing transcript.
	var ids []uuid.UUID
	for turn := 1; turn <= 3; turn++ {
		payload, err := marshalMessagePayload(llm.Message{Role: llm.RoleUser, Content: "question"})
		require.NoError(t, err)
		ev, err := store.Transcript().Append(ctx, sess.SessionID, (turn+1)/2, events.EventTypeUserMessage, payload)
		require.NoError(t, err)
		ids = append(ids, ev.EventID)
		_, err = a.CallModel(ctx, CallModelInput{
			SessionID: sess.SessionID, Turn: (turn + 1) / 2, EventIDs: append([]uuid.UUID(nil), ids...),
			Model: "test-model", SystemPrompt: &prompt,
		})
		require.NoError(t, err)
	}

	require.Len(t, bodies, 3)
	for i, b := range bodies {
		require.GreaterOrEqual(t, len(b.Messages), 3, "call %d", i)
		assert.Equal(t, "system", b.Messages[0].Role)
		assert.Equal(t, prompt, b.Messages[0].Content)
		assert.Equal(t, "system", b.Messages[1].Role)
		assert.Equal(t, pinned, b.Messages[1].Content, "call %d", i)
		assert.Equal(t, "user", b.Messages[2].Role)
	}

	evs, err := store.Transcript().Read(ctx, sess.SessionID, 0, 100)
	require.NoError(t, err)
	assert.Len(t, evs, 3, "only the three user messages; no pinned-context event")
	for _, ev := range evs {
		assert.NotContains(t, string(ev.Payload), pinned)
	}
}
