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
	// mutating RPC for release_run_target). Required for those two methods,
	// and also for DispatchBuild's FR9-FR11 app_build_log ref resolution
	// (buildref.go's resolveDispatchRef) -- the same direct-Postgres
	// rationale applies: AppBuildLogs() has no read RPC either.
	Registry repository.Registry

	// GitHub dispatches/polls the GitHub Actions build job. Required for
	// DispatchBuild/PollBuild.
	GitHub *GitHubDispatcher

	// PlanBinaryPath/WorkspaceRoot configure ResolvePlan's interim
	// release_helper_go shell-out -- see plan.go's package doc comment.
	// FinalizePublish (issue #928, finalize.go) reuses both for its own
	// release_helper_go finalize-app/finalize-chart shell-outs.
	PlanBinaryPath string
	WorkspaceRoot  string

	// ChartRepoURL/ChartRepoUser/ChartRepoPass are ChartMuseum credentials
	// for FinalizePublish's finalize-chart shell-out (issue #928).
	// Required for FinalizePublish when the batch has any chart targets;
	// this is the credential-locality move issue #928 asked for --
	// ChartMuseum write access now lives here, on the Temporal worker,
	// rather than in release-v2.yml's merged build job (which no longer
	// uploads to ChartMuseum at all -- see finalize.go's package doc
	// comment). GHCR retag auth needs no separate credential: finalize.go
	// reuses GitHub's own installation token (see GitHubDispatcher.token)
	// for that.
	ChartRepoURL  string
	ChartRepoUser string
	ChartRepoPass string

	// cloneWorkspaceFn overrides FinalizePublish's real git-clone-over-HTTPS
	// step (cloneWorkspace in finalize.go) in tests -- unexported, same
	// package only, mirroring GitHubDispatcher.Now's test-injection pattern.
	// nil (the production default) means "use the real implementation."
	cloneWorkspaceFn func(ctx context.Context, dir string) error
}

var _ ReleaseActivities = (*Activities)(nil)

