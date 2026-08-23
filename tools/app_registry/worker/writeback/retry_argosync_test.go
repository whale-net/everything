package writeback

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

// TestRetryArgoSyncWorkflow_RunsRefreshThenPoll proves the FR12 wiring:
// TriggerArgoRefresh then PollArgoSyncStatus, in that order, both invoked
// with in.IsRetry threaded through unchanged (so their promotion_sync_event
// writes land as retry_triggered/retry_observed, not the default pair --
// see ArgoSyncInput.IsRetry's doc comment) -- and the workflow's own result
// is exactly what PollArgoSyncStatus returned.
func TestRetryArgoSyncWorkflow_RunsRefreshThenPoll(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ArgoSyncInput{PromotionID: "promo-retry-1", Domain: "acme", ApplicationName: "foo-stage", IsRetry: true}
	want := ArgoSyncResult{SyncStatus: "Synced", HealthStatus: "Healthy", Terminal: true}

	var calls []string
	env.OnActivity(ActivityTriggerArgoRefresh, mock.Anything, in).Return(nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityTriggerArgoRefresh) })
	env.OnActivity(ActivityPollArgoSyncStatus, mock.Anything, in).Return(want, nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityPollArgoSyncStatus) })

	env.ExecuteWorkflow(RetryArgoSyncWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)

	require.Equal(t, []string{ActivityTriggerArgoRefresh, ActivityPollArgoSyncStatus}, calls,
		"RetryArgoSyncWorkflow must dispatch TriggerArgoRefresh then PollArgoSyncStatus, in that order")

	var got ArgoSyncResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, want, got)
}

// TestRetryArgoSyncWorkflow_TriggerArgoRefreshFailure_FailsWorkflow is the
// deliberate INVERSE of WritebackWorkflow's own best-effort contract
// (TestWritebackWorkflow_ArgoSyncFailureDoesNotFailWorkflow, workflow_test.go):
// RetryArgoSyncWorkflow's entire purpose IS the retry, so a TriggerArgoRefresh
// failure must fail the workflow outright, and PollArgoSyncStatus must never
// even run -- see RetryArgoSyncWorkflow's doc comment on why this is not the
// tail-step treatment WritebackWorkflow gives the same two activities.
func TestRetryArgoSyncWorkflow_TriggerArgoRefreshFailure_FailsWorkflow(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ArgoSyncInput{PromotionID: "promo-retry-2", Domain: "acme", ApplicationName: "foo-stage", IsRetry: true}

	var polled bool
	env.OnActivity(ActivityTriggerArgoRefresh, mock.Anything, in).
		Return(errors.New("argocd unreachable")) // retried per RetryArgoSyncWorkflow's ActivityOptions.RetryPolicy, then propagated
	env.OnActivity(ActivityPollArgoSyncStatus, mock.Anything, in).Return(ArgoSyncResult{}, nil).
		Run(func(args mock.Arguments) { polled = true })

	env.ExecuteWorkflow(RetryArgoSyncWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "a failed TriggerArgoRefresh must fail RetryArgoSyncWorkflow -- unlike WritebackWorkflow's best-effort tail treatment of the same activity")
	require.False(t, polled, "PollArgoSyncStatus must never run once TriggerArgoRefresh has failed")
}

// TestRetryArgoSyncWorkflow_PollArgoSyncStatusFailure_FailsWorkflow is the
// same INVERSE proof for PollArgoSyncStatus specifically: a genuine activity
// call failure (as opposed to FR5's "exhausted without reaching terminal",
// which is that activity's own defined success case, covered separately in
// argosync_test.go) fails RetryArgoSyncWorkflow, unlike
// TestWritebackWorkflow_PollArgoSyncStatusFailureDoesNotFailWorkflow's
// swallow of the identical error for the writeback path.
func TestRetryArgoSyncWorkflow_PollArgoSyncStatusFailure_FailsWorkflow(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ArgoSyncInput{PromotionID: "promo-retry-3", Domain: "acme", ApplicationName: "foo-stage", IsRetry: true}

	env.OnActivity(ActivityTriggerArgoRefresh, mock.Anything, in).Return(nil).Once()
	env.OnActivity(ActivityPollArgoSyncStatus, mock.Anything, in).
		Return(ArgoSyncResult{}, errors.New("argocd unreachable"))

	env.ExecuteWorkflow(RetryArgoSyncWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "a failed PollArgoSyncStatus must fail RetryArgoSyncWorkflow -- unlike WritebackWorkflow's best-effort tail treatment of the same activity")
}

// TestRetryArgoSyncWorkflowID_Format proves the "retry-<promotion_id>-<suffix>"
// scheme documented on RetryArgoSyncWorkflowID -- a pure string builder, no
// clock read of its own.
func TestRetryArgoSyncWorkflowID_Format(t *testing.T) {
	got := RetryArgoSyncWorkflowID("promo-abc", 12345)
	require.Equal(t, "retry-promo-abc-12345", got)
}

// TestRetryArgoSyncWorkflowID_DistinctSuffixesNeverCollide proves FR12's
// "an admin must be able to click retry more than once for the same
// promotion, each starting its own execution" -- two different suffixes for
// the same promotion_id produce two different workflow ids, so neither
// ExecuteWorkflow call could collide with the other via Temporal's own
// workflow-id dedup.
func TestRetryArgoSyncWorkflowID_DistinctSuffixesNeverCollide(t *testing.T) {
	first := RetryArgoSyncWorkflowID("promo-xyz", 1)
	second := RetryArgoSyncWorkflowID("promo-xyz", 2)
	require.NotEqual(t, first, second)
}

// TestRetryArgoSyncWorkflowID_NeverBarePromotionID proves the id is never
// merely the promotion_id itself -- that id is permanently owned by the
// promotion's original, by-then-terminal WritebackWorkflow (see
// RetryArgoSyncWorkflowID's doc comment); a bare promotion_id here would
// collide with that execution's own workflow id.
func TestRetryArgoSyncWorkflowID_NeverBarePromotionID(t *testing.T) {
	promotionID := "promo-bare"
	got := RetryArgoSyncWorkflowID(promotionID, 999)
	require.NotEqual(t, promotionID, got)
}
