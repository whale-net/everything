package release

import (
	"context"
	"errors"
	"testing"
	"time"

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
	registerActivityStubsWithPoll(env, func(ctx context.Context, ref BuildRef) (BuildStatus, error) {
		return BuildStatus{}, nil
	})
}

// registerActivityStubsWithPoll is registerActivityStubs with the PollBuild
// stub swapped for pollFn -- the notify-vs-poll tests need a poll that
// BLOCKS (mimicking a long-running Actions run) so the build-completed
// signal can win the race, which OnActivity mocks cannot express (a mock
// Return fires immediately, deciding the race before the signal can).
// Blocking on ctx.Done() inside pollFn is what makes the signal path
// observable: the workflow's cancelPoll (see awaitBuildCompletion) cancels
// the activity context, the stub returns ctx.Err(), and the testsuite's
// cancellation delivery finishes the activity exactly like a real one.
func registerActivityStubsWithPoll(env *testsuite.TestWorkflowEnvironment, pollFn func(ctx context.Context, ref BuildRef) (BuildStatus, error)) {
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
	env.RegisterActivityWithOptions(pollFn, activity.RegisterOptions{Name: ActivityPollBuild})
	env.RegisterActivityWithOptions(func(ctx context.Context, plan ResolvedPlan, ref BuildRef) (FinalizeResult, error) {
		return FinalizeResult{Succeeded: true}, nil
	}, activity.RegisterOptions{Name: ActivityFinalizePublish})
	env.RegisterActivityWithOptions(func(ctx context.Context, releaseRunID string, expectedVersions map[string]string) (VerifyResult, error) {
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
	// RawJSON's build_id is the `build` table UUID RecordTargetState must
	// write to release_run_target.build_id -- NOT ref.RunID (GitHub's
	// numeric Actions run id, which release_run_target.build_id rejects as
	// an invalid UUID -- see workflow.go's planBuildID call site).
	rawJSON := []byte(`{"build_id":"11111111-1111-1111-1111-111111111111","version":"v1.0.1"}`)
	plan := ResolvedPlan{ReleaseRunID: "run-1", Versions: map[string]string{testTarget().key(): "v1.0.1"}, RawJSON: rawJSON}
	ref := BuildRef{ReleaseRunID: "run-1", RunID: "42"}

	var calls []string
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-1").Return(true, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityCheckApproval) })
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityResolvePlan) })
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-1", rawJSON).Return(nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityRecordResolvedPlan) })
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityDispatchBuild) })
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityPollBuild) })
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(FinalizeResult{Succeeded: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityFinalizePublish) })
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-1", mock.Anything).Return(VerifyResult{AllPublished: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-1", testTarget(), repository.ReleaseRunTargetStateSucceeded, "11111111-1111-1111-1111-111111111111", "").
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
		ActivityCheckApproval, ActivityResolvePlan, ActivityRecordResolvedPlan, ActivityDispatchBuild,
		ActivityPollBuild, ActivityFinalizePublish, ActivityVerifyPublished, ActivityRecordTargetState,
	}, calls, "ReleaseWorkflow must dispatch activities in exactly this order (FR6, extended by issue #928's FinalizePublish)")
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
	env.OnActivity(ActivityVerifyPublished, mock.Anything, mock.Anything, mock.Anything).Return(VerifyResult{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledPollOrVerify = append(calledPollOrVerify, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-2", testTarget(), repository.ReleaseRunTargetStateFailed, "", mock.AnythingOfType("string")).
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Empty(t, calledPollOrVerify, "PollBuild/VerifyPublished must not run once DispatchBuild has failed")
}

// TestReleaseWorkflow_FinalizePublishFailure_MarksTargetsFailed proves
// issue #928's FinalizePublish is treated the same as any other pre-
// VerifyPublished step failure: a hard FinalizePublish activity error (as
// opposed to a FinalizeResult{Succeeded: false} -- see that type's doc
// comment) marks every target failed and VerifyPublished never runs.
func TestReleaseWorkflow_FinalizePublishFailure_MarksTargetsFailed(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-6", Targets: []ReleaseTarget{testTarget()}}
	plan := ResolvedPlan{ReleaseRunID: "run-6", Versions: map[string]string{testTarget().key(): "v1.0.1"}}
	ref := BuildRef{ReleaseRunID: "run-6", RunID: "46"}

	var calledVerify bool
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-6").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(FinalizeResult{}, errors.New("github artifacts unreachable")).Once()
	env.OnActivity(ActivityVerifyPublished, mock.Anything, mock.Anything, mock.Anything).Return(VerifyResult{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledVerify = true })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-6", testTarget(), repository.ReleaseRunTargetStateFailed, "", mock.AnythingOfType("string")).
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.False(t, calledVerify, "VerifyPublished must not run once FinalizePublish has hard-failed")
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
	rawJSON := []byte(`{"build_id":"33333333-3333-3333-3333-333333333333","version":"v1.0.1"}`)
	plan := ResolvedPlan{ReleaseRunID: "run-3", Versions: map[string]string{ok.key(): "v1.0.1", bad.key(): "v1.0.1"}, RawJSON: rawJSON}
	ref := BuildRef{ReleaseRunID: "run-3", RunID: "43"}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-3").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-3", rawJSON).Return(nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(FinalizeResult{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-3", mock.Anything).Return(VerifyResult{
		AllPublished: false,
		Failed:       map[string]string{bad.key(): "no published artifact found"},
	}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-3", ok, repository.ReleaseRunTargetStateSucceeded, "33333333-3333-3333-3333-333333333333", "").Return(nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-3", bad, repository.ReleaseRunTargetStateFailed, "33333333-3333-3333-3333-333333333333", "no published artifact found").Return(nil).Once()

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
	rawJSON := []byte(`{"build_id":"44444444-4444-4444-4444-444444444444","targets":[{"owner":"demo-widget","version":"v1.0.1"}]}`)
	plan := ResolvedPlan{ReleaseRunID: "run-4", Versions: map[string]string{testTarget().key(): "v1.0.1"}, RawJSON: rawJSON}
	ref := BuildRef{ReleaseRunID: "run-4", RunID: "44"}

	var calls []string
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-4").Return(true, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityCheckApproval) })
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityResolvePlan) })
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-4", rawJSON).Return(nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityRecordResolvedPlan) })
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityDispatchBuild) })
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityPollBuild) })
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(FinalizeResult{Succeeded: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityFinalizePublish) })
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-4", mock.Anything).Return(VerifyResult{AllPublished: true}, nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-4", testTarget(), repository.ReleaseRunTargetStateSucceeded, "44444444-4444-4444-4444-444444444444", "").
		Return(nil).Once().Run(func(args mock.Arguments) { calls = append(calls, ActivityRecordTargetState) })

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	require.Equal(t, []string{
		ActivityCheckApproval, ActivityResolvePlan, ActivityRecordResolvedPlan, ActivityDispatchBuild,
		ActivityPollBuild, ActivityFinalizePublish, ActivityVerifyPublished, ActivityRecordTargetState,
	}, calls, "RecordResolvedPlan must run right after ResolvePlan and before DispatchBuild (issue #906); FinalizePublish must run right after PollBuild succeeds and before VerifyPublished (issue #928)")
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
	env.OnActivity(ActivityVerifyPublished, mock.Anything, mock.Anything, mock.Anything).Return(VerifyResult{}, nil).Maybe().
		Run(func(args mock.Arguments) { calledDownstream = append(calledDownstream, ActivityVerifyPublished) })
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-5", testTarget(), repository.ReleaseRunTargetStateFailed, "", mock.AnythingOfType("string")).
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	require.Empty(t, calledDownstream, "DispatchBuild/PollBuild/VerifyPublished must not run once RecordResolvedPlan has failed")
}

