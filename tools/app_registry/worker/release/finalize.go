// finalize.go implements FinalizePublish (issue #928): the new post-build
// "finalize and publish" step that runs after DispatchBuild/PollBuild
// succeed. release-v2.yml's merged release-trigger job (issue #928's
// "build-release-artifacts") builds and pushes every app image by digest
// (a build-scoped tag, not the final version) and composes every chart's
// source tree without packaging or uploading it -- see
// tools/release_helper_go/cmd/build_app.go/build_chart.go. Neither step
// assigns a final version, uploads to ChartMuseum, or mutates App
// Registry. FinalizePublish is where that critical, safety-sensitive work
// actually happens:
//
//  1. Download the completed GHA run's "build-manifest" and
//     "chart-sources" workflow artifacts (the same actions/upload-artifact
//     mechanism this workflow already used for release-notes/digest-check/
//     helm-charts before this task -- see github.go's ListRunArtifacts/
//     DownloadArtifact, added by this task specifically for this purpose).
//     How build outputs get from GHA back to Temporal was not an explicit
//     "known unknown" in issue #928's own writeup, but is exactly that
//     class of gap -- this is the resolved design call, reusing an
//     existing mechanism rather than inventing a new callback channel.
//  2. For each app target, shell out to `release_helper_go finalize-app`
//     (retags the already-pushed digest to the resolved version via
//     go-containerregistry's crane.Tag -- see releaser_ghcr_retag.go's doc
//     comment for why a Go library beats a vendored CLI binary here).
//  3. For each chart target, shell out to `release_helper_go
//     finalize-chart` (packages the downloaded chart source tree with the
//     resolved version/app-versions and uploads to ChartMuseum).
//
// Both CLI commands reuse ExecuteRelease unchanged -- BeginPublish/
// FailPublish's two-phase reservation and RecordArtifact's compare-and-
// swap-on-unique-constraint-collision are exactly the same machinery
// release-app/release-charts already used inline in GHA (issue #928's
// explicit ask: do not reinvent this).
//
// Like ResolvePlan (see plan.go's package doc comment), this is an interim
// shell-out to release_helper_go rather than an in-process library call.
// Unlike ResolvePlan, neither finalize-app nor finalize-chart needs a real
// git checkout at all: both used to pass `--create-git-tag` (running `git
// tag`+`git push` against a real authenticated remote FinalizePublish
// cloned per invocation), but that flag and the clone-per-invocation
// plumbing it required were removed -- FinalizePublish's version-of-record
// lives in App Registry, not in a git tag, and finalize-app's retag
// (--repository/--digest) and finalize-chart's packaging (--chart-dir, an
// absolute path under the scratch tmpDir) never read anything relative to
// a git checkout. Both subprocesses now run with tmpDir (already created
// for the downloaded build artifacts, see below) as their cwd instead.
// `helm` must still be on PATH wherever this process runs (neither
// finalize-app nor finalize-chart shells out to bazel).
package release

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/s3"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// Workflow artifact names the merged release-trigger GHA job uploads and
// this activity looks for -- must match release-v2.yml's
// actions/upload-artifact `name:` fields exactly.
const (
	buildManifestArtifactName = "build-manifest"
	chartSourcesArtifactName  = "chart-sources"
	// cliBinariesArtifactName is #981's build-cli-binaries job's
	// actions/upload-artifact name -- see release-v2.yml. Unlike the two
	// artifacts above, its absence from a run is expected and not an error:
	// most releases don't include release_helper_go/app-registry (that job
	// only runs when the release matrix contains one of them).
	cliBinariesArtifactName = "cli-binaries"
)

// cliBinaryTargets maps the App Registry full_name (repository.TargetKey's
// ownerFullName, ArtifactKindImage) of release_helper_go's and
// app-registry's own app records to the "binary name" segment #981's
// build-cli-binaries job and #983's S3 key convention both use (the bare
// app.Name, not the domain-qualified full_name) -- e.g. full_name
// "tools-release_helper_go" packages files under
// cli-binaries/release_helper_go/ (see release-v2.yml's "Package CLI
// binaries" step) and publishes under S3 key prefix
// "release_helper_go/<version>/" (see tools/app_registry/ENV.md's "CLI
// binary S3" section). Sourced from tools/release_helper_go/BUILD.bazel
// (domain "tools", app_name "release_helper_go") and
// tools/app_registry/cli/BUILD.bazel (domain "tools", app_name
// "app-registry") -- both domain "tools", matching release-v2.yml's
// resolve_registry_version "tools-release_helper_go"/"tools-app-registry"
// calls.
var cliBinaryTargets = map[string]string{
	"tools-release_helper_go": "release_helper_go",
	"tools-app-registry":      "app-registry",
}

// binaryUploader is the S3 upload seam FinalizePublish's CLI-binary publish
// step uses -- satisfied by *s3.Client's Upload method (libs/go/s3, no
// changes needed there per issue #984's Implementation scope) and
// substituted by a fake in finalize_test.go. Deliberately narrow (just
// Upload, matching libs/go/s3.Client.Upload's exact signature) since that is
// the only method this step needs -- see Activities.S3Uploader's doc
// comment.
type binaryUploader interface {
	Upload(ctx context.Context, key string, data []byte, opts *s3.UploadOptions) (string, error)
}

var _ binaryUploader = (*s3.Client)(nil)

// buildAppManifest mirrors release_helper_go/cmd/build_app.go's
// BuildAppManifest JSON shape -- decoded from each <full_name>.json file
// inside the "build-manifest" artifact. A local, minimal type rather than
// importing release_helper_go/cmd (mirrors plan.go's planCLIResult doc
// comment: this package should not depend on the CLI-oriented package's
// internals just for a struct shape).
type buildAppManifest struct {
	Domain     string `json:"domain"`
	App        string `json:"app"`
	FullName   string `json:"full_name"`
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
}

