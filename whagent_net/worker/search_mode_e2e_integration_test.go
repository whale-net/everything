//go:build integration

// See //whagent_net/session:session_integration_test's BUILD comment for
// why this file only builds under the "integration" tag (real Postgres via
// dbtest, requires Docker) and is excluded from `bazel test //...`.
//
// Root plan #2602's milestone closeout (issue #2674): the one end-to-end
// exercise spanning FR1-FR10/NFR1-NFR2's interactions no single task's own
// tests reach, through SessionWorkflow via testsuite.TestWorkflowEnvironment
// -- the same testsuite pattern whagent_net/worker/workflow_search_test.go
// (issue #2671) and whagent_net/worker/workflow_caps_test.go (issue #2119)
// already establish, but with REAL activities (this file's
// registerE2EActivities, backed by newTestStore's real Postgres and an
// in-process fake MCP server, search_tools_integration_test.go's own
// fixture style) standing in for every activity except ActivityCallModel,
// which is scripted (scriptedCallModel below) since there is no live model
// provider to call in a hermetic test. This is what makes this file an
// end-to-end test rather than a workflow-routing test (workflow_search_
// test.go) or an activity-level test (search_tools_integration_test.go,
// context_integration_test.go): the same real ListToolDefinitions/
// SearchTools/DispatchTool/BuildContext/CommitTurn/UnlockedTools code a
// production worker runs, driven by SessionWorkflow's own real control
// flow, against a real transcript.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// modelPtr is the small *string helper session.AgentDefinition.Model needs
// -- this file's own copy, since the sibling helpers in workflow_test.go/
// workflow_caps_test.go live in a different go_test target (worker_test,
// hermetic) that this integration-tagged file is never compiled alongside.
func modelPtr(m string) *string { return &m }

// toolCallCounter counts real CallTool invocations the fake MCP server
// (newE2ESearchModeServer) below receives, per tool name -- the same "did
// the server actually see a dispatch" signal search_tools_integration_
// test.go's callToolCount already uses, generalized to more than one tool
// name so a single fixture can prove both "this call reached the server"
// and "this OTHER call never did" (FR4's search_tools carve-out, FR9's
// refusal-before-dispatch guarantee) in the same test.
type toolCallCounter struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newToolCallCounter() *toolCallCounter {
	return &toolCallCounter{counts: make(map[string]int64)}
}

func (c *toolCallCounter) inc(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[name]++
}

func (c *toolCallCounter) count(name string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}

// newE2ESearchModeServer starts a plain, unauthenticated in-process MCP
// server exposing four domain tools this file's tests script against:
//
//   - list_widgets / delete_gadget: ordinary domain tools, matched by the
//     "widget"/"gadget" search queries the tests below use.
//   - inspect_sensor: allowlisted but never referenced by any search query
//     in this file -- the FR9 refusal target (an allowlisted tool that is
//     never unlocked because it was never searched for).
//   - hidden_gizmo: NOT allowlisted (excluded from every agent definition's
//     AllowedTools below), but its description also contains "widget" --
//     if AllowedTools narrowing were broken, a "widget" search would
//     incorrectly match it too. This is what makes the NFR2 assertion (it
//     never appears in a search match or in Tools) an actual proof rather
//     than a vacuous one.
func newE2ESearchModeServer(t *testing.T) (string, *toolCallCounter) {
	t.Helper()

	counts := newToolCallCounter()
	srv := mcp.NewServer(&mcp.Implementation{Name: "e2e-search-mode-test-server", Version: "0.1.0"}, nil)

	register := func(name, description string) {
		mcp.AddTool(srv, &mcp.Tool{Name: name, Description: description},
			func(_ context.Context, _ *mcp.CallToolRequest, _ searchProbeInput) (*mcp.CallToolResult, searchProbeOutput, error) {
				counts.inc(name)
				return nil, searchProbeOutput{}, nil
			})
	}
	register("list_widgets", "List every widget in the catalog.")
	register("delete_gadget", "Remove a gadget from inventory.")
	register("inspect_sensor", "Inspect environmental sensor readings. This fixture never searches for it, so it can only ever be reached by name, never by search.")
	register("hidden_gizmo", "A secret gizmo whose description also mentions widget, but must never be reachable -- excluded from every agent definition's allowed_tools in this fixture.")

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL, counts
}

