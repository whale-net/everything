// Durability/NFR1 guard (issue #2114's Testing phase): replay a recorded
// workflow history against the current SessionWorkflow code via
// worker.WorkflowReplayer and assert it replays without a non-determinism
// error. This is the test workflow.go's package doc comment (NFR1
// section) points at as the standing check that
// workflow.GetVersion-gated edits stay compatible with a run whose
// history predates them -- part of this package's normal `bazel test`
// set, not a one-off debugging tool.
//
// The fixture below is a hand-built *historypb.History for one real
// SessionWorkflow run (one full turn, then back to blocking on the next
// signal) using only public go.temporal.io/api types -- there is no
// live-Temporal-server dependency here (see historyFixtureBuilder's doc
// comment for why, and for the precedent this reimplements from the
// Temporal Go SDK's own replay tests).
package main

import (
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"

	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"

	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/worker/tools"
)

// Marker event field names for a workflow.GetVersion call, mirroring the
// Temporal Go SDK's own (unexported) versionMarkerName/
// versionMarkerChangeIDName/versionMarkerDataName constants
// (internal/internal_command_state_machine.go). These are part of the
// wire history schema every SDK version agrees on, not SDK-internal-only
// naming -- the SDK's own replay tests
// (internal/internal_worker_test.go's createTestEventVersionMarker) hard-
// code the same values; there is no public constant to import instead.
const (
	replayVersionMarkerName              = "Version"
	replayVersionMarkerChangeID          = "change-id"
	replayVersionMarkerData              = "version"
	replayVersionMarkerSearchAttrUpdated = "version-search-attribute-updated"
)

// historyFixtureBuilder builds a *historypb.History one real Temporal
// workflow task at a time, auto-numbering event IDs so the fixture below
// reads as "what happened, in order" instead of requiring hand-counted
// event IDs. Modeled on the Temporal Go SDK's own replay test fixtures
// (internal/internal_worker_test.go's createTestEvent* helpers and
// createHistoryForGetVersionTests), reimplemented here with only public
// go.temporal.io/api types since the SDK's own helpers are unexported and
// this package has no other way to construct a *historypb.History without
// a live Temporal server (which this repo has no test dependency on --
// see worker/BUILD.bazel; this fixture is what makes the durability
// guard a fast, hermetic `bazel test` rather than something that needs
// Docker/a running Temporal cluster).
type historyFixtureBuilder struct {
	events []*historypb.HistoryEvent
	nextID int64
}

func newHistoryFixtureBuilder() *historyFixtureBuilder {
	return &historyFixtureBuilder{nextID: 1}
}

func (b *historyFixtureBuilder) nextEventID() int64 {
	id := b.nextID
	b.nextID++
	return id
}

func (b *historyFixtureBuilder) started(workflowType, taskQueue string, input *commonpb.Payloads) {
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   b.nextEventID(),
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				WorkflowType: &commonpb.WorkflowType{Name: workflowType},
				TaskQueue:    &taskqueuepb.TaskQueue{Name: taskQueue},
				Input:        input,
			},
		},
	})
}

// decision appends one full WorkflowTaskScheduled/Started/Completed
// triple -- one replayed workflow task whose commands are all resolved by
// the events that follow (activity/marker calls below) -- and returns the
// WorkflowTaskCompleted event's ID.
func (b *historyFixtureBuilder) decision() int64 {
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:    b.nextEventID(),
		EventType:  enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{}},
	})
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   b.nextEventID(),
		EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED,
	})
	completedID := b.nextEventID()
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:    completedID,
		EventType:  enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED,
		Attributes: &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{WorkflowTaskCompletedEventAttributes: &historypb.WorkflowTaskCompletedEventAttributes{}},
	})
	return completedID
}

// openDecision appends a WorkflowTaskScheduled/Started pair with no
// matching Completed event -- the "currently in-progress, not yet
// recorded" decision every open (still-running) workflow's history tail
// legitimately ends with. Left deliberately dangling: whatever the
// replayed workflow code does from here (for SessionWorkflow: nothing --
// it is back to blocking on its signal channel) is the *next* decision,
// not yet reflected in any history event, so nothing needs to match it.
// Mirrors the Temporal Go SDK's own
// TestReplayWorkflowHistory_IncompleteWorkflowExecution and is what makes
// this fixture an honest snapshot of a long-lived, still-open session
// (FR4) rather than a synthetic "completed" run SessionWorkflow would
// never actually produce mid-session.
func (b *historyFixtureBuilder) openDecision() {
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:    b.nextEventID(),
		EventType:  enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{}},
	})
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   b.nextEventID(),
		EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED,
	})
}

