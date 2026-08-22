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
// Unlike ResolvePlan, this activity's shell-outs pass `--create-git-tag`
// (finalize-app/finalize-chart), which runs `git tag`+`git push` -- a real
// authenticated git remote, not just a scratch directory. When
// a.WorkspaceRoot is unset, FinalizePublish performs its own shallow,
// authenticated clone of this monorepo per invocation (cloneWorkspace,
// below) into a fresh temp directory, reusing the exact GitHub App
// installation-token pattern DispatchBuild's dispatcher already has
// (a.GitHub.Config's writeback.GitHubAppConfig + Owner/Repo -- see
// github.go's package doc comment's "reuse ... rather than inventing a
// second auth mechanism" precedent) rather than requiring an operator to
// pre-provision a persistent checkout. A fresh clone per invocation also
// means ResolvePlan's previously-documented git-tag-race concurrency
// caveat (a single shared WorkspaceRoot directory racing across concurrent
// ReleaseWorkflow executions) does not apply here: each FinalizePublish
// call gets its own directory. a.WorkspaceRoot remains a supported
// override (e.g. for tests, or an operator who does want a persistent
// checkout) -- when set, it is used as-is and no clone is performed.
// `helm` must still be on PATH wherever this process runs (neither
// finalize-app nor finalize-chart shells out to bazel).
package release

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// Workflow artifact names the merged release-trigger GHA job uploads and
// this activity looks for -- must match release-v2.yml's
// actions/upload-artifact `name:` fields exactly.
const (
	buildManifestArtifactName = "build-manifest"
	chartSourcesArtifactName  = "chart-sources"
)

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
// could possibly have finalized. Per-target failures are aggregated into
// FinalizeResult.Detail and left for VerifyPublished to surface precisely
// (a target whose finalize-app/finalize-chart failed never reached
// RecordArtifact, so VerifyPublished's GetArtifact(LatestPublished) check
// correctly reports it unpublished).
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

	// workspaceDir is where finalize-app/finalize-chart run `git
	// tag`+`git push` from -- see package doc comment. a.WorkspaceRoot
	// stays a supported override; when unset, clone one ourselves.
	workspaceDir := a.WorkspaceRoot
	if workspaceDir == "" {
		cloneDir, cerr := os.MkdirTemp("", "release-finalize-workspace-*")
		if cerr != nil {
			return FinalizeResult{}, fmt.Errorf("finalize publish: create scratch workspace: %w", cerr)
		}
		defer os.RemoveAll(cloneDir) //nolint:errcheck
		clone := a.cloneWorkspaceFn
		if clone == nil {
			clone = a.cloneWorkspace
		}
		if cerr := clone(ctx, cloneDir); cerr != nil {
			return FinalizeResult{}, fmt.Errorf("finalize publish: %w", cerr)
		}
		workspaceDir = cloneDir
	}

	tmpDir, err := os.MkdirTemp("", "release-finalize-")
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: %w", err)
	}
	defer os.RemoveAll(tmpDir) //nolint:errcheck

	appsDir := filepath.Join(tmpDir, "apps")
	chartsDir := filepath.Join(tmpDir, "charts")

	artifactList, err := a.GitHub.ListRunArtifacts(ctx, ref)
	if err != nil {
		return FinalizeResult{}, fmt.Errorf("finalize publish: %w", err)
	}
	var haveManifest, haveCharts bool
	for _, art := range artifactList {
		var dest string
		switch art.Name {
		case buildManifestArtifactName:
			dest = appsDir
		case chartSourcesArtifactName:
			dest = chartsDir
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
		if art.Name == buildManifestArtifactName {
			haveManifest = true
		} else {
			haveCharts = true
		}
	}
	if len(apps) > 0 && !haveManifest {
		return FinalizeResult{}, fmt.Errorf("finalize publish: run %s produced no %q artifact for %d app target(s)", ref.RunID, buildManifestArtifactName, len(apps))
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

	buildID := planBuildID(plan.RawJSON)

	var ghcrToken string
	if len(apps) > 0 {
		ghcrToken, err = a.GitHub.token(ctx)
		if err != nil {
			return FinalizeResult{}, fmt.Errorf("finalize publish: mint registry token: %w", err)
		}
	}

	binary := a.PlanBinaryPath
	if binary == "" {
		binary = "release_helper_go"
	}

	var failures []string
	appVersions := make(map[string]string, len(apps))
	appDigests := make(map[string]string, len(apps))
	finalizeResultsDir := filepath.Join(tmpDir, "finalize-results")
	// finalizeTargets carries FinalizePublish's own precise per-target
	// outcome back to the caller (workflow.go's ReleaseWorkflow) -- see
	// FinalizeResult.Targets/FinalizeTargetOutcome's doc comments. Every
	// failures = append(...) call site below has a matching
	// finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, ...} so
	// the two never drift apart.
	finalizeTargets := make(map[string]FinalizeTargetOutcome, len(apps)+len(charts))

	for _, fullName := range apps {
		key := repository.TargetKey(repository.ArtifactKindImage, fullName)
		version := plan.Versions[key]
		m, ok := appManifests[fullName]
		if !ok {
			msg := fmt.Sprintf("%s: no build-manifest entry in run %s", fullName, ref.RunID)
			failures = append(failures, msg)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: msg}
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
			"--create-git-tag",
			"--output-dir", finalizeResultsDir,
		}
		if buildID != "" {
			args = append(args, "--build-id", buildID)
		}
		if _, err := runReleaseHelper(ctx, binary, workspaceDir, []string{"GHCR_TOKEN=" + ghcrToken}, args...); err != nil {
			msg := fmt.Sprintf("%s: finalize-app: %v", fullName, err)
			failures = append(failures, msg)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: msg}
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
			msg := fmt.Sprintf("%s: read finalize-app result: %v", fullName, rerr)
			failures = append(failures, msg)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: msg}
			continue
		}
		appVersions[fullName] = res.EffectiveVersion
		finalizeTargets[key] = FinalizeTargetOutcome{EffectiveVersion: res.EffectiveVersion}
	}

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
			msg := fmt.Sprintf("%s: no chart-sources manifest entry in run %s", fullName, ref.RunID)
			failures = append(failures, msg)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: msg}
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
			"--create-git-tag",
			"--output-dir", chartFinalizeResultsDir,
		}
		if buildID != "" {
			args = append(args, "--build-id", buildID)
		}
		if _, err := runReleaseHelper(ctx, binary, workspaceDir, chartEnv, args...); err != nil {
			msg := fmt.Sprintf("%s: finalize-chart: %v", fullName, err)
			failures = append(failures, msg)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: msg}
			continue
		}

		// Same EffectiveVersion-vs-plan-time-version divergence as apps
		// above (ExecuteRelease's no-op-rebuild path) -- captured here so
		// VerifyPublished can compare against it instead of the unpublished
		// plan-time version.
		res, rerr := readFinalizeResult(chartFinalizeResultsDir, c.Domain, c.ChartName)
		if rerr != nil {
			msg := fmt.Sprintf("%s: read finalize-chart result: %v", fullName, rerr)
			failures = append(failures, msg)
			finalizeTargets[key] = FinalizeTargetOutcome{Failed: true, Detail: msg}
			continue
		}
		finalizeTargets[key] = FinalizeTargetOutcome{EffectiveVersion: res.EffectiveVersion}
	}

	if len(failures) > 0 {
		return FinalizeResult{Succeeded: false, Detail: strings.Join(failures, "; "), Targets: finalizeTargets}, nil
	}
	return FinalizeResult{Succeeded: true, Targets: finalizeTargets}, nil
}