// newReservedNameMCPServer starts a server whose own catalog illegally
// exposes a tool literally named tools.SearchToolsName -- FR8's global
// reservation, checked once inside candidateDefinitions
// (whagent_net/worker/tools/listdefs.go) for every agent definition,
// bulk or search mode alike.
func newReservedNameMCPServer(t *testing.T) string {
	t.Helper()

	srv := mcp.NewServer(&mcp.Implementation{Name: "e2e-reserved-name-test-server", Version: "0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{Name: tools.SearchToolsName, Description: "a real domain tool illegally named search_tools"},
		func(_ context.Context, _ *mcp.CallToolRequest, _ searchProbeInput) (*mcp.CallToolResult, searchProbeOutput, error) {
			return nil, searchProbeOutput{}, nil
		})
	mcp.AddTool(srv, &mcp.Tool{Name: "ordinary_tool", Description: "an otherwise unremarkable tool."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ searchProbeInput) (*mcp.CallToolResult, searchProbeOutput, error) {
			return nil, searchProbeOutput{}, nil
		})

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// activityHolder is a swappable indirection layer over a live *Activities
// value: every activity this file registers (registerE2EActivities)
// delegates through holder.get() rather than closing over a fixed
// *Activities directly, so the durability test below
// (TestSessionWorkflow_SearchMode_WorkerRestartBetweenTurns_
// UnlockedSetRederivedFromTranscript) can swap in a completely different,
// freshly-constructed *Activities value mid-execution -- simulating a
// worker restart between two turns of the SAME session -- without touching
// SessionWorkflow's own by-name activity dispatch (main.go's contract).
// Every other test in this file just wraps a single fixed *Activities and
// never calls swap.
type activityHolder struct {
	mu  sync.Mutex
	act *Activities
}

func newActivityHolder(a *Activities) *activityHolder {
	return &activityHolder{act: a}
}

func (h *activityHolder) get() *Activities {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.act
}

func (h *activityHolder) swap(a *Activities) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.act = a
}

// activityCounters is a thread-safe per-activity-name invocation counter,
// used below to prove a negative ("bulk mode never executes
// ActivityUnlockedTools/ActivitySearchTools at all") directly, rather than
// only inferring it from the absence of a tool_unlock transcript event.
type activityCounters struct {
	mu     sync.Mutex
	counts map[string]int
}

func newActivityCounters() *activityCounters {
	return &activityCounters{counts: make(map[string]int)}
}

func (c *activityCounters) inc(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[name]++
}

func (c *activityCounters) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[name]
}