// TestReleaseWorkflow_FinalizeTargetFailure_RoutesDirectlyToFailed is the
// issue #973 proper-fix regression test: a target FinalizePublish itself
// reports Failed in FinalizeResult.Targets (e.g. a GHCR retag DENIED --
// the original bug report) must be recorded Failed with that exact detail,
// even when VerifyPublished's mock is set up to report the target
// published/satisfied. This proves the fix routes a real finalize failure
// directly from FinalizeResult.Targets, not through VerifyPublished's
// indirect presence/version inference -- PR #976's first-pass fix depended
// entirely on VerifyPublished catching this, which this test would not be
// able to distinguish from a false negative if the workflow still consulted
// VerifyPublished for this target.
func TestReleaseWorkflow_FinalizeTargetFailure_RoutesDirectlyToFailed(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-7", Targets: []ReleaseTarget{testTarget()}}
	plan := ResolvedPlan{ReleaseRunID: "run-7", Versions: map[string]string{testTarget().key(): "v1.2.3"}}
	ref := BuildRef{ReleaseRunID: "run-7", RunID: "47"}

	finalizeResult := FinalizeResult{
		Succeeded: false,
		Detail:    "demo-widget: finalize-app: DENIED: permission_denied",
		Targets: map[string]FinalizeTargetOutcome{
			testTarget().key(): {Failed: true, Detail: "finalize-app: DENIED: permission_denied"},
		},
	}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-7").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(finalizeResult, nil).Once()
	// VerifyPublished deliberately reports this target as published/
	// satisfied -- an older artifact from a prior release could easily
	// still be sitting there Published. If the workflow still consulted
	// this result for the target instead of routing FinalizeResult.Targets
	// directly, this test would incorrectly observe Succeeded.
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-7", mock.Anything).Return(VerifyResult{AllPublished: true}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-7", testTarget(), repository.ReleaseRunTargetStateFailed, mock.Anything, "finalize-app: DENIED: permission_denied").
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Len(t, got.Targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateFailed, got.Targets[0].State)
	require.Equal(t, "finalize-app: DENIED: permission_denied", got.Targets[0].ErrorDetail)
}