// marker appends a workflow.GetVersion Marker event for changeID/version,
// attributed to the workflow task that recorded it (workflowTaskCompletedID).
// Deliberately not paired with an UpsertWorkflowSearchAttributes event --
// this marker's optional "version-search-attribute-updated" detail is set
// to false specifically so the SDK's own event-ID prediction
// (internal_command_state_machine.go's
// incrementNextCommandEventIDIfVersionMarker, which reorders and consumes
// GetVersion marker events before predicting where the next *real*
// command will land in history) skips exactly one slot for this marker,
// not two. The Temporal Go SDK's replay matcher
// (internal/internal_task_handlers.go's skipDeterministicCheckForEvent)
// separately always skips a versionMarkerName MarkerRecorded event when
// comparing replayed commands against history regardless -- GetVersion
// never re-emits a marker command on replay once the changeID is already
// resolved from history, so there is nothing for this event to match
// against either way; the only thing that actually needs to line up is
// the event-ID bookkeeping above.
func (b *historyFixtureBuilder) marker(changeID string, version int, workflowTaskCompletedID int64) {
	dc := converter.GetDefaultDataConverter()
	changeIDPayload, err := dc.ToPayloads(changeID)
	if err != nil {
		panic(err)
	}
	versionPayload, err := dc.ToPayloads(version)
	if err != nil {
		panic(err)
	}
	searchAttrUpdatedPayload, err := dc.ToPayloads(false)
	if err != nil {
		panic(err)
	}
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   b.nextEventID(),
		EventType: enumspb.EVENT_TYPE_MARKER_RECORDED,
		Attributes: &historypb.HistoryEvent_MarkerRecordedEventAttributes{
			MarkerRecordedEventAttributes: &historypb.MarkerRecordedEventAttributes{
				MarkerName: replayVersionMarkerName,
				Details: map[string]*commonpb.Payloads{
					replayVersionMarkerChangeID:          changeIDPayload,
					replayVersionMarkerData:              versionPayload,
					replayVersionMarkerSearchAttrUpdated: searchAttrUpdatedPayload,
				},
				WorkflowTaskCompletedEventId: workflowTaskCompletedID,
			},
		},
	})
}

func (b *historyFixtureBuilder) signal(name string, payload *commonpb.Payloads) {
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   b.nextEventID(),
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionSignaledEventAttributes{
			WorkflowExecutionSignaledEventAttributes: &historypb.WorkflowExecutionSignaledEventAttributes{
				SignalName: name,
				Input:      payload,
				Identity:   "test-identity",
			},
		},
	})
}

// activity appends one full ActivityTaskScheduled/Started/Completed
// triple, encoding result via the default data converter so the replayed
// workflow code decodes it exactly as production code would (the
// replayed workflow never re-executes activity bodies -- only their
// recorded Result payload matters). ActivityId is the scheduled event's
// own ID as a string, the same scheme the Temporal Go SDK's own replay
// fixtures use (internal/internal_worker_test.go) and how a real server
// assigns an unset activity ID -- SessionWorkflow's activity calls never
// set one explicitly (workflow.go), so this must match for
// isCommandMatchEvent's SCHEDULE_ACTIVITY_TASK comparison to succeed.
func (b *historyFixtureBuilder) activity(name string, result interface{}) {
	dc := converter.GetDefaultDataConverter()
	resultPayloads, err := dc.ToPayloads(result)
	if err != nil {
		panic(err)
	}

	scheduledID := b.nextEventID()
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   scheduledID,
		EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
			ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
				ActivityId:   strconv.FormatInt(scheduledID, 10),
				ActivityType: &commonpb.ActivityType{Name: name},
				TaskQueue:    &taskqueuepb.TaskQueue{Name: TaskQueue},
			},
		},
	})

	startedID := b.nextEventID()
	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   startedID,
		EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED,
		Attributes: &historypb.HistoryEvent_ActivityTaskStartedEventAttributes{
			ActivityTaskStartedEventAttributes: &historypb.ActivityTaskStartedEventAttributes{ScheduledEventId: scheduledID},
		},
	})

	b.events = append(b.events, &historypb.HistoryEvent{
		EventId:   b.nextEventID(),
		EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED,
		Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{
			ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{
				ScheduledEventId: scheduledID,
				StartedEventId:   startedID,
				Result:           resultPayloads,
			},
		},
	})
}

