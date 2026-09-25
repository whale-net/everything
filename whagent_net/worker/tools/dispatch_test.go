package tools_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/libs/go/whagent"
	"github.com/whale-net/everything/whagent_net/api/persona"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// readProbeInput mirrors a real domain read tool's input shape (e.g.
// audience_score_system's list_ideas): a plain Go struct with no
// idempotency_key field, so the go-sdk's reflection-derived schema sets
// additionalProperties: false and declares no idempotency_key property --
// exactly the shape that made an unconditional idempotency_key injection
// fail schema validation before this fix (dispatch.go's
// acceptsIdempotencyKey).
type readProbeInput struct {
	ChannelID string `json:"channel_id"`
}

type readProbeOutput struct {
	ChannelID string `json:"channel_id"`
}

// writeProbeInput mirrors a real domain write tool's input shape: it
// implements whagent.IdempotencyKeyed by declaring idempotency_key as a
// JSON field, so its generated schema does carry that property and
// dispatch.go's Dispatch is expected to attach the key.
type writeProbeInput struct {
	Key string `json:"idempotency_key,omitempty"`
}

func (i writeProbeInput) IdempotencyKey() string { return i.Key }

type writeProbeOutput struct {
	ReceivedKey string `json:"received_key"`
}

// newDispatchTestServer starts a plain, unauthenticated in-process MCP
// server exposing one read tool (no idempotency_key in its schema) and one
// write tool (idempotency_key in its schema) -- everything Dispatch needs
// to exercise acceptsIdempotencyKey's read/write distinction, without any
// of audience_score_system's store/auth machinery this package must not
// depend on.
func newDispatchTestServer(t *testing.T) string {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "dispatch-test-server", Version: "0.1.0"}, nil)

	mcp.AddTool(srv, &mcp.Tool{Name: "read_probe", Description: "read-only probe with a strict schema"},
		func(_ context.Context, _ *mcp.CallToolRequest, in readProbeInput) (*mcp.CallToolResult, readProbeOutput, error) {
			return nil, readProbeOutput{ChannelID: in.ChannelID}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "write_probe", Description: "write probe expecting idempotency_key"},
		func(_ context.Context, _ *mcp.CallToolRequest, in writeProbeInput) (*mcp.CallToolResult, writeProbeOutput, error) {
			return nil, writeProbeOutput{ReceivedKey: in.Key}, nil
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// newDispatchTestServerCounting mirrors newDispatchTestServer but also
// counts every HTTP request the server receives (httpHits -- a proxy for
// "a connection was opened", since resolveTarget's mintCredential+Connect+
// ListTools sequence for a tried ref always issues at least one) and every
// genuine read_probe invocation (readCalls) -- FR9's refusal tests below
// assert both stay at zero for a call resolveTarget's search-mode gate
// refuses before ever minting a credential or opening a connection (this
// package's "Tool selection" doc comment, dispatch.go).
func newDispatchTestServerCounting(t *testing.T) (serverURL string, httpHits, readCalls *int32) {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "dispatch-test-server", Version: "0.1.0"}, nil)

	readCalls = new(int32)
	mcp.AddTool(srv, &mcp.Tool{Name: "read_probe", Description: "read-only probe with a strict schema"},
		func(_ context.Context, _ *mcp.CallToolRequest, in readProbeInput) (*mcp.CallToolResult, readProbeOutput, error) {
			atomic.AddInt32(readCalls, 1)
			return nil, readProbeOutput{ChannelID: in.ChannelID}, nil
		})

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)

	httpHits = new(int32)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(httpHits, 1)
		mcpHandler.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL, httpHits, readCalls
}

// newDispatchTestFixture builds the whagent-net-side pieces Dispatch needs
// (a persona.Issuer over a throwaway signer, and a session) -- no ledger,
// no auth verification, since this file's server has neither.
func newDispatchTestFixture(t *testing.T, serverURL string) (*tools.Dispatcher, tools.DispatchInput) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := whagent.New(priv, "https://whagent.example.test", "test-key-1")
	require.NoError(t, err)

	sess := &session.Session{
		SessionID: uuid.New(),
		Subject: session.Subject{
			Iss:  "https://keycloak.example.test/realms/humans",
			Sub:  "human-worker-actor-1",
			Kind: session.SubjectKindHuman,
		},
		OnBehalfOf: session.Subject{
			Iss:  "https://keycloak.example.test/realms/humans",
			Sub:  "human-dispatch-test-1",
			Kind: session.SubjectKindHuman,
		},
		AgentID: "dispatch-test-agent",
		Model:   "test-model",
		Status:  session.StatusRunning,
	}

	dispatcher := &tools.Dispatcher{Issuer: persona.NewIssuer(signer)}
	in := tools.DispatchInput{
		Session: sess,
		AgentID: sess.AgentID,
		ToolSet: []session.ToolServerRef{{ServerURL: serverURL}},
	}
	return dispatcher, in
}