// TestReleaseWorkflow_FinalizeNoOpRebuild_RecordsSucceeded is the inverse
// regression test the user specifically asked for: a target whose
// FinalizePublish reports success (Failed: false) with an older
// EffectiveVersion than the plan-time requested version -- simulating
// ExecuteRelease's legitimate no-op-rebuild path (identical digest reuses
// an already-published version instead of the plan-time one) -- must still
// be recorded Succeeded, not Failed. PR #976's first-pass fix (comparing
// against release_run.resolved_plan's plan-time version) would have
// misreported this exact case as a version-mismatch failure; this proves
// the proper fix (comparing against FinalizePublish's own reported
// EffectiveVersion, via expectedVersions) does not have that false
// positive.
func TestReleaseWorkflow_FinalizeNoOpRebuild_RecordsSucceeded(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-8", Targets: []ReleaseTarget{testTarget()}}
	// Plan-time requested version is v1.2.3, but the no-op rebuild reused
	// the already-published v1.0.0 instead -- see FinalizeTargetOutcome's
	// doc comment.
	plan := ResolvedPlan{ReleaseRunID: "run-8", Versions: map[string]string{testTarget().key(): "v1.2.3"}}
	ref := BuildRef{ReleaseRunID: "run-8", RunID: "48"}

	finalizeResult := FinalizeResult{
		Succeeded: true,
		Targets: map[string]FinalizeTargetOutcome{
			testTarget().key(): {EffectiveVersion: "v1.0.0"},
		},
	}
	expectedVersions := map[string]string{testTarget().key(): "v1.0.0"}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-8").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(finalizeResult, nil).Once()
	// Asserts the workflow passes FinalizePublish's own EffectiveVersion
	// (v1.0.0), not plan.Versions' plan-time v1.2.3, as expectedVersions --
	// exactly what lets VerifyPublished's real implementation (record.go)
	// compare correctly against the artifact actually published.
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-8", expectedVersions).Return(VerifyResult{AllPublished: true}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-8", testTarget(), repository.ReleaseRunTargetStateSucceeded, mock.Anything, "").
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Len(t, got.Targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, got.Targets[0].State,
		"a no-op rebuild reusing an older EffectiveVersion must not be misreported as a failure")
}

