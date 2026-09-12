// Issue #2119's Testing phase: turn/cost cap enforcement, terminal
// transcript events, and failure classification as exercised through
// SessionWorkflow via testsuite.TestWorkflowEnvironment -- the same
// pattern workflow_test.go establishes for #2114 (mocked activities,
// tracked call sequences, no live Temporal server or Postgres).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// terminalEventRecord is one CommitTerminalEvent activity invocation, as
// observed by the tests below.
type terminalEventRecord struct {
	Turn      int
	EventType string
	Payload   json.RawMessage
}

// callRecorder is a small thread-safe helper that appends a label (an
// activity name) to a shared, ordered log -- used below to assert
// cross-activity call ordering (e.g. "CommitTerminalEvent before the
// terminal UpdateSessionStatus write") the same way statusTracker
// (workflow_test.go) asserts a status sequence.
type callRecorder struct {
	mu       sync.Mutex
	log      []string
	term     []terminalEventRecord
	terminal []*session.TerminalReason
}

func (c *callRecorder) recordCall(label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, label)
}

func (c *callRecorder) recordTerminalEvent(in CommitTerminalEventInput) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.term = append(c.term, terminalEventRecord{Turn: in.Turn, EventType: in.EventType, Payload: in.Payload})
}

// recordTerminalReason captures an UpdateSessionStatus call's
// TerminalReason (nil for a non-terminal status transition) -- used to
// compare, byte for byte, against the terminal transcript event's own
// payload (FR2/FR3: one classification, two surfaces).
func (c *callRecorder) recordTerminalReason(reason *session.TerminalReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminal = append(c.terminal, reason)
}

// lastTerminalReason returns the most recent non-nil TerminalReason
// recorded, or nil if none was.
func (c *callRecorder) lastTerminalReason() *session.TerminalReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.terminal) - 1; i >= 0; i-- {
		if c.terminal[i] != nil {
			return c.terminal[i]
		}
	}
	return nil
}

func (c *callRecorder) snapshotLog() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.log))
	copy(out, c.log)
	return out
}

func (c *callRecorder) snapshotTerminalEvents() []terminalEventRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]terminalEventRecord, len(c.term))
	copy(out, c.term)
	return out
}

// wireCapTestActivities registers every activity SessionWorkflow dispatches
// and wires the ordinary happy-path ones (ResolveAgentDefinition,
// BuildContext, CommitTurn) to always succeed instantly with def as the
// resolved agent definition -- shared setup for the cap-trip tests below,
// which only differ in their SumCost sequence. UpdateSessionStatus and
// CommitTerminalEvent calls are both recorded into rec (in addition to
// tracker for UpdateSessionStatus's status sequence) so ordering and
// payload equality can be asserted across the two.
//
// CallModel is deliberately NOT wired here: testify's mock.On/OnActivity
// keeps the FIRST registered matching expectation for a given (activity,
// args) pair active for every call unless it is consumed via .Once(), so a
// caller that needs a failing CallModel (the classification tests below)
// cannot simply register a second, overriding OnActivity call afterward --
// it must be the only registration. Every caller below wires CallModel
// itself, right after calling this helper.
func wireCapTestActivities(env *testsuite.TestWorkflowEnvironment, def session.AgentDefinition, tracker *statusTracker, rec *callRecorder) {
	registerActivityStubs(env)

	env.OnActivity(ActivityUpdateSessionStatus, mock.Anything, mock.Anything).
		Return(UpdateSessionStatusResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(UpdateSessionStatusInput)
			tracker.record(in.Status)
			rec.recordCall("UpdateSessionStatus:" + string(in.Status))
			rec.recordTerminalReason(in.Terminal)
		})
	env.OnActivity(ActivityResolveAgentDefinition, mock.Anything, mock.Anything).
		Return(ResolveAgentDefinitionResult{Definition: def, Model: "test-model"}, nil)
	env.OnActivity(ActivityBuildContext, mock.Anything, mock.Anything).
		Return(BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}}, nil)
	env.OnActivity(ActivityCommitTurn, mock.Anything, mock.Anything).
		Return(CommitTurnResult{Done: false}, nil).
		Run(func(args mock.Arguments) { rec.recordCall("CommitTurn") })
	env.OnActivity(ActivityCommitTerminalEvent, mock.Anything, mock.Anything).
		Return(CommitTerminalEventResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(CommitTerminalEventInput)
			rec.recordCall("CommitTerminalEvent:" + in.EventType)
			rec.recordTerminalEvent(in)
		})
}