// cloneWorkspace performs a shallow, authenticated clone of this monorepo
// into dir, so finalize-app/finalize-chart's --create-git-tag shell-outs
// have a real git remote to push to -- see package doc comment. Reuses
// a.GitHub.Config's GitHub App credentials and Owner/Repo (already
// required for DispatchBuild) and writeback.MintInstallationToken's
// installation-token flow, the same "x-access-token:<token>" HTTPS
// credential shape writeback/gitops.go's remoteURL uses for gitops pushes
// -- one auth mechanism, not two. The token is redacted from any returned
// error.
func (a *Activities) cloneWorkspace(ctx context.Context, dir string) error {
	token, err := a.GitHub.token(ctx)
	if err != nil {
		return fmt.Errorf("mint clone token: %w", err)
	}
	ref := a.GitHub.Config.Ref
	if ref == "" {
		ref = "main"
	}
	url := "https://x-access-token:" + token + "@github.com/" + a.GitHub.Config.Owner + "/" + a.GitHub.Config.Repo + ".git"

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--branch", ref, url, dir)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if runErr := cmd.Run(); runErr != nil {
		msg := strings.ReplaceAll(out.String(), token, "REDACTED")
		return fmt.Errorf("git clone: %w: %s", runErr, strings.TrimSpace(msg))
	}
	return nil
}

// runReleaseHelper invokes binary (release_helper_go) from workspaceRoot
// with extraEnv appended to the current process's environment -- mirroring
// plan.go's ResolvePlan shell-out (same binary, same Dir convention).
func runReleaseHelper(ctx context.Context, binary, workspaceRoot string, extraEnv []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workspaceRoot
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", binary, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
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