// TestReleaseWorkflow_FinalizeTargetNoEntry_FallsThroughToVerifyPublished is
// this PR's Finding 1 regression test at the workflow level: a target with
// NO entry at all in FinalizeResult.Targets (e.g. finalize.go/finalize.go's
// CLI succeeded but the bookkeeping --output-dir result file could not be
// read back -- see finalize_test.go's
// TestActivities_FinalizePublish_MissingFinalizeAppResult_NoTargetEntry)
// must still be checked via VerifyPublished's real presence/version
// signal, not auto-succeeded or auto-failed by the workflow itself. This
// test proves the "falls through" half specifically: expectedVersions is
// the empty map for this target (no EffectiveVersion to compare, since
// FinalizePublish reported nothing), and the workflow trusts whatever
// VerifyPublished's mock returns for it -- here, deliberately Failed, so a
// workflow bug that silently treated "no entry" as an automatic success
// would be caught (it would observe Succeeded instead).
func TestReleaseWorkflow_FinalizeTargetNoEntry_FallsThroughToVerifyPublished(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-9", Targets: []ReleaseTarget{testTarget()}}
	plan := ResolvedPlan{ReleaseRunID: "run-9", Versions: map[string]string{testTarget().key(): "v1.2.3"}}
	ref := BuildRef{ReleaseRunID: "run-9", RunID: "49"}

	// No entry for testTarget().key() in Targets at all -- neither Failed
	// nor a reported EffectiveVersion.
	finalizeResult := FinalizeResult{Succeeded: true, Targets: map[string]FinalizeTargetOutcome{}}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-9").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(finalizeResult, nil).Once()
	// expectedVersions must be the empty map here -- there is no
	// EffectiveVersion for a target with no FinalizeResult.Targets entry.
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-9", map[string]string{}).
		Return(VerifyResult{AllPublished: false, Failed: map[string]string{testTarget().key(): "no published artifact found"}}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-9", testTarget(), repository.ReleaseRunTargetStateFailed, mock.Anything, "no published artifact found").
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Len(t, got.Targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateFailed, got.Targets[0].State,
		"a target with no FinalizeResult.Targets entry must be decided by VerifyPublished's real result, not auto-succeeded")
	require.Equal(t, "no published artifact found", got.Targets[0].ErrorDetail)
}

// blockingPoll returns a PollBuild stub that mimics a long-running Actions
// build: it never reports a terminal state on its own -- it only returns
// when the activity context is cancelled (exactly what
// awaitBuildCompletion's signal path does via cancelPoll). Blocking on
// ctx.Done() inside pollFn is what makes the signal path observable: the
// workflow's cancellation delivers to the activity context the same way it
// would to a real activity, and without it the tests below would hang
// (proving the poll alone cannot decide while the signal hasn't arrived).
func blockingPoll(pollStarted chan<- struct{}) func(ctx context.Context, ref BuildRef) (BuildStatus, error) {
	return func(ctx context.Context, ref BuildRef) (BuildStatus, error) {
		close(pollStarted)
		<-ctx.Done()
		return BuildStatus{}, ctx.Err()
	}
}

// TestReleaseWorkflow_SignalBeatsPoll proves the notify half of the
// notify-vs-poll split: a build-completed signal (NotifyBuildComplete,
// delivered while PollBuild is still polling a "long-running" run) decides
// the build outcome immediately -- the poll activity is cancelled and the
// workflow proceeds to finalize/verify/record exactly as a poll-success
// would have. This is the race's winning path; the fallback (no signal,
// poll decides) is already covered by every other test in this file.
func TestReleaseWorkflow_SignalBeatsPoll(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	pollStarted := make(chan struct{})
	registerActivityStubsWithPoll(env, blockingPoll(pollStarted))

	in := ReleaseWorkflowInput{ReleaseRunID: "run-10", Targets: []ReleaseTarget{testTarget()}}
	rawJSON := []byte(`{"build_id":"10101010-1010-1010-1010-101010101010","version":"v1.0.1"}`)
	plan := ResolvedPlan{ReleaseRunID: "run-10", Versions: map[string]string{testTarget().key(): "v1.0.1"}, RawJSON: rawJSON}
	ref := BuildRef{ReleaseRunID: "run-10", RunID: "410"}

	// PollBuild is NOT given an OnActivity expectation: its registered
	// stub blocks forever (until the signal path cancels it), which is the
	// whole point -- if the workflow still waited for the poll instead of
	// honoring the signal, this test would time out rather than pass.
	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-10").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-10", rawJSON).Return(nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(FinalizeResult{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-10", mock.Anything).Return(VerifyResult{AllPublished: true}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-10", testTarget(), repository.ReleaseRunTargetStateSucceeded, "10101010-1010-1010-1010-101010101010", "").
		Return(nil).Once()

	// Deliver the notification once the workflow is parked in
	// awaitBuildCompletion (poll started, nothing else to do).
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalBuildCompleted, BuildCompletedSignal{
			GitHubRunID: "410",
			Succeeded:   true,
			Detail:      "run 410 conclusion=success",
		})
	}, time.Millisecond)

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Len(t, got.Targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, got.Targets[0].State,
		"the signal must decide the build outcome (and the poll must not have needed to finish)")
}

