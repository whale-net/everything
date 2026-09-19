//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag (real Postgres via
// dbtest, requires Docker) and is excluded from `bazel test //...`.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// searchProbeInput/searchProbeOutput are a minimal, argument-free tool
// shape -- these tests only care about tool names/descriptions surfacing
// through Candidates and being recorded (or not) as CallTool invocations,
// never about a real tool's own behavior.
type searchProbeInput struct{}

type searchProbeOutput struct{}

// newSearchToolsTestServer starts a plain, unauthenticated in-process MCP
// server exposing widgetTool/gadgetTool by name and description, counting
// every CallTool invocation it receives -- FR4's "never dispatched to a
// domain server" is only provable if something on the server side would
// notice a dispatch that shouldn't have happened.
func newSearchToolsTestServer(t *testing.T) (serverURL string, callToolCount *int64) {
	t.Helper()

	var count int64
	srv := mcp.NewServer(&mcp.Implementation{Name: "search-tools-test-server", Version: "0.1.0"}, nil)

	mcp.AddTool(srv, &mcp.Tool{Name: "list_widgets", Description: "List every widget in the catalog."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ searchProbeInput) (*mcp.CallToolResult, searchProbeOutput, error) {
			atomic.AddInt64(&count, 1)
			return nil, searchProbeOutput{}, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_gadget", Description: "Remove a gadget from inventory."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ searchProbeInput) (*mcp.CallToolResult, searchProbeOutput, error) {
			atomic.AddInt64(&count, 1)
			return nil, searchProbeOutput{}, nil
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL, &count
}

// newSearchToolsTestActivities builds an *Activities wired with store and a
// throwaway-signer *tools.Dispatcher -- everything SearchTools needs
// (a.Store for transcript commits, a.Dispatcher.Issuer for candidate-pool
// credential minting), no ledger or auth verification since this file's
// fake server has neither.
func newSearchToolsTestActivities(t *testing.T, store *session.Store) *Activities {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, "https://whagent.example.test", "test-key-1")
	require.NoError(t, err)

	return &Activities{Store: store, Dispatcher: &tools.Dispatcher{Issuer: persona.NewIssuer(signer)}}
}

// TestActivities_SearchTools_CommitsCallResultUnlockTriple_AtSameCallIndex
// proves the activity's core commit contract (FR4/FR5): one search_tools
// call commits exactly three transcript events -- tool_call:<i>,
// tool_result:<i>, tool_unlock:<i> -- all at the same call index, the
// result payload names every matched tool, and the underlying MCP server
// never receives a CallTool invocation for search_tools (FR4's "never
// dispatched to a domain server").
func TestActivities_SearchTools_CommitsCallResultUnlockTriple_AtSameCallIndex(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	a := newSearchToolsTestActivities(t, store)

	serverURL, callToolCount := newSearchToolsTestServer(t)
	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}

	result, err := a.SearchTools(ctx, SearchToolsInput{
		SessionID: sess.SessionID,
		AgentID:   sess.AgentID,
		ToolSet:   toolSet,
		Turn:      1,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: tools.SearchToolsName, Arguments: `{"query":"widget"}`},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"list_widgets"}, result.Matched)

	rows, err := db.Pool.Query(ctx, `
		SELECT type, payload FROM transcript_event
		WHERE session_id = $1 AND turn = $2 ORDER BY seq
	`, sess.SessionID, 1)
	require.NoError(t, err)
	defer rows.Close()

	var types []string
	var payloads []json.RawMessage
	for rows.Next() {
		var typ string
		var payload json.RawMessage
		require.NoError(t, rows.Scan(&typ, &payload))
		types = append(types, typ)
		payloads = append(payloads, payload)
	}
	require.NoError(t, rows.Err())

	require.Equal(t, []string{"tool_call:0", "tool_result:0", "tool_unlock:0"}, types,
		"one search_tools call must commit exactly this triple, at the same call index")

	var resultPayload toolResultEventPayload
	require.NoError(t, json.Unmarshal(payloads[1], &resultPayload))
	assert.Contains(t, resultPayload.Content, "list_widgets", "the tool_result payload content must name every matched tool")
	assert.False(t, resultPayload.IsError)

	var unlockPayload toolUnlockEventPayload
	require.NoError(t, json.Unmarshal(payloads[2], &unlockPayload))
	assert.Equal(t, []string{"list_widgets"}, unlockPayload.ToolNames)

	assert.Equal(t, int64(0), atomic.LoadInt64(callToolCount), "search_tools must never be dispatched to a domain server (FR4)")
}

