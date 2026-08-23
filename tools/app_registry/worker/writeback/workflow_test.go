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