// TestReleaseWorkflow_SignalFailure_RecordsFailed proves the signal path
// carries failure exactly like PollBuild's Succeeded=false does: the
// workflow routes to recordFailure with the signal's detail.
func TestReleaseWorkflow_SignalFailure_RecordsFailed(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	pollStarted := make(chan struct{})
	registerActivityStubsWithPoll(env, blockingPoll(pollStarted))

	in := ReleaseWorkflowInput{ReleaseRunID: "run-11", Targets: []ReleaseTarget{testTarget()}}
	plan := ResolvedPlan{ReleaseRunID: "run-11", Versions: map[string]string{testTarget().key(): "v1.0.1"}}
	ref := BuildRef{ReleaseRunID: "run-11", RunID: "411"}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-11").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, mock.Anything, mock.Anything).Return(FinalizeResult{}, nil).Maybe()
	env.OnActivity(ActivityVerifyPublished, mock.Anything, mock.Anything, mock.Anything).Return(VerifyResult{}, nil).Maybe()
	// recordFailure reports the wrapped cause, so the recorded detail is
	// "build did not succeed: <signal detail>" -- not the bare signal line.
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-11", testTarget(), repository.ReleaseRunTargetStateFailed, "", "build did not succeed: run 411 conclusion=failure").
		Return(nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalBuildCompleted, BuildCompletedSignal{
			GitHubRunID: "411",
			Succeeded:   false,
			Detail:      "run 411 conclusion=failure",
		})
	}, time.Millisecond)

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError(), "a not-succeeded build must fail the workflow exactly as PollBuild's failure branch does")

	// GetWorkflowResult is deliberately not asserted here: recordFailure
	// returns the cause as the workflow error, which GetWorkflowResult
	// surfaces identically -- the per-target outcome is pinned by the
	// RecordTargetState expectation above.
}