// TestActivities_SearchTools_RetryIsIdempotent proves the activity's
// Temporal retry-safety contract: re-invoking SearchTools with identical
// input after a completed prior attempt commits no duplicate events and
// returns the same Matched result -- AppendIfAbsent's (session_id, turn,
// type) idempotency key, the same guarantee DispatchTool's own pair
// already has.
func TestActivities_SearchTools_RetryIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	a := newSearchToolsTestActivities(t, store)

	serverURL, _ := newSearchToolsTestServer(t)
	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}

	in := SearchToolsInput{
		SessionID: sess.SessionID,
		AgentID:   sess.AgentID,
		ToolSet:   toolSet,
		Turn:      1,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: tools.SearchToolsName, Arguments: `{"query":"widget"}`},
	}

	first, err := a.SearchTools(ctx, in)
	require.NoError(t, err)

	// Simulate a Temporal retry: re-invoke the same activity call with
	// identical input, as if the first attempt's result was lost before
	// Temporal recorded it complete.
	second, err := a.SearchTools(ctx, in)
	require.NoError(t, err)
	assert.Equal(t, first.Matched, second.Matched, "a retried call must return the same Matched result")

	var rowCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM transcript_event WHERE session_id = $1 AND turn = $2
	`, sess.SessionID, 1).Scan(&rowCount))
	assert.Equal(t, 3, rowCount, "a retried call must commit no duplicate events")
}

// TestActivities_SearchTools_ZeroMatch_StillCommitsUnlockWithEmptyNames
// proves FR5/FR6's "the search happened, so the transcript says so" rule:
// a query that matches nothing still commits all three events, with the
// tool_unlock event's ToolNames empty rather than the unlock being skipped
// entirely.
func TestActivities_SearchTools_ZeroMatch_StillCommitsUnlockWithEmptyNames(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	a := newSearchToolsTestActivities(t, store)

	serverURL, _ := newSearchToolsTestServer(t)
	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}

	result, err := a.SearchTools(ctx, SearchToolsInput{
		SessionID: sess.SessionID,
		AgentID:   sess.AgentID,
		ToolSet:   toolSet,
		Turn:      1,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: tools.SearchToolsName, Arguments: `{"query":"nonexistent-capability"}`},
	})
	require.NoError(t, err)
	assert.Empty(t, result.Matched)

	var types []string
	rows, err := db.Pool.Query(ctx, `
		SELECT type FROM transcript_event WHERE session_id = $1 AND turn = $2 ORDER BY seq
	`, sess.SessionID, 1)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var typ string
		require.NoError(t, rows.Scan(&typ))
		types = append(types, typ)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []string{"tool_call:0", "tool_result:0", "tool_unlock:0"}, types,
		"a zero-match search must still commit the full triple")

	var unlockPayload json.RawMessage
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT payload FROM transcript_event WHERE session_id = $1 AND turn = $2 AND type = 'tool_unlock:0'
	`, sess.SessionID, 1).Scan(&unlockPayload))
	var payload toolUnlockEventPayload
	require.NoError(t, json.Unmarshal(unlockPayload, &payload))
	assert.Empty(t, payload.ToolNames, "a zero-match search commits an unlock with an empty (not omitted) name list")
}