// DispatchBuild implements ReleaseActivities.DispatchBuild: invokes
// release-v2.yml (or whatever workflow file a.GitHub.Config.WorkflowFile
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
// FR9-FR11 (issue #923): resolves app_build_log's current row for this
// batch's targets (buildref.go's resolveDispatchRef/resolveBuildRef,
// using a.Registry) and forwards the result as a `build_ref` workflow
// input, falling back to the still-required GitHubDispatcherConfig.Ref
// (default "main") whenever no target has a current app_build_log row
// (FR10). GitHub's workflow_dispatch API's own `ref` parameter is always
// dispatched as the plain branch/tag (GitHubDispatcherConfig.Ref) -- it
// cannot carry a commit SHA (422 "No ref found for: <sha>", confirmed
// against a SHA that genuinely was the branch tip); release-v2.yml checks
// out `build_ref` explicitly instead (see that workflow's `Checkout code`
// steps). This only applies to whatever workflow
// a.GitHub.Config.WorkflowFile is configured to dispatch (in practice
// release-v2.yml for this worker's deployment -- FR11 note: release.yml's
// v1 dispatch path/trigger is untouched by this task and is not driven
// through this worker).
//
// Issue #927: when plan.RawJSON is populated (the normal case -- see
// plan.go's ResolvePlan), DispatchBuild forwards it verbatim as a single
// `resolved_plan` input rather than collapsing it into the flat
// apps/version/app_versions inputs release-v2.yml's plan-release job used
// to re-expand by re-invoking `release-helper plan` from scratch. See the
// body below and release-v2.yml's plan-release job for the
// parse-and-passthrough / fallback split.
func (a *Activities) DispatchBuild(ctx context.Context, plan ResolvedPlan, digestOverrides map[string]string) (BuildRef, error) {
	if len(digestOverrides) > 0 {
		return BuildRef{}, fmt.Errorf("dispatch build: digest overrides (FR2 hotfix/re-release input) not implemented yet -- see issue #889's Implementation scope")
	}
	if a.GitHub == nil {
		return BuildRef{}, fmt.Errorf("dispatch build: GitHub dispatcher not configured")
	}
	if a.Registry == nil {
		return BuildRef{}, fmt.Errorf("dispatch build: registry not configured (required for FR9-FR11 app_build_log ref resolution)")
	}
	if len(plan.Versions) == 0 {
		return BuildRef{}, fmt.Errorf("dispatch build: resolved plan has no versions")
	}

	apps, charts, err := splitPlanTargets(plan.Versions)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
	}

	inputs := map[string]string{
		"dry_run": "false",
	}
	// helm_charts is sent regardless of path below: release-v2.yml's
	// release-helm-charts job reads github.event.inputs.helm_charts
	// directly (its `if` gate and `--charts` arg) -- that is not part of
	// plan-release's re-derivation this issue eliminates, so it stays a
	// flat input in both branches.
	if len(charts) > 0 {
		inputs["helm_charts"] = joinComma(charts)
	}

	if len(plan.RawJSON) > 0 {
		// Issue #927: ResolvePlan (plan.go) already produced the fully-
		// expanded release matrix -- the exact same JSON RecordResolvedPlan
		// persists onto release_run.resolved_plan -- by shelling out to
		// `release_helper_go plan --format=json`. Forward it verbatim as a
		// single `resolved_plan` workflow_dispatch input instead of
		// collapsing it into apps/version/app_versions and making
		// release-v2.yml's plan-release job re-invoke `release-helper plan`
		// from scratch (checkout + bazel setup + registry queries) just to
		// re-expand those flat inputs back into the same matrix. See
		// release-v2.yml's plan-release job: it parses-and-passes-through
		// resolved_plan when present, no re-derivation.
		inputs["resolved_plan"] = string(plan.RawJSON)
	} else {
		// Fallback for a ResolvePlan implementation that legitimately omits
		// RawJSON (e.g. a test double -- see ReleaseWorkflow's
		// RecordResolvedPlan guard in workflow.go, and this package's own
		// activities_test.go happy-path tests, which construct ResolvedPlan
		// literals with no RawJSON) or any other future dispatcher that
		// hasn't computed a resolved plan up front: reconstruct the flat
		// apps/version/app_versions inputs release-v2.yml's plan-release
		// job still knows how to fully re-derive from (its
		// `release-helper plan` fallback path, unchanged by #927 -- see
		// that job's "Plan release" step). This is pre-#927 behavior,
		// preserved rather than removed, since release-v2.yml's
		// workflow_dispatch trigger is machine-only (authorize-trigger
		// requires a bot triggering_actor -- see this workflow's header
		// comment) but not exclusively Temporal-only: any other bot-
		// credentialed dispatcher hitting this endpoint without a
		// pre-computed resolved_plan still needs a working path.
		//
		// uniformVersion is only enforced here, in the flat-input fallback
		// (issue #889 follow-up: the release-trigger UI's per-target
		// version picker needs heterogeneous plan.Versions to work at all --
		// see this file's package doc comment "DispatchBuild's version-
		// uniformity limitation"). The RawJSON branch above never uses a
		// single flat version string -- it forwards the full per-target
		// versions map verbatim -- so it must not reject a heterogeneous
		// batch; only this legacy fallback, which really does need one
		// top-level `version` input, still requires uniformity.
		version, err := uniformVersion(plan.Versions)
		if err != nil {
			return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
		}
		inputs["version"] = version
		if len(apps) > 0 {
			inputs["apps"] = joinComma(apps)
		}
		appVersions, err := appVersionsJSON(plan.Versions)
		if err != nil {
			return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
		}
		if appVersions != "" {
			inputs["app_versions"] = appVersions
		}
	}

	dispatchRef, err := resolveDispatchRef(ctx, a.Registry, plan.Versions, a.GitHub.Config.Ref)
	if err != nil {
		return BuildRef{}, fmt.Errorf("dispatch build: %w", err)
	}
	// GitHub's workflow_dispatch API only accepts a branch or tag for `ref`
	// -- a raw commit SHA (what dispatchRef usually is, FR9) is rejected
	// with 422 "No ref found for: <sha>" even when that SHA is genuinely
	// the tip of a real branch. So dispatchRef is forwarded as the
	// `build_ref` workflow input instead (release-v2.yml checks it out
	// explicitly), and Dispatch is always called with ref="" -- letting it
	// fall back to the required, always-branch/tag GitHubDispatcherConfig.Ref.
	inputs["build_ref"] = dispatchRef

	ref, err := a.GitHub.Dispatch(ctx, plan.ReleaseRunID, inputs, "")
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