// buildChartManifestEntry mirrors release_helper_go/cmd/build_chart.go's
// BuildChartResult JSON shape, as listed in the "chart-sources" artifact's
// root manifest.json (see BuildChartManifestFileName).
type buildChartManifestEntry struct {
	ChartName string `json:"chart_name"`
	Domain    string `json:"domain"`
	FullName  string `json:"full_name"`
	ChartDir  string `json:"chart_dir"`
}

// finalizeCLIResult mirrors release_helper_go/cmd/releaser.go's
// ReleaseResult JSON shape -- only the field this activity actually needs.
// Both finalize-app and finalize-chart write this to --output-dir as
// <domain>-<name>.json (see finalize_app.go/finalize_chart.go).
// EffectiveVersion is the version finalize-app/finalize-chart actually
// published under, which can differ from the plan-time version requested
// via --version whenever ExecuteRelease's no-op detection reuses an
// already-published version instead (identical digest) -- see this file's
// loops over apps/charts for why appVersions and FinalizeTargetOutcome must
// use this, not plan.Versions.
type finalizeCLIResult struct {
	EffectiveVersion string `json:"effective_version"`
}

// readFinalizeResult reads and parses the finalize-app/finalize-chart
// result file --output-dir writes for the given domain/name, shared by both
// the apps and charts loops below (apps and charts each write to their own
// dir, so a shared "<domain>-<name>.json" naming convention cannot collide
// even if an app and a chart happen to share a domain+name).
func readFinalizeResult(dir, domain, name string) (finalizeCLIResult, error) {
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.json", domain, name))
	data, err := os.ReadFile(path)
	if err != nil {
		return finalizeCLIResult{}, fmt.Errorf("read %s: %w", path, err)
	}
	var res finalizeCLIResult
	if err := json.Unmarshal(data, &res); err != nil {
		return finalizeCLIResult{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if res.EffectiveVersion == "" {
		return finalizeCLIResult{}, fmt.Errorf("%s: effective_version missing", path)
	}
	return res, nil
}

// resolvedPlanBuildID is the subset of `release_helper_go plan
// --format=json`'s output FinalizePublish needs beyond what ResolvedPlan
// already carries -- see plan.go's identical planCLIResult pattern.
type resolvedPlanBuildID struct {
	BuildID string `json:"build_id"`
}

// FinalizeTargetOutcome is FinalizePublish's precise per-target result --
// the authoritative signal for whether THIS target's finalize actually
// succeeded, superseding VerifyPublished's indirect presence-check for any
// target this map has an entry for (see workflow.go's ReleaseWorkflow: a
// target found in FinalizeResult.Targets with Failed=true is recorded
// failed directly, without ever consulting VerifyPublished's result for
// it). This is issue #973's proper fix: PR #976's first pass re-derived an
// "expected version" from release_run.resolved_plan and compared it
// against VerifyPublished's presence check, but that re-derivation cannot
// distinguish a real finalize failure from ExecuteRelease's legitimate
// no-op-rebuild path (identical digest reuses an older already-published
// version instead of the plan-time one -- see EffectiveVersion below).
// Threading FinalizePublish's own direct knowledge through in-process
// avoids re-guessing it downstream at all.
type FinalizeTargetOutcome struct {
	// Failed is true if this target's finalize-app/finalize-chart
	// subprocess failed (build-manifest missing, CLI exited non-zero,
	// result file unreadable, etc).
	Failed bool
	// Detail is the failure reason, populated only when Failed.
	Detail string
	// EffectiveVersion is the version this target actually got published
	// under, populated only when !Failed. Can differ from the plan-time
	// requested version when ExecuteRelease's no-op-rebuild detection
	// reused an already-published older version instead (identical
	// digest) -- see finalizeCLIResult's doc comment for the precedent;
	// this is the same field, just also captured for charts and exposed
	// to the workflow instead of only used internally for chart pinning.
	EffectiveVersion string
}

// FinalizeResult is FinalizePublish's outcome. Unlike PollBuild's
// BuildStatus (a single pass/fail for the whole GHA run), FinalizePublish
// deliberately does not fail the whole activity when only some targets
// fail to finalize -- see the per-target failure handling below and this
// function's doc comment.
type FinalizeResult struct {
	// Succeeded is true only if every target in the batch finalized
	// without error (equivalently: no entry in Targets has Failed=true).
	// Aggregate/operator-facing only -- ReleaseWorkflow acts on Targets,
	// not this field.
	Succeeded bool
	// Detail summarizes any per-target failures (empty when Succeeded).
	// Operator-facing context only -- ReleaseWorkflow acts on Targets, not
	// this field.
	Detail string
	// Targets carries FinalizePublish's own per-target outcome, keyed by
	// repository.TargetKey(kind, ownerFullName) -- the authoritative
	// signal ReleaseWorkflow uses to decide a target's pass/fail directly,
	// superseding VerifyPublished's indirect presence-check for any target
	// with an entry here. See FinalizeTargetOutcome's doc comment.
	Targets map[string]FinalizeTargetOutcome
}

// FinalizePublish implements the new post-build finalize/publish step --
// see this file's package doc comment for the full design. Deliberately
// does not return a hard error for an individual target's finalize
// failure: a single bad app/chart must not prevent every other target in
// the batch from finalizing. It returns a hard error only for a
// batch-level setup failure (can't reach GitHub's artifacts API, no
// workspace configured, malformed plan) -- conditions where no target
// could possibly have finalized. Per-target outcomes are reported
// precisely via FinalizeResult.Targets (see FinalizeTargetOutcome's doc
// comment): workflow.go's ReleaseWorkflow routes any target with an entry
// here whose Failed is true directly to ReleaseRunTargetStateFailed,
// bypassing VerifyPublished entirely for that target. Everything else --
// a target that finalized successfully, or an ambiguous case left with no
// entry at all (e.g. this function's own bookkeeping-write-failure
// fallback, see the apps loop below) -- flows to VerifyPublished for the
// real registry-state check. FinalizeResult.Detail/Succeeded are
// operator-facing aggregates derived from Targets; ReleaseWorkflow itself
// never acts on them directly.
func (a *Activities) FinalizePublish(ctx context.Context, plan ResolvedPlan, ref BuildRef) (FinalizeResult, error) {
	if a.GitHub == nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: GitHub dispatcher not configured")
	}
	if len(plan.Versions) == 0 {
		return FinalizeResult{}, fmt.Errorf("finalize publish: resolved plan has no versions")
	}

	apps, charts, err := splitPlanTargets(plan.Versions)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "release-finalize-")
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck
	// finalize-app/finalize-chart no longer need a real git checkout --
	// neither shell-out reads files relative to its process cwd (finalize-
	// app's retag uses --repository/--digest directly; finalize-chart's
	// packaging uses --chart-dir, an absolute path under tmpDir) now that
	// --create-git-tag is gone (see package doc comment). tmpDir, already
	// created for the downloaded artifacts below, doubles as cmd.Dir.

	appsDir := filepath.Join(tmpDir, "apps")
	chartsDir := filepath.Join(tmpDir, "charts")
	// cliBinariesDir mirrors build-cli-binaries's own /tmp/cli-binaries/
	// layout once extracted: cliBinariesDir/<binary-name>/<binary-name>-
	// <os>-<arch> plus cliBinariesDir/<binary-name>/checksums.txt -- see
	// cliBinaryTargets' doc comment. Populated below only if this run
	// actually produced the artifact (haveCLIBinaries); its absence is
	// expected, not an error, unlike build-manifest/chart-sources.
	cliBinariesDir := filepath.Join(tmpDir, "cli-binaries")

	artifactList, err := a.GitHub.ListRunArtifacts(ctx, ref)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: %w", err)
	}
	var haveManifest, haveCharts, haveCLIBinaries bool
	for _, art := range artifactList {
		var dest string
		switch art.Name {
		case buildManifestArtifactName:
			dest = appsDir
		case chartSourcesArtifactName:
			dest = chartsDir
		case cliBinariesArtifactName:
			dest = cliBinariesDir
		default:
			continue
		}
		data, derr := a.GitHub.DownloadArtifact(ctx, art.ID)
		if derr != nil {
			return FinalizeResult{}, fmt.Errorf("finalize publish: download %s artifact: %w", art.Name, derr)
		}
		if uerr := unzipTo(data, dest); uerr != nil {
			return FinalizeResult{}, fmt.Errorf("finalize publish: extract %s artifact: %w", art.Name, uerr)
		}
		switch art.Name {
		case buildManifestArtifactName:
			haveManifest = true
		case chartSourcesArtifactName:
			haveCharts = true
		case cliBinariesArtifactName:
			haveCLIBinaries = true
		}
	}
	// A batch of only CLI apps (cliBinaryTargets) never needs the
	// build-manifest artifact at all: release-v2.yml's build-app step
	// deliberately writes no manifest entry for a "cli" app_type (see the
	// isCLI skip below), so the CI run has nothing to upload there and
	// legitimately produces zero build-manifest files -- reproduced in
	// prod for the "tools" domain's own release (targets
	// tools-app-registry/tools-release_helper_go, both CLI-only): the run
	// succeeded end-to-end, but this guard demanded a manifest artifact
	// that was never supposed to exist, failing every CLI-only batch.
	nonCLIAppCount := 0
	for _, fullName := range apps {
		if _, isCLI := cliBinaryTargets[fullName]; !isCLI {
			nonCLIAppCount++
		}
	}
	if nonCLIAppCount > 0 && !haveManifest {
		return FinalizeResult{}, fmt.Errorf("finalize publish: run %s produced no %q artifact for %d app target(s)", ref.RunID, buildManifestArtifactName, nonCLIAppCount)
	}
	if len(charts) > 0 && !haveCharts {
		return FinalizeResult{}, fmt.Errorf("finalize publish: run %s produced no %q artifact for %d chart target(s)", ref.RunID, chartSourcesArtifactName, len(charts))
	}
	appManifests, err := loadAppManifests(appsDir)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: %w", err)
	}
	chartManifests, err := loadChartManifest(chartsDir)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: %w", err)
	}

	// buildID gates BeginPublish/RecordArtifact for every target kind below
	// (apps, charts, and publishCLIBinaries) -- planBuildID's doc comment
	// documents this as an accepted, tolerated skip ("empty build_id ->
	// registry recording simply skipped"), on the assumption that an empty
	// value is rare. In practice, for this activity's real invocation
	// (release_helper_go plan run as a subprocess with no GITHUB_RUN_ID/
	// GITHUB_SHA in its environment -- see this file's package doc
	// comment), planRelease's own RecordBuild call (plan.go's "2.
	// RecordBuild" step) has been observed to consistently leave
	// PlanResult.BuildID empty, which -- because of json:"build_id,omitempty"
	// -- disappears from RawJSON entirely rather than surfacing as an
	// error anywhere. Confirmed against a real prod release
	// (b9fa55ce-01c9-45c4-b56b-2b4a719b89d9, tools-app-registry/
	// tools-release_helper_go v0.7.0): FinalizePublish reported
	// Succeeded:true with the right EffectiveVersion, but no
	// data.build row and no data.artifact row past "allocated" ever
	// existed for it -- the "tolerated skip" was silently skipping every
	// single Temporal-driven release, not just a rare edge case.
	//
	// CORRECTION (this fix): a random UUID is NOT safe here. Migration 001
	// declares `artifact.build_id UUID ... REFERENCES build (build_id)`,
	// and migration 007 only ever drops that column's NOT NULL for the
	// "allocated" state -- the FK itself was never dropped. A build_id that
	// names no row in `build` makes BeginPublish's own UPDATE (and
	// RecordArtifact's INSERT) fail with `artifact_build_id_fkey`, exactly
	// reproducing the "not found"/skip symptom this block was written to
	// fix, just as a hard error instead of a silent skip. So instead of
	// fabricating an unresolvable id, record a real (if minimal) `build`
	// row and use its real build_id -- BuildRepository.RecordBuild upserts
	// on (workflow_run_id, workflow_attempt), so keying it off ref.RunID
	// (the GitHub Actions run this activity already knows completed, per
	// PollBuild above) both satisfies the FK and ties the row back to the
	// actual run, which is strictly more traceable than a random UUID ever
	// was. git_sha is deliberately a placeholder, not ref-derived (nothing
	// in ResolvedPlan/BuildRef carries the head commit): unresolvable, it
	// makes resolveManifestForPublish's build_id -> git_sha lookup fall
	// through to the owner's current manifest interval, identical to its
	// existing behavior for a genuinely empty buildID.
	buildID := planBuildID(plan.RawJSON)
	if buildID == "" {
		workflowRunID := ref.RunID
		if workflowRunID == "" {
			workflowRunID = uuid.NewString()
		}
		b, _, berr := a.Registry.Builds().RecordBuild(ctx, repository.Build{
			GitSHA:          "unknown",
			WorkflowRunID:   workflowRunID,
			WorkflowAttempt: 1,
			Actor:           "app-registry-worker",
		})
		if berr != nil {
			return FinalizeResult{}, fmt.Errorf("finalize publish: record build for empty plan build_id: %w", berr)
		}
		buildID = b.BuildID
	}

	// idempotencyKeyPrefix is passed to every finalize-app/finalize-chart
	// subprocess call below as --idempotency-key-prefix, so their own
	// BeginPublish/RecordArtifact idempotency keys
	// ("<prefix>-<owner>-<kind>-begin"/"-record", releaser.go) are unique
	// per Temporal execution. Without it (the bug this fixes), releaser.go
	// falls back to GITHUB_RUN_ID/GITHUB_RUN_ATTEMPT env reads -- absent in
	// this activity's subprocess environment (same gap #1069/#1090 already
	// found for other env-var-dependent fallbacks in this same CLI) -- and
	// lands on the literal placeholder "local-1" every time, for every
	// worker-invoked release. That's not merely a skip like #1090: since
	// "local-1" never varies, EVERY app/chart finalize going through this
	// path would collide on the exact same idempotency key, and the
	// registry's idempotent-replay contract means the second-ever such
	// call replays the FIRST one's cached response instead of recording
	// the real new version -- a silent stale-data write, not just a
	// missed one. RunID (not WorkflowExecution.ID -- see #1084's fix in
	// plan.go's ResolvePlan for why) is unique per execution and stable
	// across this activity's own at-least-once retries, exactly the
	// property an idempotency-key prefix needs.
	idempotencyKeyPrefix := ""
	if info := activity.GetInfo(ctx); info.WorkflowExecution.RunID != "" {
		idempotencyKeyPrefix = info.WorkflowExecution.RunID
	}

	var ghcrToken string
	if nonCLIAppCount > 0 {
		// A GitHub App installation token cannot write to organization-owned
		// GHCR packages when used outside a GitHub Actions run (issue
		// #996) -- a.GHCRToken is a static bot-account PAT instead, not
		// minted from a.GitHub. An all-CLI batch never retags a GHCR image
		// at all (same reasoning as the build-manifest guard above), so it
		// must not be blocked on this either.
		if a.GHCRToken == "" {
			return FinalizeResult{}, fmt.Errorf("finalize publish: GHCR token not configured (set RELEASE_GHCR_TOKEN)")
		}
		ghcrToken = a.GHCRToken
	}

	binary := a.PlanBinaryPath
	if binary == "" {
		binary = "release_helper_go"
	}

	appVersions := make(map[string]string, len(apps))
	appDigests := make(map[string]string, len(apps))
	finalizeResultsDir := filepath.Join(tmpDir, "finalize-results")
	// finalizeTargets carries FinalizePublish's own precise per-target
	// outcome back to the caller (workflow.go's ReleaseWorkflow) -- see
	// FinalizeResult.Targets/FinalizeTargetOutcome's doc comments.
	// failures collects the same Detail strings inline, at each Failed
	// assignment site below, for FinalizeResult.Detail/Succeeded -- a
	// single edit point per failure kind, not a second hand-maintained
	// list: appending here instead of re-deriving via a second traversal
	// after both loops finish avoids re-iterating apps/charts and
	// recomputing repository.TargetKey a second time just to reconstruct
	// what this loop already knows at the point each failure happens
	// (this PR's Finding 5).
	finalizeTargets := make(map[string]FinalizeTargetOutcome, len(apps)+len(charts))
	var failures []string

	for _, fullName := range apps {
		key := repository.TargetKey(repository.ArtifactKindImage, fullName)
		version := plan.Versions[key]

		// cliBinaryTargets apps (release_helper_go, app-registry) are
		// app_type "cli" -- release.bzl never generates an image-push
		// target for them, so build-app deliberately writes no
		// build-manifest entry for these (see build_app.go's
		// ExecuteBuildApp). Skip the image finalize-app flow entirely;
		// publishCLIBinaries below is their actual publish step, keyed off
		// plan.Versions directly rather than a finalize-app confirmation
		// that will never come.
		if _, isCLI := cliBinaryTargets[fullName]; isCLI {
			continue
		}

		m, ok := appManifests[fullName]
		if !ok {
			detail := fmt.Sprintf("%s: no build-manifest entry in run %s", fullName, ref.RunID)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
			failures = append(failures, detail)
			workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
			continue
		}
		appDigests[fullName] = m.Digest

		args := []string{
			"finalize-app",
			"--domain", m.Domain,
			"--app", m.App,
			"--version", version,
			"--repository", m.Repository,
			"--digest", m.Digest,
			"--output-dir", finalizeResultsDir,
		}
		if buildID != "" {
			args = append(args, "--build-id", buildID)
		}
		if idempotencyKeyPrefix != "" {
			args = append(args, "--idempotency-key-prefix", idempotencyKeyPrefix)
		}
		if _, err := runReleaseHelper(ctx, binary, tmpDir, []string{"GHCR_TOKEN=" + ghcrToken}, args...); err != nil {
			detail := fmt.Sprintf("%s: finalize-app: %v", fullName, err)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
			failures = append(failures, detail)
			workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
			continue
		}

		// A no-op rebuild (identical digest to an already-published
		// version) means finalize-app reused that older version instead
		// of --version -- see finalizeCLIResult's doc comment. A chart
		// composed from this app must pin the version it ACTUALLY
		// published under, not the plan-time --version, which stays
		// unpublished in that case and would fail finalize-chart's own
		// hermeticity/pin check ("chart pins N unpublished app(s)") --
		// this is release.yml's release-helm-charts digest-check-override
		// bug (see that fix's PR), reproduced here if left unhandled. The
		// same EffectiveVersion is also this target's FinalizeTargetOutcome
		// -- VerifyPublished must compare against it, not the unpublished
		// plan-time version, or a legitimate no-op rebuild would be
		// misreported as a failure (issue #973's PR #976 first-pass gap).
		res, rerr := readFinalizeResult(finalizeResultsDir, m.Domain, m.App)
		if rerr != nil {
			// finalize-app's subprocess above already exited 0 -- the
			// actual retag/publish/RecordArtifact work succeeded. A
			// missing/corrupt --output-dir result file only means the
			// bookkeeping sidecar write failed (e.g. finalize_app.go's own
			// write hit disk-full) -- that is NOT evidence the publish
			// itself failed, and must not have the power to falsely fail a
			// release that actually succeeded (this PR's Finding 1).
			// Deliberately leave this target with NO entry in
			// finalizeTargets: workflow.go's ReleaseWorkflow then finds no
			// entry in finalizeFailures/expectedVersions for it and falls
			// through to VerifyPublished's real presence/state check
			// (record.go's defensive fallback) to decide its fate from
			// actual App Registry state, instead of a guess made here.
			//
			// appVersions still needs an entry for this app, though:
			// unlike finalizeTargets (only consulted by VerifyPublished's
			// per-target logic), appVersions is marshaled into
			// --app-versions for every chart in this same batch that
			// composes this app (see below). A missing key there is not a
			// "fall through and check later" case -- finalize_chart.go's
			// hermeticity-pin check only iterates keys present in
			// AppVersions (so a missing app can't even be flagged as an
			// unpublished pin) and build_helm.go's packageChartWithVersion
			// only rewrites values.yaml imageTag entries for keys present
			// in the map, so an absent key means the composed chart
			// silently keeps whatever imageTag was baked in at build time.
			// Fall back to the plan-time requested `version` (already in
			// scope above, the same value passed as finalize-app's
			// --version). This is safe, not a guess: ExecuteRelease
			// (releaser.go) runs its step-2 Build unconditionally, before
			// any no-op-rebuild collision detection in step 3. For
			// GHCRRetagReleaser (releaser_ghcr_retag.go), Build performs a
			// real crane.Tag write of `version` against the already-pushed
			// digest -- that registry-side tag exists and points at the
			// right content by the time finalize-app's process exits 0,
			// regardless of whatever the no-op-rebuild logic in step 3
			// later decides about App Registry's "official" published-
			// version bookkeeping (that decision only affects which
			// version string App Registry considers latest, not which tag
			// exists in the registry). So a chart pinning to `version`
			// here resolves to the correct image content either way.
			appVersions[fullName] = version
			continue
		}
		appVersions[fullName] = res.EffectiveVersion
		finalizeTargets[key] = FinalizeTargetOutcome{EffectiveVersion: res.EffectiveVersion}
	}

	// Publish release_helper_go's/app-registry's own CLI binaries now that
	// the apps loop above has recorded each target's confirmed
	// EffectiveVersion (FR8-FR9 of #979) -- see publishCLIBinaries' doc
	// comment. Deliberately runs after the apps loop entirely (not inline
	// per-target) so it only ever considers finalizeTargets entries that
	// already reflect a confirmed finalize-app outcome, never a plan-time
	// guess.
	failures = append(failures, a.publishCLIBinaries(ctx, apps, plan.Versions, finalizeTargets, cliBinariesDir, haveCLIBinaries, buildID)...)

	appVersionsJSON, _ := json.Marshal(appVersions) //nolint:errcheck
	appDigestsJSON, _ := json.Marshal(appDigests)   //nolint:errcheck

	var chartEnv []string
	if a.ChartRepoURL != "" {
		chartEnv = append(chartEnv, "CHART_REPO_URL="+a.ChartRepoURL)
	}
	if a.ChartRepoUser != "" {
		chartEnv = append(chartEnv, "CHART_REPO_USER="+a.ChartRepoUser)
	}
	if a.ChartRepoPass != "" {
		chartEnv = append(chartEnv, "CHART_REPO_PASS="+a.ChartRepoPass)
	}

	// chartFinalizeResultsDir is separate from apps' finalizeResultsDir
	// (rather than sharing one directory) so that readFinalizeResult's
	// shared "<domain>-<name>.json" naming convention cannot collide even
	// if an app and a chart happen to share the same domain+name.
	chartFinalizeResultsDir := filepath.Join(tmpDir, "finalize-results-charts")

	for _, fullName := range charts {
		key := repository.TargetKey(repository.ArtifactKindChart, fullName)
		version := plan.Versions[key]
		c, ok := chartManifests[fullName]
		if !ok {
			detail := fmt.Sprintf("%s: no chart-sources manifest entry in run %s", fullName, ref.RunID)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
			failures = append(failures, detail)
			workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
			continue
		}

		args := []string{
			"finalize-chart",
			"--chart", c.ChartName,
			"--domain", c.Domain,
			"--chart-dir", filepath.Join(chartsDir, c.ChartDir),
			"--version", version,
			"--app-versions", string(appVersionsJSON),
			"--app-digests", string(appDigestsJSON),
			"--output-dir", chartFinalizeResultsDir,
		}
		if buildID != "" {
			args = append(args, "--build-id", buildID)
		}
		if idempotencyKeyPrefix != "" {
			args = append(args, "--idempotency-key-prefix", idempotencyKeyPrefix)
		}
		if _, err := runReleaseHelper(ctx, binary, tmpDir, chartEnv, args...); err != nil {
			detail := fmt.Sprintf("%s: finalize-chart: %v", fullName, err)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
			failures = append(failures, detail)
			workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
			continue
		}

		// Same EffectiveVersion-vs-plan-time-version divergence as apps
		// above (ExecuteRelease's no-op-rebuild path) -- captured here so
		// VerifyPublished can compare against it instead of the unpublished
		// plan-time version.
		res, rerr := readFinalizeResult(chartFinalizeResultsDir, c.Domain, c.ChartName)
		if rerr != nil {
			// Same reasoning as the app loop above: finalize-chart's
			// subprocess already exited 0 -- the actual package/upload/
			// RecordArtifact work succeeded. Leave this target with NO
			// entry in finalizeTargets so VerifyPublished's real check
			// decides its fate instead of a bookkeeping-write failure here.
			continue
		}
		finalizeTargets[key] = FinalizeTargetOutcome{EffectiveVersion: res.EffectiveVersion}
	}

	if len(failures) > 0 {
		workerLog.Warn("finalize publish completed with failures",
			"run_id", ref.RunID, "target_count", len(apps)+len(charts), "failure_count", len(failures))
		return FinalizeResult{Succeeded: false, Detail: strings.Join(failures, "; "), Targets: finalizeTargets}, nil
	}
	workerLog.Info("finalize publish succeeded", "run_id", ref.RunID, "target_count", len(finalizeTargets))
	return FinalizeResult{Succeeded: true, Targets: finalizeTargets}, nil
}

