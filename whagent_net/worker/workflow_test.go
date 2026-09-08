package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/whagent_net/llm"
	"github.com/whale-net/everything/whagent_net/session"
)

// registerActivityStubs registers a placeholder function under each
// activity name SessionWorkflow dispatches by string (the Activity* name
// constants in activities.go) -- mirrors
// audience_score_system/worker/sync/workflow_test.go's
// registerActivityStubs: testsuite's OnActivity(name, ...) requires the
// name to already be a registered activity (with a matching signature)
// before it can be mocked; the bodies here are never reached, OnActivity's
// mock intercepts the call first.
func registerActivityStubs(env *testsuite.TestWorkflowEnvironment) {
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
	env.RegisterActivityWithOptions(func(ctx context.Context, in UpdateSessionStatusInput) (UpdateSessionStatusResult, error) {
		return UpdateSessionStatusResult{}, nil
	}, activity.RegisterOptions{Name: ActivityUpdateSessionStatus})
}

func testSessionID() uuid.UUID {
	return uuid.MustParse("22222222-2222-2222-2222-222222222222")
}

// statusTracker records every UpdateSessionStatus call's status, in order
// -- a small thread-safe helper shared by the tests below, since
// testsuite's mocked activity handlers can run from a different goroutine
// than the test's own assertions.
type statusTracker struct {
	mu       sync.Mutex
	statuses []session.Status
}

func (s *statusTracker) record(status session.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, status)
}

func (s *statusTracker) snapshot() []session.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]session.Status, len(s.statuses))
	copy(out, s.statuses)
	return out
}

// mockHappyPathActivities wires ResolveAgentDefinition/BuildContext/
// CallModel/CommitTurn/UpdateSessionStatus to always succeed instantly,
// tracking UpdateSessionStatus's status sequence into tracker and
// CommitTurn's turn sequence into commits. Shared setup for the tests
// below that only care about the workflow's control flow, not any one
// activity's specific behavior.
func mockHappyPathActivities(env *testsuite.TestWorkflowEnvironment, tracker *statusTracker, commits *[]int, commitsMu *sync.Mutex) {
	env.OnActivity(ActivityUpdateSessionStatus, mock.Anything, mock.Anything).
		Return(UpdateSessionStatusResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(UpdateSessionStatusInput)
			tracker.record(in.Status)
		})
	env.OnActivity(ActivityResolveAgentDefinition, mock.Anything, mock.Anything).
		Return(ResolveAgentDefinitionResult{Model: "test-model"}, nil)
	env.OnActivity(ActivityBuildContext, mock.Anything, mock.Anything).
		Return(BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}}, nil)
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).
		Return(CallModelResult{Response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}}, nil)
	env.OnActivity(ActivityCommitTurn, mock.Anything, mock.Anything).
		Return(CommitTurnResult{Done: false}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(CommitTurnInput)
			commitsMu.Lock()
			*commits = append(*commits, in.Turn)
			commitsMu.Unlock()
		})
}

// TestSessionWorkflow_BlocksThenProcessesTurn_DoesNotCompleteBetweenTurns
// proves the shape of the durable loop (issue #2114's Testing phase,
// first bullet): the workflow starts, blocks in `awaiting_input`,
// processes exactly one signalled turn, and returns to `awaiting_input`
// -- it does not complete once the turn is done, since a signal-per-turn
// session only ever ends via Stop. testsuite's TestWorkflowEnvironment
// always fast-forwards its virtual clock to the furthest pending event
// (there is no "settle and stay open forever" state to observe directly),
// so a later Stop -- delivered only after the turn has already finished
// and the workflow has looped back to awaiting_input -- is what actually
// proves the workflow did not complete on its own between turns: if it
// had, that Stop signal would never reach a still-running execution and
// the tracked status sequence would never reach `stopped`.
func TestSessionWorkflow_BlocksThenProcessesTurn_DoesNotCompleteBetweenTurns(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	tracker := &statusTracker{}
	var commits []int
	var commitsMu sync.Mutex
	mockHappyPathActivities(env, tracker, &commits, &commitsMu)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "hello"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t,
		[]session.Status{session.StatusAwaitingInput, session.StatusRunning, session.StatusAwaitingInput, session.StatusStopped},
		tracker.snapshot(),
		"the session must return to awaiting_input once the turn finishes (not stay running or jump to done) and must keep waiting there -- only the later Stop signal ends the run, proving the workflow did not complete on its own between turns",
	)
	require.Equal(t, []int{1}, commits)
}

