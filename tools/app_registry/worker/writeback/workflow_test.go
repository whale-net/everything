package writeback

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

// registerActivityStubs registers a placeholder function under each
// activity name WritebackWorkflow dispatches by string (see
// ActivityRenderEnvironmentState/ActivityPublish) -- the testsuite's
// OnActivity(name, ...) requires the name to already be a registered
// activity before it can be mocked, since it validates against the real
// signature. The bodies here are never reached: OnActivity's mock
// intercepts the call before the registered function runs.
func registerActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(func(ctx context.Context, in WritebackInput) (RenderedState, error) {
		return RenderedState{}, nil
	}, activity.RegisterOptions{Name: ActivityRenderEnvironmentState})
	env.RegisterActivityWithOptions(func(ctx context.Context, state RenderedState) (PublishResult, error) {
		return PublishResult{}, nil
	}, activity.RegisterOptions{Name: ActivityPublish})
	env.RegisterActivityWithOptions(func(ctx context.Context, promotionID, location, commitSHA string) error {
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordWritebackResult})
	env.RegisterActivityWithOptions(func(ctx context.Context, in ArgoSyncInput) error {
		return nil
	}, activity.RegisterOptions{Name: ActivityTriggerArgoRefresh})
	env.RegisterActivityWithOptions(func(ctx context.Context, in ArgoSyncInput) (ArgoSyncResult, error) {
		return ArgoSyncResult{}, nil
	}, activity.RegisterOptions{Name: ActivityPollArgoSyncStatus})
}

// TestWritebackWorkflow_RendersThenPublishes proves the workflow's shape:
// it calls RenderEnvironmentState, feeds the result into Publish, and
// returns Publish's result -- using the Temporal SDK's testsuite, which
// runs workflow code against a simulated environment with no real Temporal
// server (see libs/go/temporal/README.md's "Testing" section for why
// NewClient itself is not unit tested the same way).
func TestWritebackWorkflow_RendersThenPublishes(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := WritebackInput{PromotionID: "promo-1", EnvironmentKey: "dev", StateHash: "hash-1"}
	rendered := RenderedState{EnvironmentKey: "dev", StateHash: "hash-1", Document: []byte(`{"ok":true}`)}
	want := PublishResult{Location: "/tmp/dev.json"}

	env.OnActivity(ActivityRenderEnvironmentState, mock.Anything, in).Return(rendered, nil).Once()
	env.OnActivity(ActivityPublish, mock.Anything, rendered).Return(want, nil).Once()

	env.ExecuteWorkflow(WritebackWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got PublishResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, want, got)
}

// TestWritebackWorkflow_RecordsResultAfterPublish proves WritebackWorkflow
// calls ActivityRecordWritebackResult with Publish's own
// Location/CommitSHA immediately after Publish succeeds -- FR7a, issue
// #1029. Uses .Once() on both the Publish and RecordWritebackResult mocks
// so a regression that dropped (or duplicated) the second call fails this
// test via testify/mock's own unmet-expectation assertion in
// env.AssertExpectations, not just via the returned workflow result.
func TestWritebackWorkflow_RecordsResultAfterPublish(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := WritebackInput{PromotionID: "promo-3", EnvironmentKey: "dev", StateHash: "hash-3"}
	rendered := RenderedState{EnvironmentKey: "dev", StateHash: "hash-3", Document: []byte(`{"ok":true}`)}
	want := PublishResult{Location: "app-registry/app-registry/versions/dev.yaml", CommitSHA: "deadbeefcafef00d"}

	env.OnActivity(ActivityRenderEnvironmentState, mock.Anything, in).Return(rendered, nil).Once()
	env.OnActivity(ActivityPublish, mock.Anything, rendered).Return(want, nil).Once()
	env.OnActivity(ActivityRecordWritebackResult, mock.Anything, in.PromotionID, want.Location, want.CommitSHA).Return(nil).Once()

	env.ExecuteWorkflow(WritebackWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)

	var got PublishResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, want, got)
}