// runReleaseHelper invokes binary (release_helper_go) with cmd.Dir set to
// dir and extraEnv appended to the current process's environment. Unlike
// plan.go's ResolvePlan shell-out (which needs a real bazel workspace
// checkout), dir here is just a scratch directory -- neither finalize-app
// nor finalize-chart reads anything relative to their process cwd (see
// package doc comment) -- so callers pass tmpDir, the same scratch
// directory FinalizePublish already created for the downloaded build
// artifacts, purely so the subprocess has *some* valid working directory.
func runReleaseHelper(ctx context.Context, binary, dir string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	// The subprocess can exit 0 while still having written operator-facing
	// diagnostics to stderr -- notably finalize_app.go/finalize_chart.go's
	// "::warning::...failed to write --output-dir result file..." (added
	// specifically so an operator has visibility into the Finding-1-style
	// bookkeeping-write failure, which does NOT fail the CLI process).
	// Surface it the same way ExecuteRelease's own ::warning::/::notice::
	// lines reach the workflow/activity log (releaser.go prints those
	// directly via fmt.Printf) -- otherwise a success-path stderr message
	// is silently discarded here and never reaches any log an operator
	// could see.
	if s := strings.TrimSpace(stderr.String()); s != "" {
		fmt.Println(s)
	}
	return stdout.String(), nil
}