// registerE2EActivities registers every real (non-CallModel) activity
// SessionWorkflow drives, under its production name (activities.go's
// Activity* constants -- the exact contract main.go's own registration
// follows), delegating each call to holder's CURRENT *Activities value and
// tallying the call into counters. ActivityCallModel is deliberately never
// registered here -- every test below registers its own scripted
// implementation (scriptedCallModel), since there is no live model
// provider to call in a hermetic test.
//
// onStatus, when non-nil, is invoked synchronously from inside the
// UpdateSessionStatus wrapper immediately after the real status write
// succeeds -- the one hook the durability test uses to swap
// activityHolder's value at a precise WORKFLOW EVENT (turn 1's own
// awaiting_input write completing) rather than guessing from wall-clock/
// mock-clock timing.
func registerE2EActivities(env *testsuite.TestWorkflowEnvironment, holder *activityHolder, counters *activityCounters, onStatus func(session.Status)) {
	env.RegisterActivityWithOptions(func(ctx context.Context, sessionID uuid.UUID) (ResolveAgentDefinitionResult, error) {
		counters.inc(ActivityResolveAgentDefinition)
		return holder.get().ResolveAgentDefinition(ctx, sessionID)
	}, activity.RegisterOptions{Name: ActivityResolveAgentDefinition})

	env.RegisterActivityWithOptions(func(ctx context.Context, in BuildContextInput) (BuildContextResult, error) {
		counters.inc(ActivityBuildContext)
		return holder.get().BuildContext(ctx, in)
	}, activity.RegisterOptions{Name: ActivityBuildContext})

	env.RegisterActivityWithOptions(func(ctx context.Context, in CommitTurnInput) (CommitTurnResult, error) {
		counters.inc(ActivityCommitTurn)
		return holder.get().CommitTurn(ctx, in)
	}, activity.RegisterOptions{Name: ActivityCommitTurn})

	env.RegisterActivityWithOptions(func(ctx context.Context, in UpdateSessionStatusInput) (UpdateSessionStatusResult, error) {
		counters.inc(ActivityUpdateSessionStatus)
		res, err := holder.get().UpdateSessionStatus(ctx, in)
		if err == nil && onStatus != nil {
			onStatus(in.Status)
		}
		return res, err
	}, activity.RegisterOptions{Name: ActivityUpdateSessionStatus})

	env.RegisterActivityWithOptions(func(ctx context.Context, in SumCostInput) (SumCostResult, error) {
		counters.inc(ActivitySumCost)
		return holder.get().SumCost(ctx, in)
	}, activity.RegisterOptions{Name: ActivitySumCost})

	env.RegisterActivityWithOptions(func(ctx context.Context, in CommitTerminalEventInput) (CommitTerminalEventResult, error) {
		counters.inc(ActivityCommitTerminalEvent)
		return holder.get().CommitTerminalEvent(ctx, in)
	}, activity.RegisterOptions{Name: ActivityCommitTerminalEvent})

	env.RegisterActivityWithOptions(func(ctx context.Context, in ListToolDefinitionsInput) (ListToolDefinitionsResult, error) {
		counters.inc(ActivityListToolDefinitions)
		return holder.get().ListToolDefinitions(ctx, in)
	}, activity.RegisterOptions{Name: ActivityListToolDefinitions})

	env.RegisterActivityWithOptions(func(ctx context.Context, in DispatchToolInput) (DispatchToolResult, error) {
		counters.inc(ActivityDispatchTool)
		return holder.get().DispatchTool(ctx, in)
	}, activity.RegisterOptions{Name: ActivityDispatchTool})

	env.RegisterActivityWithOptions(func(ctx context.Context, in CommitToolLoopIterationInput) (CommitToolLoopIterationResult, error) {
		counters.inc(ActivityCommitToolLoopIteration)
		return holder.get().CommitToolLoopIteration(ctx, in)
	}, activity.RegisterOptions{Name: ActivityCommitToolLoopIteration})

	env.RegisterActivityWithOptions(func(ctx context.Context, in UnlockedToolsInput) (UnlockedToolsResult, error) {
		counters.inc(ActivityUnlockedTools)
		return holder.get().UnlockedTools(ctx, in)
	}, activity.RegisterOptions{Name: ActivityUnlockedTools})

	env.RegisterActivityWithOptions(func(ctx context.Context, in SearchToolsInput) (SearchToolsResult, error) {
		counters.inc(ActivitySearchTools)
		return holder.get().SearchTools(ctx, in)
	}, activity.RegisterOptions{Name: ActivitySearchTools})
}

// callModelRecorder captures every CallModelInput a scripted CallModel
// implementation (scriptedCallModel below) receives, in call order -- this
// file's tests inspect .Tools off these to assert exactly what each model
// call was offered (FR3/FR6/FR7/FR10), which a mocked activity with
// testify's .Run() hook could also do, but this file uses none of testify's
// mock package: every activity here is a real (or, for CallModel, a
// scripted-but-real-shaped) Go function registered directly, not a mock
// expectation.
type callModelRecorder struct {
	mu  sync.Mutex
	ins []CallModelInput
}

func (r *callModelRecorder) record(in CallModelInput) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ins = append(r.ins, in)
}

func (r *callModelRecorder) snapshot() []CallModelInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]CallModelInput, len(r.ins))
	copy(out, r.ins)
	return out
}