// wireSuccessfulCallModel registers the ordinary, always-succeeding
// CallModel mock -- every test below that does not itself need CallModel
// to fail calls this right after wireCapTestActivities.
func wireSuccessfulCallModel(env *testsuite.TestWorkflowEnvironment) {
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).
		Return(CallModelResult{Response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}}, nil)
}

// TestSessionWorkflow_TurnCapTrips_EndsCappedWithTurnsCapKind_NoFurtherTurnProcessed
// proves FR6: a session whose definition sets MaxTurns = 2 ends `capped`
// with CapKind turns on the turn that reaches the cap (turn 2), commits
// exactly two turns, and never processes a third signalled turn -- the
// workflow execution itself completes once evaluateCaps' "after" check
// (processTurn, workflow.go) trips on turn 2, so a later SendTurn signal
// has no running execution left to deliver to.
func TestSessionWorkflow_TurnCapTrips_EndsCappedWithTurnsCapKind_NoFurtherTurnProcessed(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 2, MaxCostUSD: 100}
	wireCapTestActivities(env, def, tracker, rec)
	wireSuccessfulCallModel(env)

	// SumCost stays at 0 throughout -- only the turn cap is exercised here.
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn two"})
	}, 2*time.Second)
	// A third signal, sent well after the session should already have
	// capped on turn two -- proves "does not process a further signalled
	// turn": the workflow has already completed by the time this is
	// delivered, so it must never turn into a third CommitTurn call.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn three"})
	}, 3*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusCapped, statuses[len(statuses)-1], "the session must end capped, distinct from done/stopped/failed")
	assert.NotContains(t, statuses, session.StatusDone)
	assert.NotContains(t, statuses, session.StatusStopped)
	assert.NotContains(t, statuses, session.StatusFailed)

	callLog := rec.snapshotLog()
	commitCount := 0
	for _, c := range callLog {
		if c == "CommitTurn" {
			commitCount++
		}
	}
	assert.Equal(t, 2, commitCount, "exactly two turns must commit -- the turn that reaches the cap runs once, a further signalled turn never does")

	terminalEvents := rec.snapshotTerminalEvents()
	require.Len(t, terminalEvents, 1, "tripping the cap must commit exactly one terminal transcript event")
	assert.Equal(t, events.EventTypeCapped, terminalEvents[0].EventType)
	var payload cappedEventPayload
	require.NoError(t, json.Unmarshal(terminalEvents[0].Payload, &payload))
	assert.Equal(t, session.CapKindTurns, payload.CapKind)
}

