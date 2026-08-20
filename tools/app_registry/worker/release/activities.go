package release

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// Activities is the concrete ReleaseActivities implementation
// worker/main.go registers. CheckApproval (approval.go) is real as of this
// package's introduction -- FR14's true no-op. Every other method here
// returns an unimplemented error: issue #889's own phase breakdown defers
// their real logic (GitHub Actions dispatch/poll, plan resolution,
// GetReleaseRun verification, release_run_target writes) to that issue's
// Implementation scope. See the package doc comment's "Scaffold status".
//
// Fields are added here as each activity gains a real implementation (e.g.
// a GitHub App client for DispatchBuild/PollBuild, mirroring
// worker/writeback/gitops.go's GitOpsConfig; an AppRegistry gRPC client for
// VerifyPublished/RecordTargetState, mirroring worker/writeback/stub.go's
// Client field).
type Activities struct{}

var _ ReleaseActivities = (*Activities)(nil)

// ResolvePlan is unimplemented at scaffold time. See ReleaseActivities'
// doc comment and issue #889's Implementation scope: the real
// implementation calls tools/release_helper_go/cmd/plan.go's/plan_helm.go's
// resolution logic (extracted to a library function, or shelled out to the
// existing release_helper_go plan binary as an interim step) exactly once
// per workflow execution.
func (a *Activities) ResolvePlan(ctx context.Context, targets []ReleaseTarget) (ResolvedPlan, error) {
	return ResolvedPlan{}, unimplemented(ActivityResolvePlan)
}

// DispatchBuild is unimplemented at scaffold time. See ReleaseActivities'
// doc comment and issue #889's Implementation scope: the real
// implementation invokes release.yml (or a dedicated workflow file) via the
// GitHub Actions API, reusing worker/writeback/gitops.go's GitHub App
// installation-token pattern. A non-empty digest override (FR2) must
// return a clear unimplemented-style error rather than silently building
// fresh or silently ignoring it -- this stub already satisfies that for
// every input, real or not.
func (a *Activities) DispatchBuild(ctx context.Context, plan ResolvedPlan, digestOverrides map[string]string) (BuildRef, error) {
	return BuildRef{}, unimplemented(ActivityDispatchBuild)
}

// PollBuild is unimplemented at scaffold time. See ReleaseActivities' doc
// comment and issue #889's Implementation scope: the real implementation
// polls the GitHub Actions run identified by ref until it reaches success,
// failure, or cancelled.
func (a *Activities) PollBuild(ctx context.Context, ref BuildRef) (BuildStatus, error) {
	return BuildStatus{}, unimplemented(ActivityPollBuild)
}

// VerifyPublished is unimplemented at scaffold time. See ReleaseActivities'
// doc comment and issue #889's Implementation scope: the real
// implementation calls GetRelease/GetReleaseRun (or ListArtifacts) and
// confirms every target reached published/succeeded.
func (a *Activities) VerifyPublished(ctx context.Context, releaseRunID string) (VerifyResult, error) {
	return VerifyResult{}, unimplemented(ActivityVerifyPublished)
}

// RecordTargetState is unimplemented at scaffold time. See
// ReleaseActivities' doc comment and issue #889's Implementation scope: the
// real implementation resolves target's release_run_target row (via
// GetReleaseRun on releaseRunID, matching OwnerFullName+Kind) and calls
// repository.ReleaseRunRepository.UpdateTargetState on it.
func (a *Activities) RecordTargetState(ctx context.Context, releaseRunID string, target ReleaseTarget, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	return unimplemented(ActivityRecordTargetState)
}

// unimplemented builds the scaffold-time error every not-yet-implemented
// ReleaseActivities method returns. Not wrapped in temporal.NewApplicationError
// as non-retryable: Temporal's default retry policy retrying an
// unimplemented activity is harmless (it will keep failing until the real
// implementation lands) and keeping it a plain error avoids importing
// go.temporal.io/sdk/temporal here just for that one call.
func unimplemented(activityName string) error {
	return fmt.Errorf("release.%s: not implemented yet (scaffold only -- see issue #889's Implementation scope)", activityName)
}
