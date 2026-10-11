// Idle clock behavior of SessionWorkflow in awaiting_input (expiry, turn
// re-arm, Stop/SendTurn races with the timer, pre-deploy ArmIdleTimer sweep),
// exercised through testsuite.TestWorkflowEnvironment with mocked activities.
// Mock time only advances while the workflow is blocked, so durations below
// are relative to the workflow's start.
package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/whagent_net/events"
	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// statusAt is one UpdateSessionStatus write and its offset from workflow start.
type statusAt struct {
	status session.Status
	at     time.Duration
}

// idleTimeline records each status write with its mock-clock offset.
type idleTimeline struct {
	mu       sync.Mutex
	start    time.Time
	statuses []statusAt
}

func (tl *idleTimeline) record(status session.Status, at time.Time) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.statuses = append(tl.statuses, statusAt{status: status, at: at.Sub(tl.start)})
}

func (tl *idleTimeline) snapshot() []statusAt {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	return append([]statusAt(nil), tl.statuses...)
}

// wireIdleActivities registers mocks for a turn-capable session whose
// effective idle timeout resolves to timeout. A zero timeout models a
// pre-change run whose clock is only started by the ArmIdleTimer sweep.
func wireIdleActivities(env *testsuite.TestWorkflowEnvironment, timeout time.Duration, tl *idleTimeline, rec *callRecorder) {
	registerActivityStubs(env)
	env.OnActivity(ActivityUpdateSessionStatus, mock.Anything, mock.Anything).
		Return(UpdateSessionStatusResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(UpdateSessionStatusInput)
			tl.record(in.Status, env.Now())
			rec.recordCall("UpdateSessionStatus:" + string(in.Status))
			rec.recordTerminalReason(in.Terminal)
		})
	env.OnActivity(ActivityResolveAgentDefinition, mock.Anything, mock.Anything).
		Return(ResolveAgentDefinitionResult{Definition: session.AgentDefinition{MaxCostUSD: 100}, Model: "test-model"}, nil)
	env.OnActivity(ActivityResolveIdleTimeout, mock.Anything, mock.Anything).
		Return(ResolveIdleTimeoutResult{EffectiveTimeout: timeout}, nil)
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
	env.OnActivity(ActivitySumCost, mock.Anything, mock.Anything).
		Return(SumCostResult{CostUSD: 0}, nil)
	wireSuccessfulCallModel(env)
}

// statusChangeReasonsFor returns the reason of each status_change event for
// status in terminalEvents, in commit order.
func statusChangeReasonsFor(t *testing.T, terminalEvents []terminalEventRecord, status session.Status) []string {
	t.Helper()
	var reasons []string
	want := events.StatusChangeEventType(string(status))
	for _, ev := range statusChangeEvents(terminalEvents) {
		if ev.EventType != want {
			continue
		}
		var p events.StatusChangeEventPayload
		require.NoError(t, json.Unmarshal(ev.Payload, &p))
		reasons = append(reasons, p.Reason)
	}
	return reasons
}

// statusSequence returns just the status values from a timeline.
func statusSequence(tl []statusAt) []session.Status {
	out := make([]session.Status, 0, len(tl))
	for _, s := range tl {
		out = append(out, s.status)
	}
	return out
}

// TestSessionWorkflow_IdleTimeoutElapses_EndsExpiredWithIdleTimeoutReason
// proves FR "Idle timeout expires the session": a session that never gets a
// turn ends expired exactly one effective timeout after entering
// awaiting_input, with one status_change:expired event carrying reason
// idle_timeout and no further turn.
func TestSessionWorkflow_IdleTimeoutElapses_EndsExpiredWithIdleTimeoutReason(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	const timeout = time.Minute
	tl := &idleTimeline{start: env.Now()}
	rec := &callRecorder{}
	wireIdleActivities(env, timeout, tl, rec)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tl.snapshot()
	require.NotEmpty(t, statuses)
	last := statuses[len(statuses)-1]
	assert.Equal(t, session.StatusExpired, last.status)
	require.Len(t, statuses, 2, "awaiting_input, then expired")
	assert.Equal(t, session.StatusAwaitingInput, statuses[0].status)
	assert.Equal(t, statuses[0].at+timeout, last.at, "expiry fires exactly one effective timeout after entering awaiting_input")

	assert.Equal(t, []string{events.StatusChangeReasonIdleTimeout}, statusChangeReasonsFor(t, rec.snapshotTerminalEvents(), session.StatusExpired))
	assert.NotContains(t, rec.snapshotLog(), "CommitTurn")
}