// TestSessionWorkflow_CostCapTrips_EndsCappedWithCostCapKind proves FR7:
// once the committed running cost sum (SumCost, mocked here to simulate a
// turn's cost landing) reaches the definition's MaxCostUSD, the session
// ends `capped` with CapKind cost -- exercised on the very first turn, so
// the turn cap (left at its generous default) cannot be what trips it.
//
// Red/green verified (issue body: "switch cost-cap evaluation to an
// in-workflow mutable counter, observe the SumCost test go red, revert"):
// workflow.go's evaluateCaps was temporarily changed to read SumCost once
// and cache the result in a package-level variable, reusing it on every
// later call instead of re-executing the SumCost activity -- this test
// went red (the workflow never observed the "after" call's higher cost,
// so it never capped and the run hung until the test environment's
// overall deadline). Reverting evaluateCaps back to reading SumCost fresh
// on every call turned it green again.
func TestSessionWorkflow_CostCapTrips_EndsCappedWithCostCapKind(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 0.50}
	wireCapTestActivities(env, def, tracker, rec)
	wireSuccessfulCallModel(env)

	// evaluateCaps calls SumCost twice per turn (before, after). The
	// "before" call (turn 0, nothing committed yet) reports 0; the "after"
	// call (turn 1, this turn's cost just committed) reports the full
	// $0.50 -- simulating a single turn whose own cost reaches the cap.
	var sumCostCalls int
	var sumCostMu sync.Mutex
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, _ SumCostInput) (SumCostResult, error) {
			sumCostMu.Lock()
			defer sumCostMu.Unlock()
			sumCostCalls++
			if sumCostCalls == 1 {
				return SumCostResult{CostUSD: 0}, nil
			}
			return SumCostResult{CostUSD: 0.50}, nil
		})

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
	require.Len(t, terminalEvents, 1)
	var payload cappedEventPayload
	require.NoError(t, json.Unmarshal(terminalEvents[0].Payload, &payload))
	assert.Equal(t, session.CapKindCost, payload.CapKind)

	sumCostMu.Lock()
	defer sumCostMu.Unlock()
	assert.Equal(t, 2, sumCostCalls, "SumCost must be read fresh both before and after the one turn that ran -- never cached across the two checks")
}

// TestSessionWorkflow_CappedTerminalEvent_CommittedBeforeStatusWrite proves
// the issue body's ordering requirement ("Tripping either cap writes its
// own transcript event before the session goes terminal"): the
// CommitTerminalEvent activity call must precede the UpdateSessionStatus
// call that writes the `capped` status.
func TestSessionWorkflow_CappedTerminalEvent_CommittedBeforeStatusWrite(t *testing.T) {
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

	callLog := rec.snapshotLog()
	terminalEventIdx, cappedStatusIdx := -1, -1
	for i, c := range callLog {
		if c == "CommitTerminalEvent:"+events.EventTypeCapped && terminalEventIdx == -1 {
			terminalEventIdx = i
		}
		if c == "UpdateSessionStatus:"+string(session.StatusCapped) {
			cappedStatusIdx = i
		}
	}
	require.NotEqual(t, -1, terminalEventIdx, "CommitTerminalEvent must have been called")
	require.NotEqual(t, -1, cappedStatusIdx, "UpdateSessionStatus(capped) must have been called")
	assert.Less(t, terminalEventIdx, cappedStatusIdx, "the capped transcript event must commit before the session's terminal status write")
}

// TestSessionWorkflow_FailedSession_CategoryDetailMatchAcrossStatusAndEvent
// proves FR2/FR3: a failed session's UpdateSessionStatus terminal write and
// its failure transcript event carry the exact same (category, detail)
// pair -- one classification (classifyError), computed once by failTurn,
// never independently re-derived for the two surfaces. A simulated
// provider rate limit is used as the triggering CallModel failure, which
// classify.go's rules classify retryable.
func TestSessionWorkflow_FailedSession_CategoryDetailMatchAcrossStatusAndEvent(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 100}
	wireCapTestActivities(env, def, tracker, rec)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	// CallModel always fails -- wireCapTestActivities deliberately leaves
	// CallModel unwired (see its doc comment) so this is the only
	// registration for it.
	rateLimitErr := errors.New("call model: POST https://openrouter.ai/api/v1/chat/completions: 429 Too Many Requests rate limit exceeded")
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).
		Return(CallModelResult{}, rateLimitErr)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a session ending failed is a normal business outcome, not a workflow execution error")

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusFailed, statuses[len(statuses)-1])

	terminalEvents := rec.snapshotTerminalEvents()
	require.Len(t, terminalEvents, 1)
	assert.Equal(t, events.EventTypeFailure, terminalEvents[0].EventType)
	var eventPayload failureEventPayload
	require.NoError(t, json.Unmarshal(terminalEvents[0].Payload, &eventPayload))
	assert.Equal(t, session.ErrorCategoryRetryable, eventPayload.ErrorCategory, "a simulated rate limit must classify retryable")

	// Cross-check against the exact terminal reason UpdateSessionStatus
	// received: workflow.go's SessionWorkflow builds this from outcome.
	// errorCategory/errorDetail, which runTurn/processTurn set from
	// failTurn's own return value -- the same call that produced the
	// terminal event's payload above. Compared against the actually
	// recorded TerminalReason (not an independent classifyError(rateLimitErr)
	// recomputation) since by the time the error crosses the
	// workflow.ExecuteActivity boundary, Temporal's SDK has wrapped it in
	// *temporal.ActivityError -- classifyError's own message-based rules
	// still match through that wrapping (classify.go's doc comment), but
	// the wrapped Error() string differs textually from the raw
	// rateLimitErr's, so recomputing from the unwrapped error would compare
	// two different message strings.
	terminalReason := rec.lastTerminalReason()
	require.NotNil(t, terminalReason, "UpdateSessionStatus(failed) must carry a non-nil TerminalReason")
	require.NotNil(t, terminalReason.ErrorCategory)
	require.NotNil(t, terminalReason.ErrorDetail)
	assert.Equal(t, eventPayload.ErrorCategory, *terminalReason.ErrorCategory, "GetSession's category and the failure transcript event's category must be identical")
	assert.Equal(t, eventPayload.ErrorDetail, *terminalReason.ErrorDetail, "GetSession's detail and the failure transcript event's detail must be identical")
}

