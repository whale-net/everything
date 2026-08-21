package release

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// Activities is the concrete ReleaseActivities implementation
// worker/main.go registers. CheckApproval (approval.go) is real -- FR14's
// true no-op. ResolvePlan (plan.go) and VerifyPublished/RecordTargetState
// (record.go) are real. DispatchBuild/PollBuild (below) delegate to
// GitHub, real once GitHub is configured (see NewGitHubDispatcher);
// worker/main.go leaves GitHub nil (and DispatchBuild/PollBuild return an
// explicit "not configured" error) unless the required GitHub App env vars
// are set, mirroring worker/writeback's stub/gitops selection.
type Activities struct {
	// Registry is the direct Postgres-backed repository.Registry
	// VerifyPublished/RecordTargetState read/write through -- see record.go's
	// package doc comment for why this bypasses the gRPC API (which has no
	// mutating RPC for release_run_target). Required for those two methods.
	Registry repository.Registry

	// GitHub dispatches/polls the GitHub Actions build job. Required for
	// DispatchBuild/PollBuild.
	GitHub *GitHubDispatcher

	// PlanBinaryPath/WorkspaceRoot configure ResolvePlan's interim
	// release_helper_go shell-out -- see plan.go's package doc comment.
	PlanBinaryPath string
	WorkspaceRoot  string
}

var _ ReleaseActivities = (*Activities)(nil)

// DispatchBuild implements ReleaseActivities.DispatchBuild: invokes
// release.yml (or whatever workflow file a.GitHub.Config.WorkflowFile
// names) via the GitHub Actions API with plan's resolved version, skipping
// that workflow's own plan-release re-resolution for this trigger (its
// explicit `version` input takes precedence over auto-increment -- see
// tools/release_helper_go/cmd/plan.go's buildPlanResult) -- see this
// package's github.go for the dispatch mechanics and its documented
// version-uniformity limitation.
//
// digestOverrides (FR2, hotfix/re-release) is out of scope for this task's
// implementation per issue #889's Implementation scope: a non-empty
// digestOverrides returns a clear error rather than silently building
// fresh or silently ignoring it.
//
// Idempotency (NFR3/FR11): DispatchBuild does not itself check-before-
// dispatch against an in-flight build for this release run before calling
// GitHub -- an accepted, narrow gap at this phase: workflow.go's
// defaultActivityOptions gives DispatchBuild MaximumAttempts: 5, so a
// retried DispatchBuild (e.g. after a transient network error on the
// dispatch POST itself, or on findDispatchedRun's polling) could in
// principle trigger a second GHA run for the same release. This is
// mitigated, not eliminated, by two things already true at this phase: (1)
// VerifyPublished treats already-published targets as satisfied regardless
// of which of two concurrent runs published them first (see that method's
// doc comment -- FR11's "retry after partial completion must not double-
// publish" is enforced there, downstream, not by preventing a second
// dispatch), and (2) the underlying GHA job's own BeginPublish/
// RecordArtifact calls are already idempotent by digest/version
// (architecture/08-release-lifecycle/04-run-log.md, unchanged by this
// task) -- a second concurrent run publishing the same version is a no-op
// or a detected conflict there, not a corrupted publish. A tighter
// check-before-dispatch (e.g. recording BuildRef on the release_run row
// before calling GitHub, and having a retried DispatchBuild read it back
// first) is documented follow-up, not implemented here.
//
// TODO(#923, Implementation): FR9-FR11 -- resolve app_build_log's current
// row for this batch's targets (buildref.go's resolveBuildRef, using
// a.Registry) and thread the result into a.GitHub.Dispatch's `ref`
// (GitHubDispatcherConfig.Ref is currently a static field defaulting to
// "main" -- see github.go). Not wired in during Scaffold (issue #923);
// deferred to Implementation pending the per-Dispatch()-call-parameter-
// vs-static-field design decision FR11 calls out. This only applies to
// release-v2.yml dispatches (FR11 note: release.yml's v1 dispatch path is
// explicitly out of scope for this task).
func (a *Activities) DispatchBuild(ctx context.Context, plan ResolvedPlan, digestOverrides map[string]string) (BuildRef, error) {
	if len(digestOverrides) > 0 {
		return BuildRef{}, fmt.Errorf("dispatch build: digest overrides (FR2 hotfix/re-release input) not implemented yet -- see issue #889's Implementation scope")
	}
	if a.GitHub == nil {
		return BuildRef{}, fmt.Errorf("dispatch build: GitHub dispatcher not configured")
	}
	if len(plan.Versions) == 0 {
		return BuildRef{}, fmt.Errorf("dispatch build: resolved plan has no versions")
	}

	version, err := uniformVersion(plan.Versions)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
	}
	apps, charts, err := splitPlanTargets(plan.Versions)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
	}

	inputs := map[string]string{
		"version": version,
		"dry_run": "false",
	}
	if len(apps) > 0 {
		inputs["apps"] = joinComma(apps)
	}
	if len(charts) > 0 {
		inputs["helm_charts"] = joinComma(charts)
	}
	appVersions, err := appVersionsJSON(plan.Versions)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
	}
	if appVersions != "" {
		inputs["app_versions"] = appVersions
	}

	ref, err := a.GitHub.Dispatch(ctx, plan.ReleaseRunID, inputs)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
	}
	return ref, nil
}

// PollBuild implements ReleaseActivities.PollBuild: polls the GitHub
// Actions run identified by ref until it reaches a terminal state. Read-
// only, naturally idempotent under activity retry (NFR3) -- re-polling the
// same run id produces the same answer.
func (a *Activities) PollBuild(ctx context.Context, ref BuildRef) (BuildStatus, error) {
	if a.GitHub == nil {
		return BuildStatus{}, fmt.Errorf("poll build: GitHub dispatcher not configured")
	}
	return a.GitHub.PollRun(ctx, ref)
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
