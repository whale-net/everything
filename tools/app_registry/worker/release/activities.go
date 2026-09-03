package release

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// workerLog is this package's logger, shared by every activity file here.
// Deliberately a package-level slog logger via logging.Get, not
// activity.GetLogger(ctx): several of these methods are called directly by
// unit tests with a plain context.Background() (not through Temporal's
// activity execution machinery), and activity.GetLogger panics outside a
// real activity context -- see worker/reaper and worker/outbox for the same
// "background component gets its own slog logger" precedent.
var workerLog = logging.Get("app-registry-worker-release")

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
	// FinalizePublish (issue #928, finalize.go) reuses PlanBinaryPath for
	// its own release_helper_go finalize-app/finalize-chart shell-outs, but
	// not WorkspaceRoot: neither shell-out needs a real git checkout (a
	// scratch temp dir is sufficient -- see finalize.go's package doc
	// comment), unlike ResolvePlan's `release_helper_go plan`, which needs
	// a real bazel workspace.
	PlanBinaryPath string
	WorkspaceRoot  string

	// ChartRepoURL/ChartRepoUser/ChartRepoPass are ChartMuseum credentials
	// for FinalizePublish's finalize-chart shell-out (issue #928).
	// Required for FinalizePublish when the batch has any chart targets;
	// this is the credential-locality move issue #928 asked for --
	// ChartMuseum write access now lives here, on the Temporal worker,
	// rather than in release-v2.yml's merged build job (which no longer
	// uploads to ChartMuseum at all -- see finalize.go's package doc
	// comment).
	ChartRepoURL  string
	ChartRepoUser string
	ChartRepoPass string

	// GHCRToken is a static classic PAT (write:packages, read:packages) for
	// FinalizePublish's finalize-app GHCR retag shell-out (issue #996).
	// Required whenever the batch has any app targets. Not a GitHub App
	// installation token: App installation tokens cannot write to
	// organization-owned GHCR packages outside a GitHub Actions run -- a
	// hard GitHub product limitation, not a permissions/scope gap on this
	// repo's App -- so finalize.go no longer mints one via
	// a.GitHub.token(ctx) for this purpose. a.GitHub itself (and the App
	// token it mints) is still required and unrelated: it stays the
	// credential for the GitHub Actions API calls FinalizePublish also
	// makes (ListRunArtifacts/DownloadArtifact) and for DispatchBuild/
	// PollBuild's workflow_dispatch API calls, none of which touch GHCR.
	GHCRToken string

	// ReleaseToolsS3Bucket/Endpoint/Region/AccessKey/SecretKey configure the
	// libs/go/s3.Client FinalizePublish's CLI-binary publish step (issue
	// #984, finalize.go) constructs to upload release_helper_go's/
	// app-registry's own multi-platform CLI binaries once their version is
	// confirmed (FR8-FR10 of #979) -- see finalize.go's package doc comment
	// and tools/app_registry/ENV.md's "CLI binary S3" section for the S3 key
	// convention (must match #983's ArtifactRegistry.ResolveBinaryURL read
	// side exactly). Same credential-locality pattern as ChartRepoURL/User/
	// Pass above: this worker holds write credentials, no GHA job does.
	// Required only when a batch includes a release_helper_go/app-registry
	// target -- FinalizePublish skips S3 client construction entirely
	// otherwise, so a release with no CLI-binary target needs none of these
	// set.
	ReleaseToolsS3Bucket    string
	ReleaseToolsS3Endpoint  string
	ReleaseToolsS3Region    string
	ReleaseToolsS3AccessKey string
	ReleaseToolsS3SecretKey string

	// S3Uploader, when non-nil, is used by FinalizePublish's CLI-binary
	// publish step instead of constructing a real libs/go/s3.Client from
	// ReleaseToolsS3* above -- the test seam finalize_test.go substitutes a
	// fake through (issue #984's Testing scope: covers upload key
	// correctness, no-client-when-no-CLI-binary-target, EffectiveVersion
	// divergence, and missing-artifact per-target failure without a real S3
	// endpoint). Production (worker/main.go) leaves this nil; FinalizePublish
	// lazily constructs a real client from ReleaseToolsS3* the first time a
	// batch actually needs one.
	S3Uploader binaryUploader
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
// Idempotency (NFR3/FR11, migration 023): DispatchBuild checks-before-
// dispatch against release_run.build_ref_run_id/build_ref_run_url before
// calling GitHub at all, and stamps that pair once its own GitHub.Dispatch
// call succeeds (see the body below) -- so a retried DispatchBuild
// (workflow.go's defaultActivityOptions gives it MaximumAttempts: 5, e.g.
// after a transient network error on the dispatch POST itself, or on
// findDispatchedRun's polling) returns the already-dispatched run instead
// of calling `workflow_dispatch` a second time. This was previously an
// accepted, undocumented-beyond-this-comment gap: a second GHA run for the
// same release isn't just wasteful -- release-v2.yml's concurrency group
// (concurrency: {group: release-v2, cancel-in-progress: false}) only keeps
// ONE additional pending run queued behind the one currently running, so a
// third run created while one is running and one is already queued causes
// GitHub to silently cancel the older queued run (observed in production:
// run 32691150140, "Canceling since a higher priority waiting request for
// release-v2 exists", recorded zero jobs -- cancelled before it ever
// started). Two things already true at this phase remain true as
// additional defense-in-depth, not as the primary guard anymore: (1)
// VerifyPublished treats already-published targets as satisfied regardless
// of which of two concurrent runs published them first (see that method's
// doc comment), and (2) the underlying GHA job's own BeginPublish/
// RecordArtifact calls are already idempotent by digest/version
// (architecture/08-release-lifecycle/04-run-log.md) -- a second concurrent
// run publishing the same version is a no-op or a detected conflict there,
// not a corrupted publish. Note this narrows, but does not by itself
// close, every path to a duplicate dispatch: two independent
// ReleaseWorkflow executions for two genuinely different (disjoint)
// target batches still call DispatchBuild with two different
// release_run_ids, so this per-release_run check does not serialize them
// against each other -- see repository.ReleaseRunRepository.SetBuildRef's
// doc comment.
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

	// Check-before-dispatch (migration 023): if a prior call for this exact
	// release run already dispatched and located a GitHub Actions run --
	// the common case being Temporal redelivering this activity after a
	// transient error on the dispatch POST itself or on
	// findDispatchedRun's polling, see this method's doc comment -- return
	// that run unchanged instead of calling `workflow_dispatch` again.
	// GetReleaseRun erroring (e.g. a synthetic/test release run id that was
	// never created via CreateReleaseRun, or a transient read failure) is
	// treated as "no known prior dispatch", not fatal -- this check is a
	// best-effort idempotency guard, not a strict precondition, and this
	// method has never required Registry to hold a release_run row for
	// plan.ReleaseRunID (see the FR9-FR11 app_build_log resolution below,
	// which is keyed by target, not by release run).
	if run, _, err := a.Registry.ReleaseRuns().GetReleaseRun(ctx, plan.ReleaseRunID); err == nil && run.BuildRefRunID != "" {
		workerLog.Info("build already dispatched, reusing existing run",
			"release_run_id", plan.ReleaseRunID, "run_id", run.BuildRefRunID, "run_url", run.BuildRefRunURL)
		return BuildRef{ReleaseRunID: plan.ReleaseRunID, RunID: run.BuildRefRunID, RunURL: run.BuildRefRunURL}, nil
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
	// Best-effort persist (migration 023): the GitHub Actions run is
	// already dispatched and real at this point, so a failure here must
	// never fail this activity -- doing so would make Temporal retry
	// DispatchBuild and dispatch yet another run, which is exactly the
	// duplicate-dispatch failure mode this write exists to prevent on the
	// NEXT retry. A failed write here just means this one dispatch isn't
	// remembered, degrading to today's behavior (no check-before-dispatch)
	// rather than regressing it.
	if serr := a.Registry.ReleaseRuns().SetBuildRef(ctx, plan.ReleaseRunID, ref.RunID, ref.RunURL); serr != nil {
		workerLog.Warn("best-effort persist of dispatched build ref failed; a retry may dispatch a duplicate run",
			"release_run_id", plan.ReleaseRunID, "run_id", ref.RunID, "error", serr)
	}
	workerLog.Info("build dispatched", "release_run_id", plan.ReleaseRunID, "run_id", ref.RunID, "run_url", ref.RunURL)
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