// TestSessionWorkflow_StopRacingCapTrip_ResolvesToSingleTerminalReason
// proves the issue body's compare-and-swap requirement from the
// workflow's own side: a Stop signal that arrives while the cap-trip
// path's CommitTerminalEvent activity is still in flight cancels it
// (FR1's "cancelled, not allowed to run to completion" -- the same
// discipline TestSessionWorkflow_StopDuringInFlightActivity_
// CancelsAndEndsStopped in workflow_test.go proves for CallModel), so the
// session resolves to exactly one terminal reason: `stopped`, never
// `capped` -- the capped status write is never reached because
// cappedTurn's own CommitTerminalEvent call errored out from
// cancellation and processTurn's error propagates as stopped (runTurn's
// "stopped wins the race" branch), not as a second, conflicting terminal
// write racing UpdateStatus's storage-level compare-and-swap
// (session.SessionStore.UpdateStatus, exercised directly by
// TestSessionStore_UpdateStatus_CAS_TerminalStatusNeverOverwritten in
// whagent_net/session).
func TestSessionWorkflow_StopRacingCapTrip_ResolvesToSingleTerminalReason(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	// CommitTerminalEvent is a REAL registered activity (not an OnActivity
	// mock) that blocks until its context is cancelled -- mirrors
	// workflow_test.go's TestSessionWorkflow_StopDuringInFlightActivity_
	// CancelsAndEndsStopped's CallModel setup, which documents why: this is
	// what lets the test prove actual cancellation propagation rather than
	// a delay that merely happens to expire first. Every
	// RegisterActivityWithOptions call must precede any OnActivity mock
	// (testsuite panics otherwise), so this is registered before the
	// OnActivity mocks below rather than reusing wireCapTestActivities.
	terminalEventStarted := make(chan struct{})
	var startedOnce sync.Once
	// The other five activities also need a real registration before
	// OnActivity can mock them (registerActivityStubs' own doc comment) --
	// registered here individually (rather than via registerActivityStubs,
	// which would also register its own CommitTerminalEvent stub and panic
	// on the duplicate registration above).
	env.RegisterActivityWithOptions(func(ctx context.Context, sessionID uuid.UUID) (ResolveAgentDefinitionResult, error) {
		return ResolveAgentDefinitionResult{}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgentDefinition})
	env.RegisterActivityWithOptions(func(ctx context.Context, in BuildContextInput) (BuildContextResult, error) {
		return BuildContextResult{}, nil
	}, activity.RegisterOptions{Name: ActivityBuildContext})
	env.RegisterActivityWithOptions(func(ctx context.Context, in CallModelInput) (CallModelResult, error) {
		return CallModelResult{}, nil
	}, activity.RegisterOptions{Name: ActivityCallModel})
	env.RegisterActivityWithOptions(func(ctx context.Context, in CommitTurnInput) (CommitTurnResult, error) {
		return CommitTurnResult{}, nil
	}, activity.RegisterOptions{Name: ActivityCommitTurn})
	env.RegisterActivityWithOptions(func(ctx context.Context, in SumCostInput) (SumCostResult, error) {
		return SumCostResult{}, nil
	}, activity.RegisterOptions{Name: ActivitySumCost})
	env.RegisterActivityWithOptions(func(ctx context.Context, in UpdateSessionStatusInput) (UpdateSessionStatusResult, error) {
		return UpdateSessionStatusResult{}, nil
	}, activity.RegisterOptions{Name: ActivityUpdateSessionStatus})
	env.RegisterActivityWithOptions(func(ctx context.Context, in CommitTerminalEventInput) (CommitTerminalEventResult, error) {
		startedOnce.Do(func() { close(terminalEventStarted) })
		<-ctx.Done()
		return CommitTerminalEventResult{}, ctx.Err()
	}, activity.RegisterOptions{Name: ActivityCommitTerminalEvent})

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 1, MaxCostUSD: 100}

	env.OnActivity(ActivityResolveAgentDefinition, mock.Anything, mock.Anything).
		Return(ResolveAgentDefinitionResult{Definition: def, Model: "test-model"}, nil)
	env.OnActivity(ActivityBuildContext, mock.Anything, mock.Anything).
		Return(BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}}, nil)
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).
		Return(CallModelResult{Response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}}, nil)
	env.OnActivity(ActivityCommitTurn, mock.Anything, mock.Anything).
		Return(CommitTurnResult{Done: false}, nil)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)
	env.OnActivity(ActivityUpdateSessionStatus, mock.Anything, mock.Anything).
		Return(UpdateSessionStatusResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(UpdateSessionStatusInput)
			tracker.record(in.Status)
			rec.recordCall("UpdateSessionStatus:" + string(in.Status))
		})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusStopped, statuses[len(statuses)-1], "the race must resolve to exactly one terminal reason: stopped, not capped")
	assert.NotContains(t, statuses, session.StatusCapped, "a capped status must never be written once Stop won the race")
}