func (b *historyFixtureBuilder) build() *historypb.History {
	return &historypb.History{Events: b.events}
}

// TestSessionWorkflow_ReplayRecordedHistory_NoNonDeterminismError is
// issue #2114's durability/NFR1 guard: a history recorded through one
// full turn (current agent definition resolved, context built, model
// called, turn committed, session back to awaiting_input) and left open
// -- exactly the shape a real long-lived session's history has mid-run --
// must replay against the CURRENT SessionWorkflow code with no
// non-determinism error. This is what actually proves FR4 ("a session
// resumes exactly where it left off... across a worker restart or a
// deploy that changes the workflow's own code") and what NFR1's
// workflow.GetVersion discipline (workflow.go's package doc comment)
// exists to keep true as this file changes in later tasks.
func TestSessionWorkflow_ReplayRecordedHistory_NoNonDeterminismError(t *testing.T) {
	dc := converter.GetDefaultDataConverter()
	sessionID := testSessionID()

	startInput, err := dc.ToPayloads(SessionWorkflowInput{SessionID: sessionID})
	require.NoError(t, err)
	signalPayload, err := dc.ToPayloads(SendTurnSignal{Input: "hello"})
	require.NoError(t, err)

	b := newHistoryFixtureBuilder()
	b.started("SessionWorkflow", TaskQueue, startInput)

	// Decision: SessionWorkflow's very first act, before ever blocking on a
	// signal -- writes awaiting_input (workflow.go's updateSessionStatus at
	// the top of the function). This is also where the NFR1 versioning
	// marker for "session-workflow-status-transitions" is first recorded on
	// a real run.
	completedID := b.decision()
	b.marker("session-workflow-status-transitions", 1, completedID)
	b.activity(ActivityUpdateSessionStatus, UpdateSessionStatusResult{})

	// A SendTurn signal arrives while the workflow is blocked awaiting
	// input -- turn 1 begins, session status -> running.
	b.signal(SignalSendTurn, signalPayload)
	b.decision()
	b.activity(ActivityUpdateSessionStatus, UpdateSessionStatusResult{})

	// The turn's four per-turn activities (ARCHITECTURE.md "Session
	// workflow"), each its own workflow task cycle.
	b.decision()
	b.activity(ActivityResolveAgentDefinition, ResolveAgentDefinitionResult{Model: "replay-model"})
	b.decision()
	b.activity(ActivityBuildContext, BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}})
	b.decision()
	b.activity(ActivityCallModel, CallModelResult{Response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "hi there"}}})
	b.decision()
	b.activity(ActivityCommitTurn, CommitTurnResult{Done: false})

	// The turn finishes -- session status -> awaiting_input again.
	b.decision()
	b.activity(ActivityUpdateSessionStatus, UpdateSessionStatusResult{})

	// The workflow is now back to blocking on its signal channel, open and
	// idle -- exactly the state a real long-lived session sits in between
	// turns (FR4). See openDecision's doc comment for why this tail is
	// deliberately left without a matching WorkflowTaskCompleted event.
	b.openDecision()

	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(SessionWorkflow)

	err = replayer.ReplayWorkflowHistory(nil, b.build())
	require.NoError(t, err, "the current SessionWorkflow code must replay a recorded run's history without a non-determinism error (NFR1/FR4)")
}