// TestActivities_SearchTools_TwoCallsInOneTurn_DistinctIndices_BothUnlocksLand
// proves callIndex correlation across more than one search_tools call within
// a single turn: two calls at CallIndex 0 and 1 each commit their own
// tool_call/tool_result/tool_unlock triple, and both unlock events are
// present -- the second call's commit never collides with or overwrites the
// first's.
func TestActivities_SearchTools_TwoCallsInOneTurn_DistinctIndices_BothUnlocksLand(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	a := newSearchToolsTestActivities(t, store)

	serverURL, _ := newSearchToolsTestServer(t)
	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}

	first, err := a.SearchTools(ctx, SearchToolsInput{
		SessionID: sess.SessionID,
		AgentID:   sess.AgentID,
		ToolSet:   toolSet,
		Turn:      1,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: tools.SearchToolsName, Arguments: `{"query":"widget"}`},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"list_widgets"}, first.Matched)

	second, err := a.SearchTools(ctx, SearchToolsInput{
		SessionID: sess.SessionID,
		AgentID:   sess.AgentID,
		ToolSet:   toolSet,
		Turn:      1,
		CallIndex: 1,
		Call:      llm.ToolCall{ID: "call-2", Name: tools.SearchToolsName, Arguments: `{"query":"gadget"}`},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"delete_gadget"}, second.Matched)

	var types []string
	rows, err := db.Pool.Query(ctx, `
		SELECT type FROM transcript_event WHERE session_id = $1 AND turn = $2 ORDER BY seq
	`, sess.SessionID, 1)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var typ string
		require.NoError(t, rows.Scan(&typ))
		types = append(types, typ)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{
		"tool_call:0", "tool_result:0", "tool_unlock:0",
		"tool_call:1", "tool_result:1", "tool_unlock:1",
	}, types, "each search_tools call in one turn must commit its own triple at its own call index")
}

// TestActivities_SearchTools_MalformedQuery_CommitsErrorResultNotUnlock
// proves the malformed-argument path is answered as an ordinary tool
// result, not a session failure (dispatch.go's "isError is not a
// whagent-net failure" rule): the call/result pair still commits, IsError
// is true, and -- since no search actually ran -- no tool_unlock event is
// committed at all.
func TestActivities_SearchTools_MalformedQuery_CommitsErrorResultNotUnlock(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	a := newSearchToolsTestActivities(t, store)

	serverURL, callToolCount := newSearchToolsTestServer(t)
	toolSet := []session.ToolServerRef{{ServerURL: serverURL}}

	result, err := a.SearchTools(ctx, SearchToolsInput{
		SessionID: sess.SessionID,
		AgentID:   sess.AgentID,
		ToolSet:   toolSet,
		Turn:      1,
		CallIndex: 0,
		Call:      llm.ToolCall{ID: "call-1", Name: tools.SearchToolsName, Arguments: `{}`},
	})
	require.NoError(t, err, "a malformed query must not fail the activity/session")
	assert.Empty(t, result.Matched)

	var types []string
	rows, err := db.Pool.Query(ctx, `
		SELECT type FROM transcript_event WHERE session_id = $1 AND turn = $2 ORDER BY seq
	`, sess.SessionID, 1)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var typ string
		require.NoError(t, rows.Scan(&typ))
		types = append(types, typ)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"tool_call:0", "tool_result:0"}, types,
		"a malformed query commits the call/result pair but never an unlock -- no search actually ran")

	var resultRaw json.RawMessage
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT payload FROM transcript_event WHERE session_id = $1 AND turn = $2 AND type = 'tool_result:0'
	`, sess.SessionID, 1).Scan(&resultRaw))
	var resultPayload toolResultEventPayload
	require.NoError(t, json.Unmarshal(resultRaw, &resultPayload))
	assert.True(t, resultPayload.IsError)
	assert.Equal(t, int64(0), atomic.LoadInt64(callToolCount))
}