// TestWritebackWorkflow_RecordResultFailureDoesNotFailWorkflow proves a
// failing (exhausted-retries) ActivityRecordWritebackResult call is
// swallowed -- WritebackWorkflow still completes successfully and still
// returns Publish's own result, per that best-effort gap documented on
// WritebackWorkflow and ActivityRecordWritebackResult.
func TestWritebackWorkflow_RecordResultFailureDoesNotFailWorkflow(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := WritebackInput{PromotionID: "promo-4", EnvironmentKey: "dev", StateHash: "hash-4"}
	rendered := RenderedState{EnvironmentKey: "dev", StateHash: "hash-4", Document: []byte(`{"ok":true}`)}
	want := PublishResult{Location: "app-registry/app-registry/versions/dev.yaml", CommitSHA: "cafebabe"}

	env.OnActivity(ActivityRenderEnvironmentState, mock.Anything, in).Return(rendered, nil).Once()
	env.OnActivity(ActivityPublish, mock.Anything, rendered).Return(want, nil).Once()
	env.OnActivity(ActivityRecordWritebackResult, mock.Anything, in.PromotionID, want.Location, want.CommitSHA).
		Return(errors.New("outbox row vanished")) // retried per WritebackWorkflow's ActivityOptions.RetryPolicy, then swallowed

	env.ExecuteWorkflow(WritebackWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a failed RecordWritebackResult must not fail the workflow")

	var got PublishResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, want, got, "the workflow must still return Publish's own result")
}

// TestWritebackWorkflow_RenderFailurePropagates proves a
// RenderEnvironmentState failure fails the workflow with that activity's
// error, exercising WritebackWorkflow's early return on the first
// ExecuteActivity call -- Publish is intentionally never mocked here, so a
// regression that called it anyway would still "succeed" via its
// registered-but-unmocked placeholder; this test's real assertion is on the
// workflow's returned error, not on Publish's absence.
func TestWritebackWorkflow_RenderFailurePropagates(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := WritebackInput{PromotionID: "promo-2", EnvironmentKey: "dev", StateHash: "hash-2"}
	env.OnActivity(ActivityRenderEnvironmentState, mock.Anything, in).Return(RenderedState{}, errors.New("registry unreachable"))

	env.ExecuteWorkflow(WritebackWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
}

// TestWritebackWorkflow_TriggersArgoSyncAfterRecordWritebackResult proves
// FR1-FR5's wiring into WritebackWorkflow: TriggerArgoRefresh then
// PollArgoSyncStatus are called, in that order, after
// RecordWritebackResult -- and ApplicationName is derived from the SAME
// RenderedState.ChartName/EnvironmentKey fields Publish already used
// ("<ChartName>-<EnvironmentKey>"), never re-derived a second way (see
// ArgoSyncInput's doc comment and issue #1030's Summary).
func TestWritebackWorkflow_TriggersArgoSyncAfterRecordWritebackResult(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := WritebackInput{PromotionID: "promo-5", EnvironmentKey: "stage", Domain: "acme", StateHash: "hash-5"}
	rendered := RenderedState{EnvironmentKey: "stage", Domain: "acme", ChartName: "foo", StateHash: "hash-5", Document: []byte(`{"ok":true}`)}
	want := PublishResult{Location: "acme/foo/versions/stage.yaml", CommitSHA: "cafef00d"}
	wantArgoIn := ArgoSyncInput{PromotionID: "promo-5", Domain: "acme", ApplicationName: "foo-stage"}

	var calls []string
	env.OnActivity(ActivityRenderEnvironmentState, mock.Anything, in).Return(rendered, nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityRenderEnvironmentState) })
	env.OnActivity(ActivityPublish, mock.Anything, rendered).Return(want, nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityPublish) })
	env.OnActivity(ActivityRecordWritebackResult, mock.Anything, in.PromotionID, want.Location, want.CommitSHA).Return(nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityRecordWritebackResult) })
	env.OnActivity(ActivityTriggerArgoRefresh, mock.Anything, wantArgoIn).Return(nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityTriggerArgoRefresh) })
	env.OnActivity(ActivityPollArgoSyncStatus, mock.Anything, wantArgoIn).Return(ArgoSyncResult{SyncStatus: "Synced", HealthStatus: "Healthy", Terminal: true}, nil).Once().
		Run(func(args mock.Arguments) { calls = append(calls, ActivityPollArgoSyncStatus) })

	env.ExecuteWorkflow(WritebackWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)

	require.Equal(t, []string{
		ActivityRenderEnvironmentState, ActivityPublish, ActivityRecordWritebackResult,
		ActivityTriggerArgoRefresh, ActivityPollArgoSyncStatus,
	}, calls, "WritebackWorkflow must dispatch activities in exactly this order")

	var got PublishResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, want, got)
}