// unzipTo extracts a zip archive's bytes (the format GitHub's artifact
// download endpoint returns) into destDir, creating it if needed.
func unzipTo(data []byte, destDir string) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	for _, f := range r.File {
		targetPath := filepath.Join(destDir, f.Name) //nolint:gosec
		if !strings.HasPrefix(targetPath, filepath.Clean(destDir)+string(os.PathSeparator)) && targetPath != filepath.Clean(destDir) {
			return fmt.Errorf("zip entry %q escapes destination directory", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %q: %w", f.Name, err)
		}
		out, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			rc.Close() //nolint:errcheck
			return fmt.Errorf("create %s: %w", targetPath, err)
		}
		_, copyErr := io.Copy(out, rc) //nolint:gosec
		closeErr := out.Close()
		rc.Close() //nolint:errcheck
		if copyErr != nil {
			return fmt.Errorf("write %s: %w", targetPath, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close %s: %w", targetPath, closeErr)
		}
	}
	return nil
}

// loadAppManifests reads every <full_name>.json file directly under dir
// (build_app.go's BuildAppManifest, one per app target) into a map keyed
// by FullName. dir not existing (no apps in this batch) is not an error.
func loadAppManifests(dir string) (map[string]buildAppManifest, error) {
	out := map[string]buildAppManifest{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read app manifests dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read app manifest %s: %w", e.Name(), err)
		}
		var m buildAppManifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse app manifest %s: %w", e.Name(), err)
		}
		if m.FullName != "" {
			out[m.FullName] = m
		}
	}
	return out, nil
}