// TestAgentDefinition_HasNoPerSessionCapOverrideField is a structural guard
// (issue body: "there is no per-session cap override" -- deliberate, M1
// scope) mirroring activities_test.go's reflection-based field assertions:
// session.Session (the per-session row) must never grow its own
// max_turns/max_cost_usd-shaped field, since checkCaps (caps.go) is wired
// to read exclusively from the agent definition (evaluateCaps,
// workflow.go), never from anything session-scoped.
func TestAgentDefinition_HasNoPerSessionCapOverrideField(t *testing.T) {
	typ := reflect.TypeOf(session.Session{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		assert.NotContains(t, []string{"MaxTurns", "MaxCostUSD", "MaxTurnsOverride", "MaxCostUSDOverride"}, name,
			"session.Session must not carry a per-session cap override field (%s) -- caps are agent-definition-level only in M1", name)
	}
}

// sequencedCallModel returns a CallModel mock function that plays back
// responses in order, one per call, repeating the last one if called more
// times than len(responses) -- shared by the inner-tool-loop tests below,
// which need CallModel to answer differently across a turn's own multiple
// model calls (not just once, like wireSuccessfulCallModel).
func sequencedCallModel(responses []llm.Response) (func(ctx context.Context, in CallModelInput) (CallModelResult, error), func() int) {
	var mu sync.Mutex
	calls := 0
	fn := func(ctx context.Context, in CallModelInput) (CallModelResult, error) {
		mu.Lock()
		defer mu.Unlock()
		idx := calls
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		calls++
		return CallModelResult{Response: responses[idx]}, nil
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	return fn, count
}

// TestSessionWorkflow_ToolLoop_LoopsAcrossMultipleModelCalls_CommitsTurnOnce
// proves "add the inner tool loop": a turn whose model keeps requesting
// tool calls makes more than one CallModel call, dispatching and committing
// each intermediate iteration (ActivityCommitToolLoopIteration), but still
// commits exactly one turn_usage/assistant_message row via ActivityCommitTurn
// once the model's response finally carries no more tool calls -- not one
// CommitTurn per model call.
func TestSessionWorkflow_ToolLoop_LoopsAcrossMultipleModelCalls_CommitsTurnOnce(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 100, MaxToolIterations: 100}
	wireCapTestActivities(env, def, tracker, rec)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	callModelFn, callModelCalls := sequencedCallModel([]llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "search", Arguments: "{}"}}},
		{Message: llm.Message{Role: llm.RoleAssistant}, ToolCalls: []llm.ToolCall{{ID: "call-2", Name: "search", Arguments: "{}"}}},
		{Message: llm.Message{Role: llm.RoleAssistant, Content: "done"}},
	})
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).Return(callModelFn)

	var dispatchCalls, loopIterationCalls int
	var countMu sync.Mutex
	env.OnActivity(ActivityDispatchTool, mock.Anything, mock.Anything).
		Return(DispatchToolResult{}, nil).
		Run(func(args mock.Arguments) {
			countMu.Lock()
			dispatchCalls++
			countMu.Unlock()
		})
	env.OnActivity(ActivityCommitToolLoopIteration, mock.Anything, mock.Anything).
		Return(CommitToolLoopIterationResult{PromptTokens: 10, CompletionTokens: 5, CostUSD: 0.01}, nil).
		Run(func(args mock.Arguments) {
			countMu.Lock()
			loopIterationCalls++
			countMu.Unlock()
		})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	assert.Equal(t, 3, callModelCalls(), "the turn must make one model call per loop iteration plus the final, non-looping response")
	assert.Equal(t, 2, dispatchCalls, "exactly one DispatchTool call per intermediate iteration's single tool call")
	assert.Equal(t, 2, loopIterationCalls, "exactly one CommitToolLoopIteration call per intermediate (non-final) iteration")

	callLog := rec.snapshotLog()
	commitCount := 0
	for _, c := range callLog {
		if c == "CommitTurn" {
			commitCount++
		}
	}
	assert.Equal(t, 1, commitCount, "the turn must commit exactly once, via ActivityCommitTurn, regardless of how many model calls it took")
}

