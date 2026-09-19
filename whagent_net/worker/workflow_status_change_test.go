// Issue #2753's Testing phase (whagent-net M5, root plan #2747): FR1's
// status_change transcript event emission, exercised through
// SessionWorkflow via testsuite.TestWorkflowEnvironment -- the same
// wireCapTestActivities/callRecorder pattern workflow_caps_test.go
// establishes for #2119's capped/failed terminal events.
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/session"
)

// statusChangeEvents filters terminalEvents down to only the
// status_change-typed ones, in call order -- a small helper shared by the
// tests below so each one only has to reason about the events FR1 itself
// is responsible for, not the capped/failure events other tests already
// cover.
func statusChangeEvents(terminalEvents []terminalEventRecord) []terminalEventRecord {
	var out []terminalEventRecord
	for _, ev := range terminalEvents {
		if _, ok := events.ParseStatusChangeEventType(ev.EventType); ok {
			out = append(out, ev)
		}
	}
	return out
}

// decodeStatus unmarshals a status_change event's payload and returns the
// status it carries.
func decodeStatus(t *testing.T, payload json.RawMessage) string {
	t.Helper()
	var p events.StatusChangeEventPayload
	require.NoError(t, json.Unmarshal(payload, &p))
	return p.Status
}

// TestSessionWorkflow_StatusChangeEvents_RunningAwaitingInputDone proves
// FR1: a session that runs one turn to awaiting_input and a second turn to
// done commits a status_change event (via ActivityCommitTerminalEvent) for
// every non-terminal-with-its-own-event status transition -- the pre-loop
// awaiting_input write, each turn's running write, the first turn's
// trailing awaiting_input write, and the second turn's trailing done
// write -- five events total, each with EventType ==
// events.StatusChangeEventType(<status>) and a payload that decodes back
// to that same status.
func TestSessionWorkflow_StatusChangeEvents_RunningAwaitingInputDone(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 100}

	// wireCapTestActivities is deliberately NOT used here: it registers
	// ActivityCommitTurn with a single, fixed Done: false OnActivity
	// expectation, and testify's mock.On/OnActivity keeps the FIRST
	// registered matching expectation active for every call (that file's
	// own doc comment) -- a second registration would never take effect.
	// This test needs CommitTurn's Done to flip true on its second call, so
	// every other activity is wired individually instead, mirroring
	// wireCapTestActivities' own wiring one level up.
	registerActivityStubs(env)
	env.OnActivity(ActivityUpdateSessionStatus, mock.Anything, mock.Anything).
		Return(UpdateSessionStatusResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(UpdateSessionStatusInput)
			tracker.record(in.Status)
			rec.recordCall("UpdateSessionStatus:" + string(in.Status))
		})
	env.OnActivity(ActivityResolveAgentDefinition, mock.Anything, mock.Anything).
		Return(ResolveAgentDefinitionResult{Definition: def, Model: "test-model"}, nil)
	env.OnActivity(ActivityBuildContext, mock.Anything, mock.Anything).
		Return(BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}}, nil)
	env.OnActivity(ActivityCommitTerminalEvent, mock.Anything, mock.Anything).
		Return(CommitTerminalEventResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(CommitTerminalEventInput)
			rec.recordCall("CommitTerminalEvent:" + in.EventType)
			rec.recordTerminalEvent(in)
		})
	wireSuccessfulCallModel(env)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	// First CommitTurn call (turn one) reports Done: false, so the turn
	// ends awaiting_input; the second (turn two) reports Done: true, so the
	// session ends done and the workflow execution completes.
	var commitTurnCalls int
	env.OnActivity(ActivityCommitTurn, mock.Anything, mock.Anything).
		Return(func(_ context.Context, _ CommitTurnInput) (CommitTurnResult, error) {
			commitTurnCalls++
			rec.recordCall("CommitTurn")
			return CommitTurnResult{Done: commitTurnCalls >= 2}, nil
		})

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
	assert.Equal(t, session.StatusDone, statuses[len(statuses)-1])

	scEvents := statusChangeEvents(rec.snapshotTerminalEvents())
	require.Len(t, scEvents, 5, "pre-loop awaiting_input, turn 1 running, turn 1 awaiting_input, turn 2 running, turn 2 done")

	assert.Equal(t, 0, scEvents[0].Turn)
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusAwaitingInput)), scEvents[0].EventType)
	assert.Equal(t, string(session.StatusAwaitingInput), decodeStatus(t, scEvents[0].Payload))

	assert.Equal(t, 1, scEvents[1].Turn)
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusRunning)), scEvents[1].EventType)
	assert.Equal(t, string(session.StatusRunning), decodeStatus(t, scEvents[1].Payload))

	assert.Equal(t, 1, scEvents[2].Turn)
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusAwaitingInput)), scEvents[2].EventType)
	assert.Equal(t, string(session.StatusAwaitingInput), decodeStatus(t, scEvents[2].Payload))
	assert.Equal(t, scEvents[1].Turn, scEvents[2].Turn, "FR2: running and its same-iteration trailing awaiting_input share Turn")
	assert.NotEqual(t, scEvents[1].EventType, scEvents[2].EventType, "but carry distinct EventType so AppendIfAbsent never collides them")

	assert.Equal(t, 2, scEvents[3].Turn)
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusRunning)), scEvents[3].EventType)

	assert.Equal(t, 2, scEvents[4].Turn)
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusDone)), scEvents[4].EventType)
	assert.Equal(t, string(session.StatusDone), decodeStatus(t, scEvents[4].Payload))
	assert.Equal(t, scEvents[3].Turn, scEvents[4].Turn, "FR2: running and its same-iteration trailing done share Turn")
	assert.NotEqual(t, scEvents[3].EventType, scEvents[4].EventType)
}

