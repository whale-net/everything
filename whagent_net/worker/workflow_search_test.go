// Issue #2671's Testing phase: dispatchToolCall's workflow-level routing
// between ActivitySearchTools and ActivityDispatchTool -- exercised through
// SessionWorkflow via testsuite.TestWorkflowEnvironment, the same pattern
// workflow_caps_test.go establishes for issue #2119 (mocked activities,
// tracked call sequences, no live Temporal server or Postgres).
package main

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// TestSessionWorkflow_SearchMode_SearchToolsCall_ExecutesActivitySearchTools_NotDispatchTool
// proves dispatchToolCall's core routing rule (FR4): in a search-mode
// session, a model-requested search_tools call executes ActivitySearchTools,
// never ActivityDispatchTool -- the call is answered entirely in-process,
// never sent toward a domain server.
func TestSessionWorkflow_SearchMode_SearchToolsCall_ExecutesActivitySearchTools_NotDispatchTool(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{
		MaxTurns:          100,
		MaxCostUSD:        100,
		MaxToolIterations: 100,
		ToolLoadingMode:   session.ToolLoadingModeSearch,
	}
	wireCapTestActivities(env, def, tracker, rec)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	callModelFn, _ := sequencedCallModel([]llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{
				{ID: "call-1", Name: tools.SearchToolsName, Arguments: `{"query":"widgets"}`},
			},
		},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	})
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).Return(callModelFn)

	var mu sync.Mutex
	var searchCalls, dispatchCalls int
	var lastSearchIn SearchToolsInput
	env.OnActivity(ActivitySearchTools, mock.Anything, mock.Anything).
		Return(SearchToolsResult{Matched: []string{"list_widgets"}}, nil).
		Run(func(args mock.Arguments) {
			mu.Lock()
			defer mu.Unlock()
			searchCalls++
			lastSearchIn = args.Get(1).(SearchToolsInput)
		})
	env.OnActivity(ActivityDispatchTool, mock.Anything, mock.Anything).
		Return(DispatchToolResult{}, nil).
		Run(func(args mock.Arguments) {
			mu.Lock()
			defer mu.Unlock()
			dispatchCalls++
		})
	env.OnActivity(ActivityCommitToolLoopIteration, mock.Anything, mock.Anything).
		Return(CommitToolLoopIterationResult{}, nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "find me a tool"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, searchCalls, "a search-mode search_tools call must execute ActivitySearchTools exactly once")
	assert.Equal(t, 0, dispatchCalls, "a search-mode search_tools call must never execute ActivityDispatchTool")
	assert.Equal(t, tools.SearchToolsName, lastSearchIn.Call.Name)
	assert.Equal(t, 0, lastSearchIn.CallIndex)
	assert.Equal(t, 1, lastSearchIn.Turn)
}

// TestSessionWorkflow_SearchMode_DomainToolCall_ExecutesActivityDispatchTool_NotSearchTools
// is the other half of dispatchToolCall's routing rule: a search-mode
// session's model-requested call for an ordinary (already-unlocked) domain
// tool still executes ActivityDispatchTool, exactly as a bulk-mode session
// always has -- only a literal search_tools call is special-cased.
func TestSessionWorkflow_SearchMode_DomainToolCall_ExecutesActivityDispatchTool_NotSearchTools(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{
		MaxTurns:          100,
		MaxCostUSD:        100,
		MaxToolIterations: 100,
		ToolLoadingMode:   session.ToolLoadingModeSearch,
	}
	wireCapTestActivities(env, def, tracker, rec)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	callModelFn, _ := sequencedCallModel([]llm.Response{
		{
			Message:   llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "list_widgets", Arguments: "{}"}},
		},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	})
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).Return(callModelFn)

	var mu sync.Mutex
	var searchCalls, dispatchCalls int
	env.OnActivity(ActivitySearchTools, mock.Anything, mock.Anything).
		Return(SearchToolsResult{}, nil).
		Run(func(args mock.Arguments) {
			mu.Lock()
			defer mu.Unlock()
			searchCalls++
		})
	env.OnActivity(ActivityDispatchTool, mock.Anything, mock.Anything).
		Return(DispatchToolResult{}, nil).
		Run(func(args mock.Arguments) {
			mu.Lock()
			defer mu.Unlock()
			dispatchCalls++
		})
	env.OnActivity(ActivityCommitToolLoopIteration, mock.Anything, mock.Anything).
		Return(CommitToolLoopIterationResult{}, nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "delete the widget"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, dispatchCalls, "an ordinary domain tool call must still execute ActivityDispatchTool")
	assert.Equal(t, 0, searchCalls, "an ordinary domain tool call must never execute ActivitySearchTools")
}