// TestSessionWorkflow_ToolIterationCapTrips_EndsCappedWithToolIterationsCapKind
// proves the inner tool loop's own run-away guard: a model that never stops
// requesting tool calls trips MaxToolIterations rather than looping without
// bound, ending the session capped with CapKind tool_iterations -- distinct
// from the turns/cost caps, and without ever reaching ActivityCommitTurn.
func TestSessionWorkflow_ToolIterationCapTrips_EndsCappedWithToolIterationsCapKind(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tracker := &statusTracker{}
	rec := &callRecorder{}
	def := session.AgentDefinition{MaxTurns: 100, MaxCostUSD: 100, MaxToolIterations: 2}
	wireCapTestActivities(env, def, tracker, rec)
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)

	// Always requests another tool call -- never a final, tool-call-free
	// response -- so the only way this turn ever ends is the iteration cap.
	alwaysToolCalls := llm.Response{
		Message:   llm.Message{Role: llm.RoleAssistant},
		ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "search", Arguments: "{}"}},
	}
	callModelFn, callModelCalls := sequencedCallModel([]llm.Response{alwaysToolCalls, alwaysToolCalls, alwaysToolCalls})
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

	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	assert.Equal(t, session.StatusCapped, statuses[len(statuses)-1])

	assert.Equal(t, 2, callModelCalls(), "MaxToolIterations=2 allows exactly two model calls before the third would be blocked")

	terminalEvents := rec.snapshotTerminalEvents()
	require.Len(t, terminalEvents, 1)
	var payload cappedEventPayload
	require.NoError(t, json.Unmarshal(terminalEvents[0].Payload, &payload))
	assert.Equal(t, session.CapKindToolIterations, payload.CapKind)

	callLog := rec.snapshotLog()
	assert.NotContains(t, callLog, "CommitTurn", "a turn that trips the iteration cap must never reach the ordinary commit path")
}