// scriptedCallModel returns a CallModel implementation that records every
// call it receives into rec, then returns responses in order -- one
// element of responses is consumed per call, panicking (via a returned
// error) if the script under-provisions calls, so an unexpectedly-long
// tool loop fails loudly rather than silently reusing a stale response.
// Every response's Usage.ProviderCostUSD is expected to be set by the
// caller (costPtr, tool_loop_integration_test.go) -- CommitTurn/
// CommitToolLoopIteration's resolveCost fails outright otherwise, since
// this file never configures Activities.Prices.
func scriptedCallModel(rec *callModelRecorder, responses []llm.Response) func(context.Context, CallModelInput) (CallModelResult, error) {
	var i int
	var mu sync.Mutex
	return func(_ context.Context, in CallModelInput) (CallModelResult, error) {
		rec.record(in)
		mu.Lock()
		defer mu.Unlock()
		if i >= len(responses) {
			return CallModelResult{}, fmt.Errorf("scriptedCallModel: exhausted scripted responses after %d calls", i)
		}
		resp := responses[i]
		i++
		return CallModelResult{Response: resp}, nil
	}
}

// toolDefNames extracts just the names off a []llm.ToolDefinition, in
// order -- what every Tools-shape assertion below compares against,
// keeping the assertions themselves free of Parameters/Description noise.
func toolDefNames(defs []llm.ToolDefinition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

// queryToolUnlockNames reads back a single tool_unlock:<callIndex> event's
// ToolNames for (sessionID, turn) directly from Postgres -- the same shape
// of direct-SQL assertion search_tools_integration_test.go's own tests use,
// reused here at the end-to-end level to check exactly what a turn's
// search_tools call actually matched, independent of what CallModelInput
// captured (that only shows what a LATER turn was subsequently offered).
func queryToolUnlockNames(t *testing.T, ctx context.Context, db *dbtest.Postgres, sessionID uuid.UUID, turn, callIndex int) []string {
	t.Helper()
	var raw json.RawMessage
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT payload FROM transcript_event WHERE session_id = $1 AND turn = $2 AND type = $3
	`, sessionID, turn, toolUnlockEventType(callIndex)).Scan(&raw))
	var payload toolUnlockEventPayload
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload.ToolNames
}

// e2eSearchAgentDefinition builds, upserts, and assigns a search-mode
// agent definition pointed at serverURL's three allowlisted domain tools
// (list_widgets/delete_gadget/inspect_sensor -- never hidden_gizmo) to
// sess, under agentID -- the shared fixture setup every search-mode test
// below starts from.
func e2eSearchAgentDefinition(t *testing.T, ctx context.Context, store *session.Store, sess *session.Session, agentID, serverURL string) {
	t.Helper()
	def := session.AgentDefinition{
		AgentID:           agentID,
		Version:           1,
		Model:             modelPtr("test-model"),
		ToolSet:           []session.ToolServerRef{{ServerURL: serverURL, AllowedTools: []string{"list_widgets", "delete_gadget", "inspect_sensor"}}},
		MaxTurns:          100,
		MaxCostUSD:        100,
		MaxToolIterations: 20,
		ToolLoadingMode:   session.ToolLoadingModeSearch,
	}
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, &def))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.AgentID, def.Version))
}

// TestSessionWorkflow_SearchMode_FullLifecycle_EndToEnd is this milestone's
// core end-to-end proof: one search-mode session across four turns,
// exercised through SessionWorkflow with real ListToolDefinitions/
// SearchTools/DispatchTool/BuildContext/CommitTurn activities against a
// real transcript and a real (fake) MCP server.
//
//   - Turn 1: Tools carries exactly search_tools (FR3). The model searches
//     "widget"; the search matches list_widgets only (never hidden_gizmo,
//     excluded from AllowedTools despite its description also containing
//     "widget" -- NFR2).
//   - Turn 2: Tools is [search_tools, list_widgets] (FR6/FR7). The model
//     dispatches list_widgets, which actually reaches the fake server.
//   - Turn 3: turn 3's OWN search ("gadget") has not landed yet by the time
//     its Tools is resolved (UnlockedTools runs before this turn's own
//     search_tools call), so turn 3's Tools is BYTE-IDENTICAL to turn 2's
//     -- the root-plan #2602 scope note's prompt-cache prefix-stability
//     guarantee.
//   - Turn 4: a plain, tool-call-free turn added purely to observe the
//     CUMULATIVE effect of turn 3's search: Tools is now
//     [search_tools, list_widgets, delete_gadget] -- delete_gadget
//     APPENDED after list_widgets, never reordering it.
func TestSessionWorkflow_SearchMode_FullLifecycle_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	serverURL, calls := newE2ESearchModeServer(t)
	e2eSearchAgentDefinition(t, ctx, store, sess, "e2e-search-lifecycle-agent", serverURL)

	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	holder := newActivityHolder(newSearchToolsTestActivities(t, store))
	registerE2EActivities(env, holder, newActivityCounters(), nil)

	rec := &callModelRecorder{}
	env.RegisterActivityWithOptions(scriptedCallModel(rec, []llm.Response{
		// Turn 1: search for "widget", then a final tool-call-free response.
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c1", Name: tools.SearchToolsName, Arguments: `{"query":"widget"}`}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "found a widget tool"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		// Turn 2: dispatch the now-unlocked list_widgets, then finish.
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c2", Name: "list_widgets", Arguments: "{}"}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "listed widgets"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		// Turn 3: search again, for "gadget", then finish.
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c3", Name: tools.SearchToolsName, Arguments: `{"query":"gadget"}`}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "found a gadget tool"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		// Turn 4: plain response, no tool calls -- only here to observe the
		// cumulative Tools turn 3's search produced.
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "all done"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
	}), activity.RegisterOptions{Name: ActivityCallModel})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "find a widget tool"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "use it"})
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "find a gadget tool"})
	}, 3*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "anything else?"})
	}, 4*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 5*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: sess.SessionID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	ins := rec.snapshot()
	require.Len(t, ins, 7, "two CallModel calls for each of turns 1-3 (the search, then the final response), one for turn 4's single final-only response")

	assert.Equal(t, []string{tools.SearchToolsName}, toolDefNames(ins[0].Tools), "turn 1 must offer exactly search_tools (FR3)")
	assert.Equal(t, ins[0].Tools, ins[1].Tools, "both of turn 1's own loop iterations must see the identical Tools")

	assert.Equal(t, []string{tools.SearchToolsName, "list_widgets"}, toolDefNames(ins[2].Tools), "turn 2 must offer search_tools plus what turn 1's search unlocked, in unlock order (FR6/FR7)")
	assert.Equal(t, ins[2].Tools, ins[3].Tools)

	assert.Equal(t, ins[2].Tools, ins[4].Tools, "turn 3's Tools must be byte-identical to turn 2's Tools -- turn 3's own search has not landed yet when its Tools was resolved (prompt-cache prefix stability, root plan #2602)")
	assert.Equal(t, ins[4].Tools, ins[5].Tools)

	assert.Equal(t, []string{tools.SearchToolsName, "list_widgets", "delete_gadget"}, toolDefNames(ins[6].Tools), "turn 4 must show delete_gadget appended AFTER list_widgets, never reordering it")
	assert.Equal(t, ins[4].Tools, ins[6].Tools[:2], "turn 4's Tools prefix must still be byte-identical to turn 3's Tools")

	for i, in := range ins {
		for _, d := range in.Tools {
			assert.NotEqual(t, "hidden_gizmo", d.Name, "call %d must never offer hidden_gizmo (NFR2: excluded from allowed_tools, even though its description also matches the \"widget\" query)", i)
		}
	}

	turn1Matched := queryToolUnlockNames(t, ctx, db, sess.SessionID, 1, 0)
	assert.Equal(t, []string{"list_widgets"}, turn1Matched, "turn 1's search for \"widget\" must match only list_widgets -- never hidden_gizmo, which is excluded from allowed_tools (NFR2)")

	turn3Matched := queryToolUnlockNames(t, ctx, db, sess.SessionID, 3, 0)
	assert.Equal(t, []string{"delete_gadget"}, turn3Matched, "turn 3's search for \"gadget\" must match only delete_gadget")

	assert.Equal(t, int64(1), calls.count("list_widgets"), "list_widgets must have actually been dispatched to the domain server exactly once, on turn 2")
	assert.Equal(t, int64(0), calls.count("delete_gadget"), "delete_gadget was only ever searched for, never dispatched, in this script")
	assert.Equal(t, int64(0), calls.count("hidden_gizmo"), "hidden_gizmo must never be dispatched (NFR2)")
}

// TestSessionWorkflow_SearchMode_DispatchNeverSearchedTool_RefusedButSessionEndsCleanly
// is FR9's end-to-end proof: inspect_sensor is allowlisted on this agent
// definition, but this script's only search query ("widget") never matches
// it, so it is never unlocked. When the model then tries to call it
// directly on turn 2, dispatchToolCall's real routing (workflow.go) ->
// DispatchTool -> tools.Dispatcher.Dispatch's resolveTarget (dispatch.go)
// refuses it with "no configured server exposes this tool" BEFORE any
// credential is minted or the domain server is ever contacted. That refusal
// surfaces as an ordinary, controlled session failure (failTurn's doc
// comment: "a session ending failed is a legitimate terminal business
// outcome this workflow execution completes normally over, not a Temporal
// workflow execution failure") -- SessionWorkflow itself still completes
// with no GetWorkflowError, which is the sense in which the session
// "continues" rather than crashing the workflow execution.
func TestSessionWorkflow_SearchMode_DispatchNeverSearchedTool_RefusedButSessionEndsCleanly(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	serverURL, calls := newE2ESearchModeServer(t)
	e2eSearchAgentDefinition(t, ctx, store, sess, "e2e-search-refusal-agent", serverURL)

	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	holder := newActivityHolder(newSearchToolsTestActivities(t, store))
	registerE2EActivities(env, holder, newActivityCounters(), nil)

	rec := &callModelRecorder{}
	env.RegisterActivityWithOptions(scriptedCallModel(rec, []llm.Response{
		// Turn 1: search only for "widget" -- inspect_sensor is allowlisted
		// but never matched, so it is never unlocked.
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c1", Name: tools.SearchToolsName, Arguments: `{"query":"widget"}`}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		// Turn 2: the model tries to call inspect_sensor directly, even
		// though it was never offered/unlocked -- FR9's refusal gate.
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c2", Name: "inspect_sensor", Arguments: "{}"}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
	}), activity.RegisterOptions{Name: ActivityCallModel})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "find a widget tool"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "inspect the sensor"})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: sess.SessionID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a refused tool call is a controlled session failure, not a Temporal workflow execution error")

	got, err := store.Sessions().GetByID(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, session.StatusFailed, got.Status)
	require.NotNil(t, got.ErrorDetail)
	assert.Contains(t, *got.ErrorDetail, "no configured server exposes this tool")
	assert.Equal(t, int64(0), calls.count("inspect_sensor"), "a refused call must never reach the domain server (FR9) -- the refusal happens before any credential is minted or connection opened")
}

// TestSessionWorkflow_BulkMode_ControlCase_EndToEnd is FR2's session-level
// control case: the SAME tool set (minus search_tools, which a bulk
// definition never offers at all) run through the identical real-activity
// SessionWorkflow harness as the search-mode tests above, but with a
// bulk-mode (zero-valued ToolLoadingMode) agent definition. Turn 1 already
// carries the full allowed tool set (never staged in behind search_tools),
// no tool_unlock event is ever committed, ActivityUnlockedTools/
// ActivitySearchTools are never executed at all, and the turn's
// turn_context is still bounded by maxContextEvents -- FR10's search-mode-
// only budget never applies here.
func TestSessionWorkflow_BulkMode_ControlCase_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	serverURL, calls := newE2ESearchModeServer(t)

	def := session.AgentDefinition{
		AgentID:           "e2e-bulk-control-agent",
		Version:           1,
		Model:             modelPtr("test-model"),
		ToolSet:           []session.ToolServerRef{{ServerURL: serverURL, AllowedTools: []string{"list_widgets", "delete_gadget", "inspect_sensor"}}},
		MaxTurns:          100,
		MaxCostUSD:        100,
		MaxToolIterations: 20,
		// ToolLoadingMode left at its zero value -- bulk, FR2's default.
	}
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, &def))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.AgentID, def.Version))

	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	holder := newActivityHolder(newSearchToolsTestActivities(t, store))
	counters := newActivityCounters()
	registerE2EActivities(env, holder, counters, nil)

	rec := &callModelRecorder{}
	env.RegisterActivityWithOptions(scriptedCallModel(rec, []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "delete_gadget", Arguments: "{}"}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
	}), activity.RegisterOptions{Name: ActivityCallModel})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "delete the gadget"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: sess.SessionID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	ins := rec.snapshot()
	require.Len(t, ins, 2)
	for i, in := range ins {
		assert.ElementsMatch(t, []string{"list_widgets", "delete_gadget", "inspect_sensor"}, toolDefNames(in.Tools),
			"call %d must already carry the FULL allowed tool set on turn 1, unaffected by search-mode's staged rollout (FR2)", i)
		for _, d := range in.Tools {
			assert.NotEqual(t, tools.SearchToolsName, d.Name, "bulk mode must never offer search_tools")
		}
	}

	assert.Equal(t, int64(1), calls.count("delete_gadget"))

	var unlockCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT count(*) FROM transcript_event WHERE session_id = $1 AND type LIKE 'tool_unlock:%'
	`, sess.SessionID).Scan(&unlockCount))
	assert.Equal(t, 0, unlockCount, "a bulk-mode session must never commit a tool_unlock event")

	assert.Equal(t, 0, counters.get(ActivityUnlockedTools), "bulk mode must never execute ActivityUnlockedTools")
	assert.Equal(t, 0, counters.get(ActivitySearchTools), "bulk mode must never execute ActivitySearchTools")

	var eventIDCount int
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT array_length(event_ids, 1) FROM turn_context WHERE session_id = $1 AND turn = 1
	`, sess.SessionID).Scan(&eventIDCount))
	assert.LessOrEqual(t, eventIDCount, maxContextEvents, "a bulk-mode turn's context must still be bounded by maxContextEvents, unaffected by FR10's search-mode-only budget")
}

// e2eReservedNameFailsLoudly is the shared body for the two FR8 end-to-end
// tests below (bulk and search mode): a server that illegally exposes a
// tool literally named search_tools must fail the very first turn loudly,
// before ActivityCallModel ever runs -- so ActivityCallModel is registered
// here as a poison pill, counted rather than allowed to actually respond.
func e2eReservedNameFailsLoudly(t *testing.T, mode session.ToolLoadingMode) {
	t.Helper()
	ctx := context.Background()
	store, _ := newTestStore(t)
	sess := newTestSessionRow(t, ctx, store)
	serverURL := newReservedNameMCPServer(t)

	def := session.AgentDefinition{
		AgentID:           "e2e-reserved-name-agent-" + string(mode),
		Version:           1,
		Model:             modelPtr("test-model"),
		ToolSet:           []session.ToolServerRef{{ServerURL: serverURL}},
		MaxTurns:          100,
		MaxCostUSD:        100,
		MaxToolIterations: 20,
		ToolLoadingMode:   mode,
	}
	require.NoError(t, store.AgentDefinitions().Upsert(ctx, &def))
	require.NoError(t, store.AgentDefinitions().AssignToSession(ctx, sess.SessionID, def.AgentID, def.Version))

	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	holder := newActivityHolder(newSearchToolsTestActivities(t, store))
	registerE2EActivities(env, holder, newActivityCounters(), nil)

	var callModelInvocations int32
	env.RegisterActivityWithOptions(func(_ context.Context, _ CallModelInput) (CallModelResult, error) {
		atomic.AddInt32(&callModelInvocations, 1)
		return CallModelResult{Response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "unreachable"}}}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "hello"})
	}, time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: sess.SessionID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	got, err := store.Sessions().GetByID(ctx, sess.SessionID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, session.StatusFailed, got.Status)
	require.NotNil(t, got.ErrorDetail)
	assert.Contains(t, *got.ErrorDetail, "reserved tool name")
	assert.Equal(t, int32(0), atomic.LoadInt32(&callModelInvocations), "CallModel must never run once ListToolDefinitions has already failed the turn (FR8)")
}

// TestSessionWorkflow_BulkMode_ServerExposingReservedName_FailsLoudly is
// FR8's end-to-end proof for a bulk-mode definition: ActivityListTool
// Definitions runs unconditionally ahead of CallModel and fails the turn.
func TestSessionWorkflow_BulkMode_ServerExposingReservedName_FailsLoudly(t *testing.T) {
	e2eReservedNameFailsLoudly(t, session.ToolLoadingModeBulk)
}

// TestSessionWorkflow_SearchMode_ServerExposingReservedName_FailsLoudly is
// FR8's end-to-end proof for a search-mode definition: the SAME reserved-
// name check runs from inside the search-mode ActivityListToolDefinitions
// call (ahead of ActivityBuildContext, FR10's reordering), so the turn
// fails just as loudly there too.
func TestSessionWorkflow_SearchMode_ServerExposingReservedName_FailsLoudly(t *testing.T) {
	e2eReservedNameFailsLoudly(t, session.ToolLoadingModeSearch)
}

// TestSessionWorkflow_SearchMode_WorkerRestartBetweenTurns_UnlockedSetRederivedFromTranscript
// is FR5/FR6's durability guard: turn 1 runs entirely under activitiesA
// (its own *session.Store, its own *pgxpool.Pool); the moment turn 1's own
// awaiting_input status write lands (onStatus below, fired synchronously
// from inside the real UpdateSessionStatus activity), activityHolder is
// swapped to activitiesB -- a COMPLETELY independent *Activities value,
// built from a freshly dialed *pgxpool.Pool against the SAME underlying
// database, sharing no Go-level state with activitiesA whatsoever. Turn 2
// then runs entirely under activitiesB. The only way turn 2's
// ActivityUnlockedTools/ActivityListToolDefinitions calls (now answered by
// activitiesB, which has never touched this session before) could possibly
// know that turn 1 unlocked list_widgets is by reading it back out of the
// transcript in Postgres -- proving the unlocked set is durable across
// exactly the kind of worker-process restart a real deploy would produce,
// not cached anywhere in workflow or activity-process memory.
func TestSessionWorkflow_SearchMode_WorkerRestartBetweenTurns_UnlockedSetRederivedFromTranscript(t *testing.T) {
	ctx := context.Background()
	storeA, db := newTestStore(t)
	sess := newTestSessionRow(t, ctx, storeA)
	serverURL, calls := newE2ESearchModeServer(t)
	e2eSearchAgentDefinition(t, ctx, storeA, sess, "e2e-durability-agent", serverURL)

	pool2, err := pgxpool.New(ctx, db.ConnString)
	require.NoError(t, err)
	t.Cleanup(pool2.Close)
	storeB := session.New(pool2, nil)

	activitiesA := newSearchToolsTestActivities(t, storeA)
	activitiesB := newSearchToolsTestActivities(t, storeB)
	holder := newActivityHolder(activitiesA)

	var awaitingInputCount int32
	onStatus := func(status session.Status) {
		if status != session.StatusAwaitingInput {
			return
		}
		// The FIRST awaiting_input write is SessionWorkflow's very first
		// act, before any turn has run at all; the SECOND is turn 1
		// finishing and the session going idle again -- exactly the point a
		// real worker restart between turns would land.
		if atomic.AddInt32(&awaitingInputCount, 1) == 2 {
			holder.swap(activitiesB)
		}
	}

	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerE2EActivities(env, holder, newActivityCounters(), onStatus)

	rec := &callModelRecorder{}
	env.RegisterActivityWithOptions(scriptedCallModel(rec, []llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c1", Name: tools.SearchToolsName, Arguments: `{"query":"widget"}`}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "c2", Name: "list_widgets", Arguments: "{}"}}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}, Usage: llm.UsageReport{ProviderCostUSD: costPtr(0)}},
	}), activity.RegisterOptions{Name: ActivityCallModel})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "find a widget tool"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "use it"})
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 3*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: sess.SessionID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	ins := rec.snapshot()
	require.Len(t, ins, 4)
	assert.Equal(t, []string{tools.SearchToolsName, "list_widgets"}, toolDefNames(ins[2].Tools),
		"turn 2's Tools, resolved entirely by activitiesB (the rebuilt activity environment), must still include what turn 1 (run under activitiesA) unlocked -- proving the unlocked set is re-derived from the transcript, not from any in-process cache")

	assert.Equal(t, int64(1), calls.count("list_widgets"), "the dispatched call must have actually reached the domain server, under the rebuilt activity environment")

	got, err := storeA.Sessions().GetByID(ctx, sess.SessionID)
	require.NoError(t, err)
	assert.Equal(t, session.StatusStopped, got.Status)
}
