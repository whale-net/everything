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
// shell-out to release_helper_go rather than an in-process library call,
// and inherits the identical operational precondition: a.WorkspaceRoot
// must be a real directory on disk (finalize-app/finalize-chart need
// `helm`/`git` on PATH there; unlike ResolvePlan they do NOT need
// `bazel` or a monorepo checkout, since neither shells out to bazel).
// FinalizePublish also inherits ResolvePlan's already-accepted concurrency
// caveat: if a.WorkspaceRoot is a single shared directory and Temporal runs
// two ReleaseWorkflow executions' activities concurrently, their `git tag`
// invocations race in the same working tree. Not solved here -- the same
// gap plan.go's shell-out already carries, not a new one this task
// introduces.
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

// resolvedPlanBuildID is the subset of `release_helper_go plan
// --format=json`'s output FinalizePublish needs beyond what ResolvedPlan
// already carries -- see plan.go's identical planCLIResult pattern.
type resolvedPlanBuildID struct {
	BuildID string `json:"build_id"`
}

// FinalizeResult is FinalizePublish's outcome. Unlike PollBuild's
// BuildStatus (a single pass/fail for the whole GHA run), FinalizePublish
// deliberately does not fail the whole activity when only some targets
// fail to finalize -- see the per-target failure handling below and this
// function's doc comment.
type FinalizeResult struct {
	// Succeeded is true only if every target in the batch finalized
	// without error.
	Succeeded bool
	// Detail summarizes any per-target failures (empty when Succeeded).
	// VerifyPublished (the next workflow step) is still the authoritative
	// per-target signal -- Detail is for operator-facing context only.
	Detail string
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
	if a.WorkspaceRoot == "" {
		return FinalizeResult{}, fmt.Errorf("finalize publish: WorkspaceRoot not configured -- finalize-app/finalize-chart shell out from it, same operational precondition as ResolvePlan (see plan.go's package doc comment)")
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

	for _, fullName := range apps {
		key := repository.TargetKey(repository.ArtifactKindImage, fullName)
		version := plan.Versions[key]
		m, ok := appManifests[fullName]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: no build-manifest entry in run %s", fullName, ref.RunID))
			continue
		}
		appVersions[fullName] = version
		appDigests[fullName] = m.Digest

		args := []string{
			"finalize-app",
			"--domain", m.Domain,
			"--app", m.App,
			"--version", version,
			"--repository", m.Repository,
			"--digest", m.Digest,
			"--create-git-tag",
		}
		if buildID != "" {
			args = append(args, "--build-id", buildID)
		}
		if _, err := runReleaseHelper(ctx, binary, a.WorkspaceRoot, []string{"GHCR_TOKEN=" + ghcrToken}, args...); err != nil {
			failures = append(failures, fmt.Sprintf("%s: finalize-app: %v", fullName, err))
		}
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

	for _, fullName := range charts {
		key := repository.TargetKey(repository.ArtifactKindChart, fullName)
		version := plan.Versions[key]
		c, ok := chartManifests[fullName]
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: no chart-sources manifest entry in run %s", fullName, ref.RunID))
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
		}
		if buildID != "" {
			args = append(args, "--build-id", buildID)
		}
		if _, err := runReleaseHelper(ctx, binary, a.WorkspaceRoot, chartEnv, args...); err != nil {
			failures = append(failures, fmt.Sprintf("%s: finalize-chart: %v", fullName, err))
		}
	}

	if len(failures) > 0 {
		return FinalizeResult{Succeeded: false, Detail: strings.Join(failures, "; ")}, nil
	}
	return FinalizeResult{Succeeded: true}, nil
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