// TestSessionWorkflow_ReplayRecordedHistory_WithCapEnforcementAndToolDispatch_NoNonDeterminismError
// is issue #2121's own NFR1 guard, complementing the test above rather
// than replacing it: that fixture proves a history recorded BEFORE
// "session-workflow-cap-enforcement"/"session-workflow-tool-dispatch"
// existed (no markers for either) still replays under the current code
// (both gates fall back to workflow.DefaultVersion, per GetVersion's
// documented behavior for a changeID absent from history at its call
// site) -- the backward direction NFR1 exists for. This fixture proves
// the forward direction: a history recorded WITH both gates already at
// version 1 -- the shape every session started after this task's deploy
// actually has, including a real FR8/FR2 tool-call/tool-result round
// trip -- also replays cleanly against the current code. Marker
// placement mirrors workflow.go's actual call sites exactly:
// "session-workflow-cap-enforcement" is recorded on the very first
// decision of processTurn (attached to the same WorkflowTaskCompleted as
// ActivityResolveAgentDefinition's scheduling, since workflow.GetVersion
// itself never yields); "session-workflow-tool-dispatch" is recorded
// immediately after BuildContext's activity completes, ahead of
// ActivityListToolDefinitions -- see workflow.go's processTurn for the
// exact sequence this fixture reproduces.
func TestSessionWorkflow_ReplayRecordedHistory_WithCapEnforcementAndToolDispatch_NoNonDeterminismError(t *testing.T) {
	dc := converter.GetDefaultDataConverter()
	sessionID := testSessionID()

	startInput, err := dc.ToPayloads(SessionWorkflowInput{SessionID: sessionID})
	require.NoError(t, err)
	signalPayload, err := dc.ToPayloads(SendTurnSignal{Input: "hello"})
	require.NoError(t, err)

	b := newHistoryFixtureBuilder()
	b.started("SessionWorkflow", TaskQueue, startInput)

	completedID := b.decision()
	b.marker("session-workflow-status-transitions", 1, completedID)
	b.activity(ActivityUpdateSessionStatus, UpdateSessionStatusResult{})

	b.signal(SignalSendTurn, signalPayload)
	b.decision()
	b.activity(ActivityUpdateSessionStatus, UpdateSessionStatusResult{})

	// processTurn's first act: workflow.GetVersion("session-workflow-cap-
	// enforcement", ...) never yields, so its marker is recorded on the
	// SAME decision that schedules ActivityResolveAgentDefinition.
	completedID = b.decision()
	b.marker("session-workflow-cap-enforcement", 1, completedID)
	b.activity(ActivityResolveAgentDefinition, ResolveAgentDefinitionResult{Model: "replay-model"})

	// evaluateCaps' "before" half (turn-1 usage) -- not capped, so
	// processTurn continues.
	b.decision()
	b.activity(ActivitySumCost, SumCostResult{CostUSD: 0.01})

	b.decision()
	b.activity(ActivityBuildContext, BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}})

	// workflow.GetVersion("session-workflow-tool-dispatch", ...) is called
	// right after BuildContext's Get() returns -- same "never yields"
	// reasoning, so its marker is recorded on the decision that schedules
	// ActivityListToolDefinitions.
	completedID = b.decision()
	b.marker("session-workflow-tool-dispatch", 1, completedID)
	b.activity(ActivityListToolDefinitions, ListToolDefinitionsResult{
		Tools: []llm.ToolDefinition{{Name: "search", Description: "search ASS", Parameters: nil}},
	})

	// The model responds with one tool call -- FR8's round trip.
	b.decision()
	b.activity(ActivityCallModel, CallModelResult{Response: llm.Response{
		Message:   llm.Message{Role: llm.RoleAssistant, Content: ""},
		ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "search", Arguments: `{"query":"hello"}`}},
	}})

	// processTurn's per-tool-call dispatch loop -- one ActivityDispatchTool
	// per entry of the model's ToolCalls (workflow.go).
	b.decision()
	b.activity(ActivityDispatchTool, DispatchToolResult{Result: tools.Result{
		ToolCallID: "call-1", Name: "search", Content: "3 results found", IsError: false,
	}})

	b.decision()
	b.activity(ActivityCommitTurn, CommitTurnResult{Done: false})

	// evaluateCaps' "after" half -- still not capped.
	b.decision()
	b.activity(ActivitySumCost, SumCostResult{CostUSD: 0.02})

	b.decision()
	b.activity(ActivityUpdateSessionStatus, UpdateSessionStatusResult{})

	b.openDecision()

	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflow(SessionWorkflow)

	err = replayer.ReplayWorkflowHistory(nil, b.build())
	require.NoError(t, err, "the current SessionWorkflow code must replay a FRESH (post-#2121) history -- cap-enforcement and tool-dispatch both already at version 1, including a real tool-call/tool-result round trip -- without a non-determinism error (NFR1)")
}