// TestWritebackWorkflow_ArgoSyncFailureDoesNotFailWorkflow proves the
// best-effort contract on TriggerArgoRefresh/PollArgoSyncStatus (issue
// #1030 scope item 3, mirroring RecordWritebackResult's own best-effort
// gap): a TriggerArgoRefresh failure does not fail WritebackWorkflow's
// overall result, and PollArgoSyncStatus still runs afterward -- ArgoCD may
// already be mid-sync from an earlier auto-sync/refresh, so polling stays
// informative even when the explicit refresh call failed.
func TestWritebackWorkflow_ArgoSyncFailureDoesNotFailWorkflow(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := WritebackInput{PromotionID: "promo-6", EnvironmentKey: "dev", StateHash: "hash-6"}
	rendered := RenderedState{EnvironmentKey: "dev", StateHash: "hash-6", Document: []byte(`{"ok":true}`)}
	want := PublishResult{Location: "/tmp/dev.json"}

	var polled bool
	env.OnActivity(ActivityRenderEnvironmentState, mock.Anything, in).Return(rendered, nil).Once()
	env.OnActivity(ActivityPublish, mock.Anything, rendered).Return(want, nil).Once()
	env.OnActivity(ActivityRecordWritebackResult, mock.Anything, in.PromotionID, want.Location, want.CommitSHA).Return(nil).Once()
	env.OnActivity(ActivityTriggerArgoRefresh, mock.Anything, mock.Anything).
		Return(errors.New("argocd unreachable")) // retried per WritebackWorkflow's ActivityOptions.RetryPolicy, then swallowed
	env.OnActivity(ActivityPollArgoSyncStatus, mock.Anything, mock.Anything).Return(ArgoSyncResult{}, nil).Once().
		Run(func(args mock.Arguments) { polled = true })

	env.ExecuteWorkflow(WritebackWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a failed TriggerArgoRefresh must not fail the workflow")
	require.True(t, polled, "PollArgoSyncStatus must still run even when TriggerArgoRefresh failed")

	var got PublishResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, want, got, "the workflow must still return Publish's own result")
}

// TestWritebackWorkflow_PollArgoSyncStatusFailureDoesNotFailWorkflow is the
// same best-effort proof for PollArgoSyncStatus specifically: an activity
// call failure (as opposed to FR5's "still pending" success case, already
// covered in argosync_test.go) must not fail WritebackWorkflow either.
func TestWritebackWorkflow_PollArgoSyncStatusFailureDoesNotFailWorkflow(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := WritebackInput{PromotionID: "promo-7", EnvironmentKey: "dev", StateHash: "hash-7"}
	rendered := RenderedState{EnvironmentKey: "dev", StateHash: "hash-7", Document: []byte(`{"ok":true}`)}
	want := PublishResult{Location: "/tmp/dev.json"}

	env.OnActivity(ActivityRenderEnvironmentState, mock.Anything, in).Return(rendered, nil).Once()
	env.OnActivity(ActivityPublish, mock.Anything, rendered).Return(want, nil).Once()
	env.OnActivity(ActivityRecordWritebackResult, mock.Anything, in.PromotionID, want.Location, want.CommitSHA).Return(nil).Once()
	env.OnActivity(ActivityTriggerArgoRefresh, mock.Anything, mock.Anything).Return(nil).Once()
	env.OnActivity(ActivityPollArgoSyncStatus, mock.Anything, mock.Anything).
		Return(ArgoSyncResult{}, errors.New("argocd unreachable"))

	env.ExecuteWorkflow(WritebackWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError(), "a failed PollArgoSyncStatus must not fail the workflow")

	var got PublishResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, want, got, "the workflow must still return Publish's own result")
}