// TestSessionWorkflow_TwoSequentialTurns_OrderedNoDuplication proves two
// turns signalled in sequence each commit their own turn, in order, with
// no duplication (issue #2114's Testing phase, second bullet). A Stop
// after both turns ends the run cleanly so the full call sequence can be
// asserted deterministically once the workflow settles.
func TestSessionWorkflow_TwoSequentialTurns_OrderedNoDuplication(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	tracker := &statusTracker{}
	var commits []int
	var commitsMu sync.Mutex
	mockHappyPathActivities(env, tracker, &commits, &commitsMu)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn two"})
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 3*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []int{1, 2}, commits, "each signalled turn must commit exactly once, in order, with no duplication")
	require.Equal(t,
		[]session.Status{
			session.StatusAwaitingInput,                        // initial
			session.StatusRunning, session.StatusAwaitingInput, // turn 1
			session.StatusRunning, session.StatusAwaitingInput, // turn 2
			session.StatusStopped, // Stop between turns
		},
		tracker.snapshot(),
	)
}

// TestSessionWorkflow_StopDuringInFlightActivity_CancelsAndEndsStopped
// proves FR1's emergency-brake semantics (issue #2114's Testing phase,
// "cancellation" bullet): a Stop that arrives while CallModel is in
// flight cancels it rather than waiting for it to finish, and the session
// ends `stopped` with whatever had already committed (nothing from the
// cancelled turn) still present -- CommitTurn is never reached, since the
// model call that would have fed it never completed.
func TestSessionWorkflow_StopDuringInFlightActivity_CancelsAndEndsStopped(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	env.RegisterActivityWithOptions(func(ctx context.Context, sessionID uuid.UUID) (ResolveAgentDefinitionResult, error) {
		return ResolveAgentDefinitionResult{}, nil
	}, activity.RegisterOptions{Name: ActivityResolveAgentDefinition})
	env.RegisterActivityWithOptions(func(ctx context.Context, in BuildContextInput) (BuildContextResult, error) {
		return BuildContextResult{}, nil
	}, activity.RegisterOptions{Name: ActivityBuildContext})
	env.RegisterActivityWithOptions(func(ctx context.Context, in CommitTurnInput) (CommitTurnResult, error) {
		return CommitTurnResult{}, nil
	}, activity.RegisterOptions{Name: ActivityCommitTurn})
	env.RegisterActivityWithOptions(func(ctx context.Context, in UpdateSessionStatusInput) (UpdateSessionStatusResult, error) {
		return UpdateSessionStatusResult{}, nil
	}, activity.RegisterOptions{Name: ActivityUpdateSessionStatus})
	// CallModel is a REAL registered activity (not an OnActivity mock) that
	// blocks until its context is cancelled -- this is what lets the test
	// prove actual cancellation propagation (FR1: "cancelled, not allowed
	// to run to completion"), rather than merely a mocked delay expiring on
	// its own. Every RegisterActivityWithOptions call must precede any
	// OnActivity mock (testsuite panics otherwise), so this is grouped with
	// the other real registrations above rather than declared next to its
	// own explanatory comment further down.
	callModelStarted := make(chan struct{})
	env.RegisterActivityWithOptions(func(ctx context.Context, in CallModelInput) (CallModelResult, error) {
		close(callModelStarted)
		<-ctx.Done()
		return CallModelResult{}, ctx.Err()
	}, activity.RegisterOptions{Name: ActivityCallModel})

	tracker := &statusTracker{}
	env.OnActivity(ActivityUpdateSessionStatus, mock.Anything, mock.Anything).
		Return(UpdateSessionStatusResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(UpdateSessionStatusInput)
			tracker.record(in.Status)
		})
	env.OnActivity(ActivityResolveAgentDefinition, mock.Anything, mock.Anything).
		Return(ResolveAgentDefinitionResult{Model: "test-model"}, nil)
	env.OnActivity(ActivityBuildContext, mock.Anything, mock.Anything).
		Return(BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}}, nil)
	var commitCalled bool
	env.OnActivity(ActivityCommitTurn, mock.Anything, mock.Anything).
		Return(CommitTurnResult{}, nil).
		Run(func(args mock.Arguments) { commitCalled = true })

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "hello"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 2*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.False(t, commitCalled, "CommitTurn must never run -- the in-flight CallModel was cancelled, not allowed to complete")
	statuses := tracker.snapshot()
	require.NotEmpty(t, statuses)
	require.Equal(t, session.StatusStopped, statuses[len(statuses)-1], "the session must end stopped, not awaiting_input or done")
	require.Contains(t, statuses, session.StatusRunning, "the turn must have actually started (running) before the stop cancelled it")
}