// TestReleaseWorkflow_StaleSignalIgnored proves the stale-signal guard: a
// build-completed signal whose GitHubRunID doesn't match the dispatched
// run's BuildRef.RunID (a notification from some OTHER Actions run -- e.g.
// a retry after the first dispatch died) is ignored, and the workflow keeps
// waiting until either a matching signal or the poll decides. Here a
// matching signal arrives after the stale one and must be the decider.
func TestReleaseWorkflow_StaleSignalIgnored(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()

	pollStarted := make(chan struct{})
	registerActivityStubsWithPoll(env, blockingPoll(pollStarted))

	in := ReleaseWorkflowInput{ReleaseRunID: "run-12", Targets: []ReleaseTarget{testTarget()}}
	rawJSON := []byte(`{"build_id":"12121212-1212-1212-1212-121212121212","version":"v1.0.1"}`)
	plan := ResolvedPlan{ReleaseRunID: "run-12", Versions: map[string]string{testTarget().key(): "v1.0.1"}, RawJSON: rawJSON}
	ref := BuildRef{ReleaseRunID: "run-12", RunID: "412"}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-12").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-12", rawJSON).Return(nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(FinalizeResult{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-12", mock.Anything).Return(VerifyResult{AllPublished: true}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-12", testTarget(), repository.ReleaseRunTargetStateSucceeded, "12121212-1212-1212-1212-121212121212", "").
		Return(nil).Once()

	// A stale signal first (wrong run id), then the matching one. The
	// stale one must be skipped, not decide.
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalBuildCompleted, BuildCompletedSignal{
			GitHubRunID: "999999",
			Succeeded:   false,
			Detail:      "run 999999 conclusion=failure (stale)",
		})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(SignalBuildCompleted, BuildCompletedSignal{
			GitHubRunID: "412",
			Succeeded:   true,
			Detail:      "run 412 conclusion=success",
		})
	}, 2*time.Millisecond)

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Len(t, got.Targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, got.Targets[0].State,
		"a stale signal from a different run must be ignored; the matching signal must decide")
}

// TestReleaseWorkflow_SignalArrivesLate_PollDecides proves the poll half of
// the split stays authoritative when no notification ever arrives: with a
// poll that finishes normally (and no signal at all), the workflow follows
// the poll result -- the pre-notify behavior, unchanged. (A signal that
// arrives only AFTER the poll decided is equivalent to no signal for the
// workflow: an undelivered signal is a no-op in Temporal.)
func TestReleaseWorkflow_SignalArrivesLate_PollDecides(t *testing.T) {
	ts := testsuite.WorkflowTestSuite{}
	env := ts.NewTestWorkflowEnvironment()
	registerActivityStubs(env)

	in := ReleaseWorkflowInput{ReleaseRunID: "run-13", Targets: []ReleaseTarget{testTarget()}}
	rawJSON := []byte(`{"build_id":"13131313-1313-1313-1313-131313131313","version":"v1.0.1"}`)
	plan := ResolvedPlan{ReleaseRunID: "run-13", Versions: map[string]string{testTarget().key(): "v1.0.1"}, RawJSON: rawJSON}
	ref := BuildRef{ReleaseRunID: "run-13", RunID: "413"}

	env.OnActivity(ActivityCheckApproval, mock.Anything, "run-13").Return(true, nil).Once()
	env.OnActivity(ActivityResolvePlan, mock.Anything, in.Targets).Return(plan, nil).Once()
	env.OnActivity(ActivityRecordResolvedPlan, mock.Anything, "run-13", rawJSON).Return(nil).Once()
	env.OnActivity(ActivityDispatchBuild, mock.Anything, plan, map[string]string{}).Return(ref, nil).Once()
	env.OnActivity(ActivityPollBuild, mock.Anything, ref).Return(BuildStatus{Succeeded: true, Detail: "run 413 conclusion=success"}, nil).Once()
	env.OnActivity(ActivityFinalizePublish, mock.Anything, plan, ref).Return(FinalizeResult{Succeeded: true}, nil).Once()
	env.OnActivity(ActivityVerifyPublished, mock.Anything, "run-13", mock.Anything).Return(VerifyResult{AllPublished: true}, nil).Once()
	env.OnActivity(ActivityRecordTargetState, mock.Anything, "run-13", testTarget(), repository.ReleaseRunTargetStateSucceeded, "13131313-1313-1313-1313-131313131313", "").
		Return(nil).Once()

	env.ExecuteWorkflow(ReleaseWorkflow, in)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())

	var got ReleaseWorkflowResult
	require.NoError(t, env.GetWorkflowResult(&got))
	require.Len(t, got.Targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, got.Targets[0].State)
}