// TestSessionWorkflow_IdleClock_RearmsAfterTurn_ExpiresOneTimeoutAfterTurnEnds
// proves a turn in flight cancels the idle clock and the clock is re-armed
// with a full timeout when the turn completes. The first clock would have
// expired at 60s, so expiry at 70s shows it was cancelled at turn start.
func TestSessionWorkflow_IdleClock_RearmsAfterTurn_ExpiresOneTimeoutAfterTurnEnds(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	const timeout = time.Minute
	tl := &idleTimeline{start: env.Now()}
	rec := &callRecorder{}
	wireIdleActivities(env, timeout, tl, rec)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "hello"})
	}, 10*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tl.snapshot()
	require.Equal(t, []session.Status{
		session.StatusAwaitingInput,
		session.StatusRunning,
		session.StatusAwaitingInput,
		session.StatusExpired,
	}, statusSequence(statuses))

	turnEnd := statuses[2].at
	assert.Equal(t, 10*time.Second, statuses[1].at, "the turn starts at the signal")
	assert.Equal(t, turnEnd+timeout, statuses[3].at, "the re-armed clock runs a full timeout from the turn's end")
	assert.NotEqual(t, statuses[0].at+timeout, statuses[3].at, "the pre-turn clock must not expire the session")

	assert.Equal(t, 1, countOf(rec.snapshotLog(), "CommitTurn"))
	assert.Equal(t, []string{events.StatusChangeReasonIdleTimeout}, statusChangeReasonsFor(t, rec.snapshotTerminalEvents(), session.StatusExpired))
}

// TestSessionWorkflow_IdleClock_NotRunningDuringTurn proves a turn in flight
// is never interrupted by the idle timeout and its duration does not count as
// idle time. The model call blocks until the test releases it, or a 2s wall
// fallback, since the mock clock does not advance mid-activity.
func TestSessionWorkflow_IdleClock_NotRunningDuringTurn(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	const timeout = time.Minute
	tl := &idleTimeline{start: env.Now()}
	rec := &callRecorder{}
	wireIdleActivities(env, timeout, tl, rec)

	release := make(chan struct{})
	var once sync.Once
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, _ CallModelInput) (CallModelResult, error) {
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
			return CallModelResult{Response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}}, nil
		})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "hello"})
	}, 10*time.Second)
	env.RegisterDelayedCallback(func() {
		once.Do(func() { close(release) })
	}, 2*timeout)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tl.snapshot()
	seq := statusSequence(statuses)
	require.GreaterOrEqual(t, len(seq), 4)
	assert.Equal(t, session.StatusAwaitingInput, seq[0])
	assert.Equal(t, session.StatusRunning, seq[1])
	assert.Equal(t, session.StatusAwaitingInput, seq[2], "the turn completes back to awaiting_input, never expired mid-turn")
	assert.Equal(t, session.StatusExpired, seq[len(seq)-1])
	for _, s := range seq[1 : len(seq)-1] {
		assert.NotEqual(t, session.StatusExpired, s)
	}
	last := statuses[len(statuses)-1]
	assert.Equal(t, statuses[len(statuses)-2].at+timeout, last.at, "expiry is a full timeout after the final awaiting_input")
}