// loadChartManifest reads build_chart.go's BuildChartManifestFileName
// (manifest.json) at the root of dir into a map keyed by FullName (the
// *published* chart name -- see BuildChartResult's doc comment). dir not
// existing (no charts in this batch) is not an error.
func loadChartManifest(dir string) (map[string]buildChartManifestEntry, error) {
	out := map[string]buildChartManifestEntry{}
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read chart manifest %s: %w", path, err)
	}
	var entries []buildChartManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse chart manifest %s: %w", path, err)
	}
	for _, e := range entries {
		if e.FullName != "" {
			out[e.FullName] = e
		}
	}
	return out, nil
}

// publishCLIBinaries uploads release_helper_go's/app-registry's own
// multi-platform CLI binaries to S3, once per qualifying target in apps --
// see cliBinaryTargets' doc comment for the full_name->binary-name mapping.
// A target only qualifies if versions carries a non-empty entry for it.
// cliBinaryTargets apps are app_type "cli", so they have no image to
// finalize-app-retag and no finalizeTargets entry from the apps loop above
// (see that loop's cliBinaryTargets skip) -- this publishes them straight
// off the plan's own resolved version instead of an image-finalize
// confirmation that will never exist for these targets.
//
// The binaryUploader (a real libs/go/s3.Client, or a.S3Uploader's test
// fake -- see binaryUploaderFor) is constructed lazily, on the first
// qualifying target, so a release with none never requires S3 credentials
// configured at all. If construction fails, every remaining qualifying
// target in this batch is recorded as failed too (each gets its own
// finalizeTargets/failures entry, matching this file's "a single bad
// target must not stop others" policy -- see FinalizePublish's doc
// comment) rather than aborting the loop outright.
//
// A qualifying target whose cli-binaries artifact is missing, or whose
// binary subdirectory (cliBinariesDir/<binaryName>) is missing or empty,
// is a per-target failure: this OVERWRITES that target's finalizeTargets
// entry from success to Failed and returns its detail in the returned
// slice, exactly the same shape every other failure path in this file
// uses -- FR9's guarantee only holds if a confirmed-version target's
// publish is never silently skipped.
//
// A successful S3 upload alone does not make the target promotable: it
// still must be recorded in the App Registry (BeginPublish then
// RecordArtifact, transitioning the AllocateVersion-created "allocated"
// row through "publishing" to "published") the same way finalize-app/
// finalize-chart's ExecuteRelease already does for image/chart targets --
// see recordCLIBinaryArtifact's doc comment. Without this, a real prod
// release (targets tools-app-registry/tools-release_helper_go, v0.6.0)
// uploaded its binaries to S3 successfully but VerifyPublished
// (record.go) correctly reported "no published artifact found: not
// found" for both, since nothing had ever moved the row past "allocated"
// -- the release_run_target ended up Failed despite the underlying
// upload having genuinely worked. buildID (the App Registry build_id this
// release's plan resolved, same value the apps loop above passes to
// finalize-app/finalize-chart) gates this exactly like that existing
// path: an empty buildID means BeginPublish/RecordArtifact are simply
// skipped (planBuildID's doc comment), not treated as a target failure --
// registry recording was already best-effort/tolerated for images with no
// buildID, so binaries are no stricter.
func (a *Activities) publishCLIBinaries(ctx context.Context, apps []string, versions map[string]string, finalizeTargets map[string]FinalizeTargetOutcome, cliBinariesDir string, haveCLIBinaries bool, buildID string) []string {
	var failures []string
	var uploader binaryUploader

	for _, fullName := range apps {
		binaryName, ok := cliBinaryTargets[fullName]
		if !ok {
			continue
		}
		key := repository.TargetKey(repository.ArtifactKindImage, fullName)
		// cliBinaryTargets apps skip the image finalize-app flow entirely
		// (see the apps loop above), so there is no finalizeTargets
		// EffectiveVersion to gate on here -- publish straight off the
		// plan's own resolved version instead.
		version := versions[key]
		if version == "" {
			continue
		}

		if uploader == nil {
			u, err := a.binaryUploaderFor(ctx)
			if err != nil {
				detail := fmt.Sprintf("%s: construct release-tools S3 client: %v", fullName, err)
				finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
				failures = append(failures, detail)
				workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
				continue
			}
			uploader = u
		}

		binDir := filepath.Join(cliBinariesDir, binaryName)
		entries, direrr := os.ReadDir(binDir)
		if !haveCLIBinaries || direrr != nil || len(entries) == 0 {
			detail := fmt.Sprintf("%s: no cli-binaries artifact entry for %q (expected dir %s)", fullName, binaryName, binDir)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
			failures = append(failures, detail)
			workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
			continue
		}

		var uploadErr error
		var checksumsData []byte
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			// Only the platform binaries (<binaryName>-<os>-<arch>) and
			// the checksum manifest (checksums.txt) are part of the S3 key
			// convention -- package_assets.go's generateChecksumFiles also
			// writes a SHA256SUMS file into the same directory, which this
			// task's scope does not publish (FR16 only asks for
			// checksums.txt parity).
			fileName := e.Name()
			if fileName != "checksums.txt" && !strings.HasPrefix(fileName, binaryName+"-") {
				continue
			}
			data, rerr := os.ReadFile(filepath.Join(binDir, fileName))
			if rerr != nil {
				uploadErr = fmt.Errorf("read %s: %w", fileName, rerr)
				break
			}
			if fileName == "checksums.txt" {
				checksumsData = data
			}
			s3Key := cliBinaryS3Key(binaryName, version, fileName)
			if _, uerr := uploader.Upload(ctx, s3Key, data, nil); uerr != nil {
				uploadErr = fmt.Errorf("upload %s: %w", s3Key, uerr)
				break
			}
		}
		if uploadErr != nil {
			detail := fmt.Sprintf("%s: publish cli binaries: %v", fullName, uploadErr)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
			failures = append(failures, detail)
			workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
			continue
		}

		if buildID != "" {
			if err := a.recordCLIBinaryArtifact(ctx, fullName, version, buildID, checksumsData); err != nil {
				detail := fmt.Sprintf("%s: record published artifact in App Registry: %v", fullName, err)
				finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: detail}
				failures = append(failures, detail)
				workerLog.Warn("finalize target failed", "target", fullName, "detail", detail)
				continue
			}
		}
		workerLog.Info("cli binary published", "target", fullName, "version", version)
		finalizeTargets[key] = FinalizeTargetOutcome{EffectiveVersion: version}
	}

	return failures
}