// TestSessionWorkflow_StatusChangeEvent_Stopped proves FR1's fourth
// status: a Stop signal delivered while the session is idle between turns
// (never entered running) commits a status_change:stopped event at turn 0
// -- the same turn value the pre-loop awaiting_input event already used,
// since no turn ever incremented.
func TestSessionWorkflow_StatusChangeEvent_Stopped(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 100}
	wireCapTestActivities(env, def, tracker, rec)
	wireSuccessfulCallModel(env)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusStopped, statuses[len(statuses)-1])

	scEvents := statusChangeEvents(rec.snapshotTerminalEvents())
	require.Len(t, scEvents, 2, "pre-loop awaiting_input, then stopped")
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusStopped)), scEvents[1].EventType)
	assert.Equal(t, 0, scEvents[1].Turn)
	assert.Equal(t, string(session.StatusStopped), decodeStatus(t, scEvents[1].Payload))
}

// TestSessionWorkflow_StatusChangeEvent_NotDuplicatedForCappedOrFailed
// proves FR1's explicit "not a duplicate event" requirement: a session
// ending capped or failed commits exactly one terminal transcript event
// each (cappedTurn's/failTurn's own, asserted elsewhere), and never an
// additional status_change:capped or status_change:failed event --
// events.StatusChangeEventType only ever derives the four non-terminal-
// with-their-own-event statuses, so no such type could even be produced,
// but this pins the observable behavior directly.
func TestSessionWorkflow_StatusChangeEvent_NotDuplicatedForCappedOrFailed(t *testing.T) {
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

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusCapped, statuses[len(statuses)-1])

	terminalEvents := rec.snapshotTerminalEvents()
	require.Len(t, terminalEvents, 3, "the pre-loop awaiting_input status_change event, the turn's own running status_change event, and cappedTurn's own capped event -- never a status_change:capped on top")
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusAwaitingInput)), terminalEvents[0].EventType)
	assert.Equal(t, events.StatusChangeEventType(string(session.StatusRunning)), terminalEvents[1].EventType)
	assert.Equal(t, events.EventTypeCapped, terminalEvents[2].EventType)

	scEvents := statusChangeEvents(terminalEvents)
	require.Len(t, scEvents, 2, "only the pre-loop awaiting_input and the turn's own running status_change events -- capped must never grow its own status_change event")
}