// TestDispatch_ReadTool_NoIdempotencyKeyAttached is this fix's regression
// proof: a read tool whose schema has no idempotency_key property (and,
// like a real domain server's RegisterRead-generated schema,
// additionalProperties: false) must not have the call fail schema
// validation -- which is exactly what happened when Dispatch attached
// idempotency_key to every call unconditionally (see dispatch.go's
// acceptsIdempotencyKey).
func TestDispatch_ReadTool_NoIdempotencyKeyAttached(t *testing.T) {
	ctx := context.Background()
	serverURL := newDispatchTestServer(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	result, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	assert.False(t, result.IsError, "a read tool with a strict schema must not fail validation: %s", result.Content)
	assert.Contains(t, result.Content, "chan-1")
}

// TestDispatch_WriteTool_IdempotencyKeyAttached proves the flip side: a
// tool whose schema does declare idempotency_key (whagent.IdempotencyKeyed)
// still gets FR11's deterministic key attached.
func TestDispatch_WriteTool_IdempotencyKeyAttached(t *testing.T) {
	ctx := context.Background()
	serverURL := newDispatchTestServer(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "write_probe", Arguments: `{}`}

	result, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	require.False(t, result.IsError, "unexpected tool error: %s", result.Content)

	wantKey := whagent.DeriveIdempotencyKey(in.Session.SessionID.String(), in.Turn, in.CallIndex)
	assert.Contains(t, result.Content, wantKey)
}

// TestDispatch_SearchMode_EmptyUnlocked_RefusesRealTool_NoConnect is FR9's
// core refusal proof: a search-mode call for a name the session has never
// unlocked is refused with the exact allowlist-refusal error shape, and
// resolveTarget's gate fires before any credential is minted or connection
// opened -- the fake server records zero HTTP hits and zero genuine
// read_probe invocations.
func TestDispatch_SearchMode_EmptyUnlocked_RefusesRealTool_NoConnect(t *testing.T) {
	ctx := context.Background()
	serverURL, httpHits, readCalls := newDispatchTestServerCounting(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Mode = session.ToolLoadingModeSearch
	in.Unlocked = nil
	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	_, err := dispatcher.Dispatch(ctx, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no configured server exposes this tool`)
	assert.Equal(t, int32(0), atomic.LoadInt32(httpHits), "a search-mode refusal must never open a connection")
	assert.Equal(t, int32(0), atomic.LoadInt32(readCalls), "a search-mode refusal must never reach the domain server's handler")
}

// TestDispatch_SearchMode_Unlocked_DispatchesNormally is the positive
// case: a name present in Unlocked dispatches exactly as bulk mode would.
func TestDispatch_SearchMode_Unlocked_DispatchesNormally(t *testing.T) {
	ctx := context.Background()
	serverURL := newDispatchTestServer(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Mode = session.ToolLoadingModeSearch
	in.Unlocked = []string{"read_probe"}
	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	result, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	assert.False(t, result.IsError, "unexpected tool error: %s", result.Content)
	assert.Contains(t, result.Content, "chan-1")
}

// TestDispatch_SearchMode_NotInUnlocked_Refused proves a non-empty but
// non-matching Unlocked set still refuses a call for a different name.
func TestDispatch_SearchMode_NotInUnlocked_Refused(t *testing.T) {
	ctx := context.Background()
	serverURL, httpHits, readCalls := newDispatchTestServerCounting(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Mode = session.ToolLoadingModeSearch
	in.Unlocked = []string{"write_probe"}
	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	_, err := dispatcher.Dispatch(ctx, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no configured server exposes this tool`)
	assert.Equal(t, int32(0), atomic.LoadInt32(httpHits))
	assert.Equal(t, int32(0), atomic.LoadInt32(readCalls))
}

// TestDispatch_SearchMode_SearchToolsCall_NotRejectedByModeGate proves
// resolveTarget's search-mode gate is not what rejects a search_tools
// call (that name is exempted from the gate outright): a call for
// tools.SearchToolsName still ultimately fails here, because no
// configured domain server actually exposes a real tool by that name
// (FR8's global reservation, search.go) and this fixture never wires a
// #2671-style in-process handler for it -- but it fails via the ordinary
// per-ref lookup loop further down, which is only ever reached once a
// connection has actually been attempted. A non-zero httpHits count is
// this test's proof the mode gate itself did not short-circuit the call.
func TestDispatch_SearchMode_SearchToolsCall_NotRejectedByModeGate(t *testing.T) {
	ctx := context.Background()
	serverURL, httpHits, _ := newDispatchTestServerCounting(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Mode = session.ToolLoadingModeSearch
	in.Unlocked = nil
	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: tools.SearchToolsName, Arguments: `{"query":"probe"}`}

	_, err := dispatcher.Dispatch(ctx, in)
	require.Error(t, err, "no configured server exposes search_tools as a real tool -- FR8's reservation, not this test's fixture")
	assert.Contains(t, err.Error(), `no configured server exposes this tool`)
	assert.NotEqual(t, int32(0), atomic.LoadInt32(httpHits),
		"a search_tools call must fall through resolveTarget's mode gate into the ordinary per-ref lookup loop, proven by a connection actually having been attempted")
}

// TestDispatch_SearchMode_UnlockedButNoLongerAllowlisted_StillRefused
// proves NFR2's composition rule: a name that is unlocked but no longer
// present in the matching ref's AllowedTools must still be refused, by
// isAllowed further down -- the search-mode gate above alone is not
// sufficient to let a call through.
func TestDispatch_SearchMode_UnlockedButNoLongerAllowlisted_StillRefused(t *testing.T) {
	ctx := context.Background()
	serverURL, httpHits, readCalls := newDispatchTestServerCounting(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)
	in.ToolSet = []session.ToolServerRef{{ServerURL: serverURL, AllowedTools: []string{"write_probe"}}}

	in.Mode = session.ToolLoadingModeSearch
	in.Unlocked = []string{"read_probe"}
	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	_, err := dispatcher.Dispatch(ctx, in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no configured server exposes this tool`)
	assert.Equal(t, int32(0), atomic.LoadInt32(httpHits),
		"isAllowed must refuse an unlocked-but-no-longer-allowlisted name before ever connecting (NFR2 composition)")
	assert.Equal(t, int32(0), atomic.LoadInt32(readCalls))
}

// TestDispatch_BulkMode_UnlockedSetIgnored proves the check is strictly
// mode-gated: a bulk-mode (zero-valued Mode) DispatchInput with a
// non-empty Unlocked set accidentally populated must behave exactly as
// today -- FR9's gate never applies outside search mode.
func TestDispatch_BulkMode_UnlockedSetIgnored(t *testing.T) {
	ctx := context.Background()
	serverURL := newDispatchTestServer(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Unlocked = []string{"some-other-name"}
	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	result, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	assert.False(t, result.IsError, "unexpected tool error: %s", result.Content)
	assert.Contains(t, result.Content, "chan-1")
}

// TestDispatch_MalformedArguments_IsErrorResultNotSessionFailure proves a
// model that emits unparseable tool arguments gets an ordinary IsError
// tool result back, so it can see what it did wrong and retry -- rather
// than a hard Go error, which propagates to failTurn and ends the whole
// session `failed` over one bad argument blob. That error would also be
// classified non_retryable, so retrying the session could never rescue it.
//
// This is the same handling SearchTools already gave a malformed query
// (worker/activities.go's decodeSearchQuery); this closes the gap for an
// ordinary domain tool, where the mistake is far more likely.
func TestDispatch_MalformedArguments_IsErrorResultNotSessionFailure(t *testing.T) {
	ctx := context.Background()
	serverURL, httpHits, readCalls := newDispatchTestServerCounting(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id": `} // truncated JSON

	result, err := dispatcher.Dispatch(ctx, in)

	require.NoError(t, err, "malformed arguments must not fail the dispatch; they are the model's mistake to correct")
	assert.True(t, result.IsError, "an undecodable argument blob must be reported to the model as a tool error")
	assert.Contains(t, result.Content, "arguments", "the model needs to be told what was wrong: %s", result.Content)
	assert.Equal(t, "call-1", result.ToolCallID, "the error result must stay bound to the call that produced it")
	assert.Equal(t, "read_probe", result.Name)

	// resolveTarget has already opened the MCP session and listed tools by
	// this point (it must, to check the name is callable), so HTTP hits
	// are expected. What must not have happened is the tool itself running.
	assert.Zero(t, *readCalls, "the tool must not be invoked with undecodable arguments")
	assert.Positive(t, *httpHits, "resolveTarget is expected to have connected before decoding")
}

// TestDispatch_ValidArguments_AreUnaffected is the counterweight: the
// malformed-arguments path must not have changed how a well-formed call
// dispatches.
func TestDispatch_ValidArguments_AreUnaffected(t *testing.T) {
	ctx := context.Background()
	serverURL := newDispatchTestServer(t)
	dispatcher, in := newDispatchTestFixture(t, serverURL)

	in.Turn = 1
	in.CallIndex = 0
	in.Call = llm.ToolCall{ID: "call-1", Name: "read_probe", Arguments: `{"channel_id":"chan-1"}`}

	result, err := dispatcher.Dispatch(ctx, in)
	require.NoError(t, err)
	assert.False(t, result.IsError, "a valid call must dispatch normally: %s", result.Content)
	assert.Contains(t, result.Content, "chan-1")
}