// recordCLIBinaryArtifact transitions fullName's AllocateVersion-created
// "allocated" artifact row through "publishing" to "published" (the same
// BeginPublish-then-RecordArtifact sequence ExecuteRelease uses for
// image/chart targets -- tools/release_helper_go/cmd/releaser.go -- just
// called in-process against a.Registry instead of over gRPC, matching
// this package's established direct-Postgres pattern for release_run
// bookkeeping (record.go's package doc comment)). Digest is a SHA-256 of
// the platform binaries' checksums.txt -- the closest available
// content-identity signal in this flow, since (unlike ExecuteRelease's
// BinaryReleaser, which hashes a locally bazel-built file) this activity
// never builds anything: it only downloads a pre-built, already-uploaded
// cli-binaries GHA artifact.
func (a *Activities) recordCLIBinaryArtifact(ctx context.Context, fullName, version, buildID string, checksumsData []byte) error {
	if len(checksumsData) == 0 {
		return fmt.Errorf("no checksums.txt content to derive a digest from")
	}
	sum := sha256.Sum256(checksumsData)
	digest := "sha256:" + hex.EncodeToString(sum[:])

	app, err := a.Registry.Apps().GetAppByFullName(ctx, fullName)
	if err != nil {
		return fmt.Errorf("look up app %q: %w", fullName, err)
	}
	repositoryHint := fmt.Sprintf("github.com/%s/%s", a.GitHub.Config.Owner, a.GitHub.Config.Repo)

	if _, err := a.Registry.Artifacts().BeginPublish(ctx, repository.ArtifactKindBinary, app.AppID, version, buildID, repositoryHint, repository.VersionSourceRegistry); err != nil {
		return fmt.Errorf("begin publish: %w", err)
	}

	if _, _, err := a.Registry.Artifacts().RecordArtifact(ctx, repository.Artifact{
		Kind:        repository.ArtifactKindBinary,
		AppID:       app.AppID,
		Repository:  repositoryHint,
		Version:     version,
		Digest:      digest,
		BuildID:     buildID,
		PublishedAt: time.Now(),
	}, nil); err != nil {
		return fmt.Errorf("record artifact: %w", err)
	}
	return nil
}

