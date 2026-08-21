package release

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// registerActivityStubs registers a placeholder function under each
// activity name ReleaseWorkflow dispatches by string (see the Activity*
// name constants in workflow.go) -- mirrors
// worker/writeback/workflow_test.go's registerActivityStubs: the testsuite's
// OnActivity(name, ...) requires the name to already be a registered
// activity before it can be mocked, since it validates against the real
// signature. The bodies here are never reached: OnActivity's mock
// intercepts the call before the registered function runs.
func registerActivityStubs(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions(func(ctx context.Context, releaseRunID string) (bool, error) {
		return true, nil
	}, activity.RegisterOptions{Name: ActivityCheckApproval})
	env.RegisterActivityWithOptions(func(ctx context.Context, targets []ReleaseTarget) (ResolvedPlan, error) {
		return ResolvedPlan{}, nil
	}, activity.RegisterOptions{Name: ActivityResolvePlan})
	env.RegisterActivityWithOptions(func(ctx context.Context, releaseRunID string, resolvedPlan []byte) error {
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordResolvedPlan})
	env.RegisterActivityWithOptions(func(ctx context.Context, plan ResolvedPlan, digests map[string]string) (BuildRef, error) {
		return BuildRef{}, nil
	}, activity.RegisterOptions{Name: ActivityDispatchBuild})
	env.RegisterActivityWithOptions(func(ctx context.Context, ref BuildRef) (BuildStatus, error) {
		return BuildStatus{}, nil
	}, activity.RegisterOptions{Name: ActivityPollBuild})
	env.RegisterActivityWithOptions(func(ctx context.Context, releaseRunID string) (VerifyResult, error) {
		return VerifyResult{}, nil
	}, activity.RegisterOptions{Name: ActivityVerifyPublished})
	env.RegisterActivityWithOptions(func(ctx context.Context, releaseRunID string, target ReleaseTarget, state repository.ReleaseRunTargetState, buildID, errorDetail string) error {
		return nil
	}, activity.RegisterOptions{Name: ActivityRecordTargetState})
}

func testTarget() ReleaseTarget {
	return ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
}

// TestReleaseWorkflow_HappyPath proves the workflow's shape (FR6): it calls
// CheckApproval -> ResolvePlan -> DispatchBuild -> PollBuild ->
// VerifyPublished -> RecordTargetState, in that order, and records every
// target succeeded when the build and verification both succeed.
func TestReleaseWorkflow_HappyPath(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-1", Targets: []ReleaseTarget{testTarget()}}
	plan := ResolvedPlan{ReleaseRunID: "run-1", Versions: map[string]string{testTarget().key(): "v1.0.1"}}
	ref := BuildRef{ReleaseRunID: "run-1", RunID: "42"}

	var calls []string
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-1").Return(true, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityCheckApproval) })
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityResolvePlan) })
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityDispatchBuild) })
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityPollBuild) })
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-1").Return(VerifyResult{AllPublished: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-1", testTarget(), repository.ReleaseRunTargetStateSucceeded, "42", "").
		Return(nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityRecordTargetState) })

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Equal(t, "run-1", got.ReleaseRunID)
	require.Len(t, got.Targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, got.Targets[0].State)

	require.Equal(t, []string{
		ActivityCheckApproval, ActivityResolvePlan, ActivityDispatchBuild,
		ActivityPollBuild, ActivityVerifyPublished, ActivityRecordTargetState,
	}, calls, "ReleaseWorkflow must dispatch activities in exactly this order (FR6)")
}