// TestSessionWorkflow_DefinitionChangedBetweenTurns_PickedUpFreshOnSecondTurn
// proves the agent definition is resolved fresh every turn, never cached
// in workflow state (issue #2114's Testing phase, last bullet): turn 1
// and turn 2 each get ResolveAgentDefinition's own return value, even
// though the model differs between the two calls -- exactly what would
// break if a future edit started caching turn 1's ResolveAgentDefinition
// result in a workflow-level variable and reusing it. Red/green
// discipline (issue body): temporarily caching resolved.Model across
// turns in processTurn (workflow.go) turns this test red -- turn 2's
// observed model stays "model-v1" instead of "model-v2" -- and reverting
// that cache turns it green again; this was exercised by hand while
// writing this test (not left in the tree).
func TestSessionWorkflow_DefinitionChangedBetweenTurns_PickedUpFreshOnSecondTurn(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	tracker := &statusTracker{}
	env.OnActivity(ActivityUpdateSessionStatus, mock.Anything, mock.Anything).
		Return(UpdateSessionStatusResult{}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(UpdateSessionStatusInput)
			tracker.record(in.Status)
		})
	env.OnActivity(ActivityBuildContext, mock.Anything, mock.Anything).
		Return(BuildContextResult{EventIDs: []uuid.UUID{uuid.New()}}, nil)
	env.OnActivity(ActivityCommitTurn, mock.Anything, mock.Anything).
		Return(CommitTurnResult{Done: false}, nil)

	// ResolveAgentDefinition returns a different model on each call --
	// simulating the definition drifting between turn 1 and turn 2 (a
	// reassignment landing mid-session).
	var resolveCalls int
	var resolveMu sync.Mutex
	env.OnActivity(ActivityResolveAgentDefinition, mock.Anything, mock.Anything).
		Return(func(ctx context.Context, sessionID uuid.UUID) (ResolveAgentDefinitionResult, error) {
			resolveMu.Lock()
			resolveCalls++
			n := resolveCalls
			resolveMu.Unlock()
			if n == 1 {
				return ResolveAgentDefinitionResult{Model: "model-v1"}, nil
			}
			return ResolveAgentDefinitionResult{Model: "model-v2"}, nil
		})

	var models []string
	var modelsMu sync.Mutex
	env.OnActivity(ActivityCallModel, mock.Anything, mock.Anything).
		Return(CallModelResult{Response: llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: "ok"}}}, nil).
		Run(func(args mock.Arguments) {
			in := args.Get(1).(CallModelInput)
			modelsMu.Lock()
			models = append(models, in.Model)
			modelsMu.Unlock()
		})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn one"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalSendTurn, SendTurnSignal{Input: "turn two"})
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalStop, struct{}{})
	}, 3*time.Second)

	env.ExecuteWorkflow(SessionWorkflow, SessionWorkflowInput{SessionID: testSessionID()})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{"model-v1", "model-v2"}, models,
		"turn 2 must observe the drifted definition -- ResolveAgentDefinition must run fresh every turn, never cached in workflow state")
}