// binaryUploaderFor returns the binaryUploader FinalizePublish's CLI-binary
// publish step should use: a.S3Uploader if the caller (a test, per
// Activities.S3Uploader's doc comment) already substituted one, otherwise a
// real libs/go/s3.Client constructed from a.ReleaseToolsS3*. Constructed
// once per FinalizePublish call, not per file (per issue #984's
// Implementation scope) -- callers must only invoke this when the batch
// actually has at least one cliBinaryTargets entry, so a release with no
// release_helper_go/app-registry target never requires S3 credentials to be
// configured at all.
func (a *Activities) binaryUploaderFor(ctx context.Context) (binaryUploader, error) {
	if a.S3Uploader != nil {
		return a.S3Uploader, nil
	}
	return s3.NewClient(ctx, s3.Config{
		Bucket:    a.ReleaseToolsS3Bucket,
		Endpoint:  a.ReleaseToolsS3Endpoint,
		Region:    a.ReleaseToolsS3Region,
		AccessKey: a.ReleaseToolsS3AccessKey,
		SecretKey: a.ReleaseToolsS3SecretKey,
	})
}

// cliBinaryS3Key builds the S3 key for one platform file of a CLI binary,
// per tools/app_registry/ENV.md's "CLI binary S3" convention --
// "<binary>/<version>/<binary>-<os>-<arch>", e.g.
// "release_helper_go/v1.2.3/release_helper_go-linux-amd64". version must be
// the target's FinalizeTargetOutcome.EffectiveVersion (never the plan-time
// requested version -- see finalizeCLIResult's doc comment). fileName is the
// bare file name as #981's package-assets/checksum output produces it
// (either "<binary>-<os>-<arch>" or "checksums.txt").
func cliBinaryS3Key(binaryName, version, fileName string) string {
	return fmt.Sprintf("%s/%s/%s", binaryName, version, fileName)
}

// planBuildID extracts the App Registry build_id field from
// ResolvedPlan.RawJSON (see plan.go's ResolvePlan -- the same field
// release-v2.yml's plan-release job surfaces as its `build-id` output).
// Returns "" (not an error) if rawJSON is empty or has no build_id -- a
// missing build_id is tolerated the same way release-app/release-charts
// already treat an empty --build-id (BeginPublish/RecordArtifact are
// simply skipped -- see ExecuteRelease).
func planBuildID(rawJSON []byte) string {
	if len(rawJSON) == 0 {
		return ""
	}
	var parsed resolvedPlanBuildID
	if err := json.Unmarshal(rawJSON, &parsed); err != nil {
		return ""
	}
	return parsed.BuildID
}
