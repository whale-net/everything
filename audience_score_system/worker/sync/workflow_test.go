package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/audience_score_system/store"
)

// registerActivityStubs registers a placeholder function under each
// activity name ChannelSyncWorkflow dispatches by string (see the
// Activity* name constants in workflow.go) -- mirrors
// tools/app_registry/worker/release/workflow_test.go's
// registerActivityStubs: testsuite's OnActivity(name, ...) requires the
// name to already be a registered activity (with a matching signature)
// before it can be mocked. The bodies here are never reached: OnActivity's
// mock intercepts the call before the registered function runs.
func registerActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(func(ctx context.Context, channelID uuid.UUID) (ChannelState, error) {
		return ChannelState{}, nil
	}, activity.RegisterOptions{Name: ActivityLoadChannelState})
	env.RegisterActivityWithOptions(func(ctx context.Context, channelID uuid.UUID) error {
		return nil
	}, activity.RegisterOptions{Name: ActivitySyncSchedule})
	env.RegisterActivityWithOptions(func(ctx context.Context, channelID uuid.UUID) error {
		return nil
	}, activity.RegisterOptions{Name: ActivitySyncOutcomes})
}

func testChannelID() uuid.UUID {
	return uuid.MustParse("11111111-1111-1111-1111-111111111111")
}

// TestChannelSyncWorkflow_NeedsReauth_SkipsCleanly proves FR14's skip
// gate: a needs_reauth Channel completes the workflow run successfully
// without invoking either sync activity -- no retry, no quota burn.
func TestChannelSyncWorkflow_NeedsReauth_SkipsCleanly(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	channelID := testChannelID()
	// SyncSchedule/SyncOutcomes are given .Maybe() expectations (0-or-more
	// calls, tracked into calledSync) rather than left unmocked -- mirrors
	// tools/app_registry/worker/release/workflow_test.go's
	// TestReleaseWorkflow_DispatchBuildFailure_MarksTargetsFailed comment:
	// ChannelSyncWorkflow's fixed control flow never reaches them once the
	// needs_reauth skip fires, but if a regression changed that, an
	// unmocked call here would just fall through to registerActivityStubs'
	// placeholder and silently "succeed" rather than failing this test --
	// .Maybe() plus the explicit calledSync assertion below is what
	// actually proves the negative. (env.AssertNotCalled's bool return is
	// deliberately NOT used for this: it swallows failures against a
	// dummyT internally and short-circuits before ever calling the real
	// *testing.T, so an ignored/misused AssertNotCalled call here would not
	// fail the test.)
	var calledSync []string
	env.OnActivity(ActivityLoadChannelState, mock.Anything, channelID).
		Return(ChannelState{ConnectionState: store.ConnectionStateNeedsReauth}, nil).Once()
	env.OnActivity(ActivitySyncSchedule, mock.Anything, mock.Anything).Return(nil).Maybe().
		Run(func(args mock.Arguments) { calledSync = append(calledSync, ActivitySyncSchedule) })
	env.OnActivity(ActivitySyncOutcomes, mock.Anything, mock.Anything).Return(nil).Maybe().
		Run(func(args mock.Arguments) { calledSync = append(calledSync, ActivitySyncOutcomes) })

	env.ExecuteWorkflow(ChannelSyncWorkflow, ChannelSyncInput{ChannelID: channelID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Empty(t, calledSync, "a needs_reauth Channel must skip both sync activities (FR14)")
}

// TestChannelSyncWorkflow_Connected_RunsBothActivitiesInOrder proves a
// connected Channel's happy path: LoadChannelState -> SyncSchedule ->
// SyncOutcomes, each invoked exactly once, in that order.
func TestChannelSyncWorkflow_Connected_RunsBothActivitiesInOrder(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	channelID := testChannelID()
	var calls []string
	env.OnActivity(ActivityLoadChannelState, mock.Anything, channelID).
		Return(ChannelState{ConnectionState: store.ConnectionStateConnected}, nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityLoadChannelState) })
	env.OnActivity(ActivitySyncSchedule, mock.Anything, channelID).Return(nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivitySyncSchedule) })
	env.OnActivity(ActivitySyncOutcomes, mock.Anything, channelID).Return(nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivitySyncOutcomes) })

	env.ExecuteWorkflow(ChannelSyncWorkflow, ChannelSyncInput{ChannelID: channelID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, []string{ActivityLoadChannelState, ActivitySyncSchedule, ActivitySyncOutcomes}, calls)
}

