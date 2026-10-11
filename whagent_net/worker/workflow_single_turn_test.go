// Single-turn done, post-final turn-cap evaluation, and the cost-cap
// precedence rule, exercised through SessionWorkflow via
// testsuite.TestWorkflowEnvironment (mocked activities, no live Temporal).
package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// wireSumCostStepped makes SumCost report zero on its first call (the
// turn's "before" check) and afterCost on every later call (the "after"
// check and any tool-loop re-check).
func wireSumCostStepped(env *testsuite.TestWorkflowEnvironment, afterCost float64) {
	var calls int
	var mu sync.Mutex
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, _ SumCostInput) (SumCostResult, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return SumCostResult{CostUSD: 0}, nil
			}
			return SumCostResult{CostUSD: afterCost}, nil
		})
}

// doneStatusChangeReasons returns the reason of each status_change:done
// event in terminalEvents, in commit order, decoded from its payload.
func doneStatusChangeReasons(t *testing.T, terminalEvents []terminalEventRecord) []string {
	t.Helper()
	var reasons []string
	doneType := events.StatusChangeEventType(string(session.StatusDone))
	for _, ev := range terminalEvents {
		if ev.EventType != doneType {
			continue
		}
		var p events.StatusChangeEventPayload
		require.NoError(t, json.Unmarshal(ev.Payload, &p))
		reasons = append(reasons, p.Reason)
	}
	return reasons
}

// TestSessionWorkflow_SingleTurn_EndsDone_WithSingleTurnReason proves the
// single-turn outcome (FR "Single-turn ends as done", FR "Single-turn done
// reason"): a definition with MaxTurns = 1 ends its first turn as `done`,
// never `capped`, commits no capped transcript event, and its done
// status_change event carries reason single_turn. A second signalled turn
// is never processed.
func TestSessionWorkflow_SingleTurn_EndsDone_WithSingleTurnReason(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 1, MaxCostUSD: 100}
	wireCapTestActivities(env, def, tracker, rec)
	wireSuccessfulCallModel(env)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn two"})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusDone, statuses[len(statuses)-1], "a single-turn session must end done, not capped")
	assert.NotContains(t, statuses, session.StatusCapped)

	commitCount := 0
	for _, c := range rec.snapshotLog() {
		if c == "CommitTurn" {
			commitCount++
		}
	}
	assert.Equal(t, 1, commitCount, "the second signalled turn must never run after the single-turn done")

	terminalEvents := rec.snapshotTerminalEvents()
	assert.Empty(t, nonStatusChangeTerminalEvents(terminalEvents), "a single-turn done must not commit a capped (or any other terminal) transcript event")
	assert.Equal(t, []string{events.StatusChangeReasonSingleTurn}, doneStatusChangeReasons(t, terminalEvents))

	// Done is the only status with a reason; the earlier awaiting_input
	// status_change event must stay reason-less.
	for _, ev := range statusChangeEvents(terminalEvents) {
		if ev.EventType == events.StatusChangeEventType(string(session.StatusAwaitingInput)) {
			var p events.StatusChangeEventPayload
			require.NoError(t, json.Unmarshal(ev.Payload, &p))
			assert.Empty(t, p.Reason)
		}
	}
}

// TestSessionWorkflow_SingleTurn_CostCapReachedSameTurn_EndsCapped proves
// the precedence rule: when the turn that satisfies max_turns = 1 also
// reaches the cost cap, the session ends `capped` with CapKind cost, not
// done.
func TestSessionWorkflow_SingleTurn_CostCapReachedSameTurn_EndsCapped(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 1, MaxCostUSD: 0.5}
	wireCapTestActivities(env, def, tracker, rec)
	wireSuccessfulCallModel(env)
	wireSumCostStepped(env, 0.5)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusCapped, statuses[len(statuses)-1], "cost cap on the single turn must win over the single-turn done")
	assert.NotContains(t, statuses, session.StatusDone)

	terminalEvents := nonStatusChangeTerminalEvents(rec.snapshotTerminalEvents())
	require.Len(t, terminalEvents, 1)
	assert.Equal(t, events.EventTypeCapped, terminalEvents[0].EventType)
	var payload cappedEventPayload
	require.NoError(t, json.Unmarshal(terminalEvents[0].Payload, &payload))
	assert.Equal(t, session.CapKindCost, payload.CapKind)
}

// TestSessionWorkflow_SingleTurn_ToolIterations_EndsDoneAfterFinalResponse
// proves the turn cap is evaluated only after a turn's final response: a
// single-turn session whose turn makes a tool call and then a final,
// tool-free response still ends done -- the tool loop itself never caps on
// the turn count.
func TestSessionWorkflow_SingleTurn_ToolIterations_EndsDoneAfterFinalResponse(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 1, MaxCostUSD: 100, MaxToolIterations: 100}
	wireCapTestActivities(env, def, tracker, rec)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	callModelFn, callModelCalls := sequencedCallModel([]llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "search", Arguments: "{}"}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	})
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).Return(callModelFn)
	env.OnActivity(ActivityDispatchTool, mock.Anything, mock.Anything).Return(DispatchToolResult{}, nil)
	env.OnActivity(ActivityCommitToolLoopIteration, mock.Anything, mock.Anything).
		Return(CommitToolLoopIterationResult{}, nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	assert.Equal(t, 2, callModelCalls(), "the tool loop must run to the final response, not stop at the turn cap")
	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusDone, statuses[len(statuses)-1])
	assert.Equal(t, []string{events.StatusChangeReasonSingleTurn}, doneStatusChangeReasons(t, rec.snapshotTerminalEvents()))
	assert.Empty(t, nonStatusChangeTerminalEvents(rec.snapshotTerminalEvents()))
}