// TestSessionWorkflow_StopAroundTimeout proves Stop wins when it lands before
// the clock fires, and that when Stop and the clock fire at the same mock
// instant exactly one terminal status is written and one status_change event
// is committed.
func TestSessionWorkflow_StopAroundTimeout(t *testing.T) {
	const timeout = time.Minute

	t.Run("StopBeforeTimeoutEndsStopped", func(t *testing.T) {
		ts := testsuite.WorkflowTestSuite{}
		env := ts.NewTestWorkflowEnvironment()
		tl := &idleTimeline{start: env.Now()}
		rec := &callRecorder{}
		wireIdleActivities(env, timeout, tl, rec)

		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(SignalStop, struct{}{})
		}, timeout-time.Second)

		env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
		seq := statusSequence(tl.snapshot())
		assert.Equal(t, []session.Status{session.StatusAwaitingInput, session.StatusStopped}, seq)
		assert.Empty(t, statusChangeReasonsFor(t, rec.snapshotTerminalEvents(), session.StatusExpired))
	})

	t.Run("StopAtTimeoutWritesExactlyOneTerminal", func(t *testing.T) {
		ts := testsuite.WorkflowTestSuite{}
		env := ts.NewTestWorkflowEnvironment()
		tl := &idleTimeline{start: env.Now()}
		rec := &callRecorder{}
		wireIdleActivities(env, timeout, tl, rec)

		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(SignalStop, struct{}{})
		}, timeout)

		env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
		seq := statusSequence(tl.snapshot())
		require.Len(t, seq, 2, "awaiting_input then exactly one terminal status")
		assert.Contains(t, []session.Status{session.StatusStopped, session.StatusExpired}, seq[1])
		terminal := 0
		for _, s := range seq {
			if s.IsTerminal() {
				terminal++
			}
		}
		assert.Equal(t, 1, terminal)
		expired := statusChangeReasonsFor(t, rec.snapshotTerminalEvents(), session.StatusExpired)
		assert.LessOrEqual(t, len(expired), 1)
	})
}

// TestSessionWorkflow_SendTurnAtTimeout_TurnAndExpiryNeverOverlap proves a
// turn that wins the race with the clock is never interrupted by expiry, and
// that a clock which wins leaves no turn behind.
func TestSessionWorkflow_SendTurnAtTimeout_TurnAndExpiryNeverOverlap(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	const timeout = time.Minute
	tl := &idleTimeline{start: env.Now()}
	rec := &callRecorder{}
	wireIdleActivities(env, timeout, tl, rec)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "at the deadline"})
	}, timeout)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	seq := statusSequence(tl.snapshot())
	require.NotEmpty(t, seq)
	assert.Equal(t, session.StatusExpired, seq[len(seq)-1])
	for i, s := range seq {
		if s == session.StatusExpired {
			assert.Equal(t, len(seq)-1, i, "expired is written once, last")
			assert.Equal(t, session.StatusAwaitingInput, seq[i-1], "expiry never fires while a turn is running")
		}
	}
	turns := countOf(rec.snapshotLog(), "CommitTurn")
	if turns == 0 {
		assert.Equal(t, []session.Status{session.StatusAwaitingInput, session.StatusExpired}, seq)
	} else {
		assert.Equal(t, 1, turns)
		assert.Contains(t, seq, session.StatusRunning)
	}
}

// TestSessionWorkflow_ArmIdleTimerSweep_IsIdempotent proves the pre-deploy
// sweep arms the clock once with the timeout from its first signal, and a
// repeat sweep does not re-arm or extend it. A run with no known timeout
// starts no clock until the first ArmIdleTimer arrives.
func TestSessionWorkflow_ArmIdleTimerSweep_IsIdempotent(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	tl := &idleTimeline{start: env.Now()}
	rec := &callRecorder{}
	wireIdleActivities(env, 0, tl, rec)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalArmIdleTimer, ArmIdleTimerSignal{EffectiveTimeout: time.Minute})
	}, 10*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalArmIdleTimer, ArmIdleTimerSignal{EffectiveTimeout: 10 * time.Minute})
	}, 30*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	statuses := tl.snapshot()
	require.Len(t, statuses, 2)
	assert.Equal(t, session.StatusAwaitingInput, statuses[0].status)
	assert.Equal(t, 10*time.Second+time.Minute, statuses[1].at, "the first sweep's timeout arms the clock from its arrival; the second does not extend it")
	assert.Equal(t, session.StatusExpired, statuses[1].status)
}

func countOf(log []string, want string) int {
	n := 0
	for _, s := range log {
		if s == want {
			n++
		}
	}
	return n
}