// revokedErr builds the exact error shape an activity constructs on
// youtube.ErrRevoked (see RevokedErrorType's doc comment): a non-retryable
// temporal.ApplicationError of RevokedErrorType.
func revokedErr(msg string) error {
	return temporal.NewNonRetryableApplicationError(msg, RevokedErrorType, errors.New("revoked"))
}

// TestChannelSyncWorkflow_SyncScheduleRevoked_EndsCleanly proves FR4/FR14:
// a RevokedErrorType error from SyncSchedule ends the workflow run
// successfully (not a workflow failure) and SyncOutcomes never runs.
func TestChannelSyncWorkflow_SyncScheduleRevoked_EndsCleanly(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	channelID := testChannelID()
	var calledSyncOutcomes bool
	env.OnActivity(ActivityLoadChannelState, mock.Anything, channelID).
		Return(ChannelState{ConnectionState: store.ConnectionStateConnected}, nil).Once()
	env.OnActivity(ActivitySyncSchedule, mock.Anything, channelID).
		Return(revokedErr("credential revoked")).Once()
	env.OnActivity(ActivitySyncOutcomes, mock.Anything, mock.Anything).Return(nil).Maybe().
		Run(func(args mock.Arguments) { calledSyncOutcomes = true })

	env.ExecuteWorkflow(ChannelSyncWorkflow, ChannelSyncInput{ChannelID: channelID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a revoked-credential error must end the run cleanly, not fail the workflow")
	require.False(t, calledSyncOutcomes, "SyncOutcomes must not run once SyncSchedule reports a revoked credential")
}

// TestChannelSyncWorkflow_SyncOutcomesRevoked_EndsCleanly is the same
// assertion as above for the SyncOutcomes leg -- both activities share the
// isRevoked contract (workflow.go).
func TestChannelSyncWorkflow_SyncOutcomesRevoked_EndsCleanly(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	channelID := testChannelID()
	env.OnActivity(ActivityLoadChannelState, mock.Anything, channelID).
		Return(ChannelState{ConnectionState: store.ConnectionStateConnected}, nil).Once()
	env.OnActivity(ActivitySyncSchedule, mock.Anything, channelID).Return(nil).Once()
	env.OnActivity(ActivitySyncOutcomes, mock.Anything, channelID).
		Return(revokedErr("credential revoked")).Once()

	env.ExecuteWorkflow(ChannelSyncWorkflow, ChannelSyncInput{ChannelID: channelID})

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a revoked-credential error must end the run cleanly, not fail the workflow")
}

// TestChannelSyncWorkflow_TransientError_FailsWorkflowAfterBoundedRetries
// proves FR4's "bounded retry, never infinite" contract: a plain
// (retryable) activity error is retried per defaultActivityOptions'
// RetryPolicy (MaximumAttempts: 5) and, once exhausted, fails the
// workflow rather than hanging or silently succeeding.
func TestChannelSyncWorkflow_TransientError_FailsWorkflowAfterBoundedRetries(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	channelID := testChannelID()
	attempts := 0
	env.OnActivity(ActivityLoadChannelState, mock.Anything, channelID).
		Return(ChannelState{ConnectionState: store.ConnectionStateConnected}, nil).Once()
	env.OnActivity(ActivitySyncSchedule, mock.Anything, channelID).
		Return(errors.New("youtube quota exceeded")).Times(5).
		Run(func(args mock.Arguments) { attempts++ })
	env.OnActivity(ActivitySyncOutcomes, mock.Anything, mock.Anything).Return(nil).Maybe()

	env.ExecuteWorkflow(ChannelSyncWorkflow, ChannelSyncInput{ChannelID: channelID})

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "a transient error must eventually fail the workflow once retries are exhausted")
	require.Equal(t, 5, attempts, "MaximumAttempts: 5 bounds retries -- must not retry forever")
	env.AssertNotCalled(t, ActivitySyncOutcomes, mock.Anything, mock.Anything)
}

// TestChannelSyncWorkflow_NeedsReauthThenConnected_AutoResumesOnNextRun is
// FR14's auto-resume assertion: a needs_reauth run skips cleanly, and a
// subsequent run (simulating the next scheduled cycle re-checking state
// after a reconnect) invokes both sync activities -- no manual step, no
// schedule re-creation required.
func TestChannelSyncWorkflow_NeedsReauthThenConnected_AutoResumesOnNextRun(t *testing.T) {
	channelID := testChannelID()

	// First run: needs_reauth, skips cleanly.
	t.Run("first run skips", func(t *testing.T) {
		ts := testsuite.WorkflowTestSuite{}
		env := ts.NewTestWorkflowEnvironment()
		registerActivityStubs(env)

		var calledSync []string
		env.OnActivity(ActivityLoadChannelState, mock.Anything, channelID).
			Return(ChannelState{ConnectionState: store.ConnectionStateNeedsReauth}, nil).Once()
		env.OnActivity(ActivitySyncSchedule, mock.Anything, mock.Anything).Return(nil).Maybe().
			Run(func(args mock.Arguments) { calledSync = append(calledSync, ActivitySyncSchedule) })
		env.OnActivity(ActivitySyncOutcomes, mock.Anything, mock.Anything).Return(nil).Maybe().
			Run(func(args mock.Arguments) { calledSync = append(calledSync, ActivitySyncOutcomes) })

		env.ExecuteWorkflow(ChannelSyncWorkflow, ChannelSyncInput{ChannelID: channelID})

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
		require.Empty(t, calledSync, "a needs_reauth Channel must skip both sync activities (FR14)")
	})

	// Second run (a fresh workflow execution, exactly as the next scheduled
	// cycle would produce -- ChannelSyncWorkflow re-reads connection_state
	// fresh on every run rather than caching it, see LoadChannelState's doc
	// comment): now connected, both activities run.
	t.Run("second run resumes", func(t *testing.T) {
		ts := testsuite.WorkflowTestSuite{}
		env := ts.NewTestWorkflowEnvironment()
		registerActivityStubs(env)

		var calls []string
		env.OnActivity(ActivityLoadChannelState, mock.Anything, channelID).
			Return(ChannelState{ConnectionState: store.ConnectionStateConnected}, nil).Once().
			Run(func(args mock.Arguments) { calls = append(calls, ActivityLoadChannelState) })
		env.OnActivity(ActivitySyncSchedule, mock.Anything, channelID).Return(nil).Once().
			Run(func(args mock.Arguments) { calls = append(calls, ActivitySyncSchedule) })
		env.OnActivity(ActivitySyncOutcomes, mock.Anything, channelID).Return(nil).Once().
			Run(func(args mock.Arguments) { calls = append(calls, ActivitySyncOutcomes) })

		env.ExecuteWorkflow(ChannelSyncWorkflow, ChannelSyncInput{ChannelID: channelID})

		require.True(t, env.IsWorkflowCompleted())
		require.NoError(t, env.GetWorkflowError())
		require.Equal(t, []string{ActivityLoadChannelState, ActivitySyncSchedule, ActivitySyncOutcomes}, calls,
			"a reconnect must resume syncing automatically on the next scheduled run (FR14)")
	})
}