// TestReleaseWorkflow_DispatchBuildFailure_MarksTargetsFailed proves FR11's
// failure-propagation contract from issue #889's Testing scope: "a
// DispatchBuild failure should mark targets failed, not hang or silently
// succeed". PollBuild/VerifyPublished must never be called once
// DispatchBuild fails.
func TestReleaseWorkflow_DispatchBuildFailure_MarksTargetsFailed(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-2", Targets: []ReleaseTarget{testTarget()}}
	plan := ResolvedPlan{ReleaseRunID: "run-2", Versions: map[string]string{testTarget().key(): "v1.0.1"}}

	// PollBuild/VerifyPublished are given .Maybe() expectations (0-or-more
	// calls, tracked into calledPollOrVerify) rather than left unmocked:
	// ReleaseWorkflow's fixed control flow never reaches them once
	// DispatchBuild fails (an early return -- see workflow.go's
	// ReleaseWorkflow), but if a future regression changed that, an
	// unmocked activity call here would just fall through to
	// registerActivityStubs' placeholder and silently "succeed" rather than
	// failing this test -- .Maybe() plus the explicit assertion below is
	// what actually proves the negative.
	var calledPollOrVerify []string
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-2").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(BuildRef{}, errors.New("github unreachable")).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, mock.Anything).Return(BuildStatus{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledPollOrVerify = append(calledPollOrVerify, ActivityPollBuild) })
	env.OnActivity(ActivityVerifyPublished, mock.Anything, mock.Anything).Return(VerifyResult{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledPollOrVerify = append(calledPollOrVerify, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-2", testTarget(), repository.ReleaseRunTargetStateFailed, "", mock.AnythingOfType("string")).
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Empty(t, calledPollOrVerify, "PollBuild/VerifyPublished must not run once DispatchBuild has failed")
}

// TestReleaseWorkflow_VerifyPublished_PartialFailure proves a target
// VerifyPublished reports as not-published is recorded Failed while a
// sibling target that did publish is recorded Succeeded in the same
// workflow execution.
func TestReleaseWorkflow_VerifyPublished_PartialFailure(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	ok := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
	bad := ReleaseTarget{OwnerFullName: "demo-gadget", Kind: repository.ArtifactKindImage}
	in := ReleaseWorkflowInput{ReleaseRunID: "run-3", Targets: []ReleaseTarget{ok, bad}}
	plan := ResolvedPlan{ReleaseRunID: "run-3", Versions: map[string]string{ok.key(): "v1.0.1", bad.key(): "v1.0.1"}}
	ref := BuildRef{ReleaseRunID: "run-3", RunID: "43"}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-3").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-3").Return(VerifyResult{
		AllPublished: false,
		Failed:       map[string]string{bad.key(): "no published artifact found"},
	}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-3", ok, repository.ReleaseRunTargetStateSucceeded, "43", "").Return(nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-3", bad, repository.ReleaseRunTargetStateFailed, "43", "no published artifact found").Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Len(t, got.Targets, 2)
	byOwner := map[string]repository.ReleaseRunTargetState{}
	for _, tr := range got.Targets {
		byOwner[tr.OwnerFullName] = tr.State
	}
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, byOwner["demo-widget"])
	require.Equal(t, repository.ReleaseRunTargetStateFailed, byOwner["demo-gadget"])
}

// TestReleaseWorkflow_RecordResolvedPlan_DispatchedWhenPlanHasRawJSON
// proves issue #906's new dispatch step: when ResolvePlan returns a plan
// with non-empty RawJSON, ReleaseWorkflow calls RecordResolvedPlan with
// that exact releaseRunID/RawJSON pair immediately after ResolvePlan and
// before DispatchBuild -- the ordering
// TestReleaseWorkflow_HappyPath's calls slice pattern already asserts for
// the other five activities, extended here to include this one.
func TestReleaseWorkflow_RecordResolvedPlan_DispatchedWhenPlanHasRawJSON(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-4", Targets: []ReleaseTarget{testTarget()}}
	rawJSON := []byte(`{"targets":[{"owner":"demo-widget","version":"v1.0.1"}]}`)
	plan := ResolvedPlan{ReleaseRunID: "run-4", Versions: map[string]string{testTarget().key(): "v1.0.1"}, RawJSON: rawJSON}
	ref := BuildRef{ReleaseRunID: "run-4", RunID: "44"}

	var calls []string
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-4").Return(true, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityCheckApproval) })
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityResolvePlan) })
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-4", rawJSON).Return(nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityRecordResolvedPlan) })
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityDispatchBuild) })
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityPollBuild) })
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-4").Return(VerifyResult{AllPublished: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-4", testTarget(), repository.ReleaseRunTargetStateSucceeded, "44", "").
		Return(nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityRecordTargetState) })

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Equal(t, []string{
		ActivityCheckApproval, ActivityResolvePlan, ActivityRecordResolvedPlan, ActivityDispatchBuild,
		ActivityPollBuild, ActivityVerifyPublished, ActivityRecordTargetState,
	}, calls, "RecordResolvedPlan must run right after ResolvePlan and before DispatchBuild (issue #906)")
}

// TestReleaseWorkflow_RecordResolvedPlan_Failure_MarksTargetsFailed proves
// a RecordResolvedPlan failure is treated the same as any other pre-
// RecordTargetState step failure (issue #889's Testing scope, extended to
// this new step by #906): DispatchBuild/PollBuild/VerifyPublished must
// never run, and every target is recorded failed via the recordFailure
// branch.
func TestReleaseWorkflow_RecordResolvedPlan_Failure_MarksTargetsFailed(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-5", Targets: []ReleaseTarget{testTarget()}}
	rawJSON := []byte(`{"targets":[{"owner":"demo-widget","version":"v1.0.1"}]}`)
	plan := ResolvedPlan{ReleaseRunID: "run-5", Versions: map[string]string{testTarget().key(): "v1.0.1"}, RawJSON: rawJSON}

	var calledDownstream []string
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-5").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-5", rawJSON).Return(errors.New("db unavailable")).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, mock.Anything, mock.Anything).Return(BuildRef{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledDownstream = append(calledDownstream, ActivityDispatchBuild) })
	env.OnActivity(ActivityPollBuild, mock.Anything, mock.Anything).Return(BuildStatus{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledDownstream = append(calledDownstream, ActivityPollBuild) })
	env.OnActivity(ActivityVerifyPublished, mock.Anything, mock.Anything).Return(VerifyResult{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledDownstream = append(calledDownstream, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-5", testTarget(), repository.ReleaseRunTargetStateFailed, "", mock.AnythingOfType("string")).
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Empty(t, calledDownstream, "DispatchBuild/PollBuild/VerifyPublished must not run once RecordResolvedPlan has failed")
}
