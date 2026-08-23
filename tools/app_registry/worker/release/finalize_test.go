package release

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/whale-net/everything/libs/go/s3"
)

// zipDir builds a zip archive (the format GitHub's artifact download
// endpoint returns) from a map of relative-path -> file content, mirroring
// what actions/upload-artifact -> actions/download-artifact round trip
// produces.
func zipDir(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := w.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return buf.Bytes()
}

func TestUnzipTo_ExtractsNestedFiles(t *testing.T) {
	dest := t.TempDir()
	data := zipDir(t, map[string]string{
		"demo-widget.json":            `{"full_name":"demo-widget"}`,
		"helm-demo/Chart.yaml":        "name: demo\nversion: 0.0.0\n",
		"helm-demo/templates/dep.yml": "kind: Deployment\n",
	})

	require.NoError(t, unzipTo(data, dest))

	got, err := os.ReadFile(filepath.Join(dest, "demo-widget.json"))
	require.NoError(t, err)
	require.Equal(t, `{"full_name":"demo-widget"}`, string(got))

	got, err = os.ReadFile(filepath.Join(dest, "helm-demo", "Chart.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(got), "version: 0.0.0")
}

func TestUnzipTo_RejectsPathTraversal(t *testing.T) {
	dest := t.TempDir()
	data := zipDir(t, map[string]string{"../escape.txt": "nope"})
	err := unzipTo(data, dest)
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes destination directory")
}

func TestLoadAppManifests_ReadsEveryJSONFileKeyedByFullName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "demo-widget.json"), []byte(`{
		"domain": "demo", "app": "widget", "full_name": "demo-widget",
		"repository": "ghcr.io/whale-net/demo-widget", "digest": "sha256:aaa"
	}`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "demo-gadget.json"), []byte(`{
		"domain": "demo", "app": "gadget", "full_name": "demo-gadget",
		"repository": "ghcr.io/whale-net/demo-gadget", "digest": "sha256:bbb"
	}`), 0644))
	// A non-.json file must be ignored, not fail the whole load.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("ignore me"), 0644))

	got, err := loadAppManifests(dir)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "sha256:aaa", got["demo-widget"].Digest)
	require.Equal(t, "ghcr.io/whale-net/demo-gadget", got["demo-gadget"].Repository)
}

func TestLoadAppManifests_MissingDirIsNotAnError(t *testing.T) {
	got, err := loadAppManifests(filepath.Join(t.TempDir(), "does-not-exist"))
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestLoadChartManifest_ReadsManifestJSONKeyedByFullName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`[
		{"chart_name": "helm-demo", "domain": "demo", "full_name": "demo-demo", "chart_dir": "helm-demo"}
	]`), 0644))

	got, err := loadChartManifest(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "helm-demo", got["demo-demo"].ChartName)
	require.Equal(t, "helm-demo", got["demo-demo"].ChartDir)
}

func TestLoadChartManifest_MissingDirIsNotAnError(t *testing.T) {
	got, err := loadChartManifest(filepath.Join(t.TempDir(), "does-not-exist"))
	require.NoError(t, err)
	require.Empty(t, got)
}

// fakeFinalizeAppScript is a minimal stand-in for release_helper_go
// finalize-app: it doesn't retag or publish anything, but it does write the
// --output-dir result file (echoing --version back as effective_version)
// FinalizePublish now requires reading back after every finalize-app call
// -- see finalize.go's readFinalizeResult. Good enough for tests that
// exercise FinalizePublish's own orchestration (workspace handling,
// artifact download/parsing), not finalize-app's own no-op-detection
// logic (covered by finalize_app_test.go/cmd_test instead).
func fakeFinalizeAppScript() []byte {
	return []byte(`#!/bin/sh
domain=""; app=""; version=""; outdir=""
while [ $# -gt 0 ]; do
  case "$1" in
    --domain) domain="$2"; shift 2;;
    --app) app="$2"; shift 2;;
    --version) version="$2"; shift 2;;
    --output-dir) outdir="$2"; shift 2;;
    *) shift;;
  esac
done
if [ -n "$outdir" ]; then
  mkdir -p "$outdir"
  printf '{"effective_version":"%s"}' "$version" > "$outdir/$domain-$app.json"
fi
exit 0
`)
}

func TestPlanBuildID_ExtractsFieldOrEmpty(t *testing.T) {
	require.Equal(t, "", planBuildID(nil))
	require.Equal(t, "", planBuildID([]byte("not json")))
	require.Equal(t, "", planBuildID([]byte(`{"version":"v1.0.0"}`)))
	require.Equal(t, "build-123", planBuildID([]byte(`{"build_id":"build-123","version":"v1.0.0"}`)))
}

// TestActivities_FinalizePublish_WorkspaceRootUnset_Succeeds proves
// FinalizePublish does not require a.WorkspaceRoot at all -- neither
// finalize-app nor finalize-chart needs a real git checkout any more (see
// finalize.go's package doc comment), so an unset a.WorkspaceRoot must not
// hard-fail or attempt any clone. The fake release_helper_go binary is a
// no-op (exit 0): this test is about FinalizePublish's own orchestration,
// not finalize-app/finalize-chart's logic.
func TestActivities_FinalizePublish_WorkspaceRootUnset_Succeeds(t *testing.T) {
	appManifest := `{"domain":"demo","app":"widget","full_name":"demo-widget","repository":"ghcr.io/whale-net/demo-widget","digest":"sha256:aaa"}`
	buildManifestZip := zipDir(t, map[string]string{"demo-widget.json": appManifest})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":7,"name":"build-manifest","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/7/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildManifestZip)
	})

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "fake-release-helper-go")
	require.NoError(t, os.WriteFile(bin, fakeFinalizeAppScript(), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		GHCRToken:      "test-ghcr-token",
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"image:demo-widget": "v1.2.3"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err, "WorkspaceRoot unset must not hard-fail -- FinalizePublish needs no git checkout at all")
	require.True(t, result.Succeeded, "detail: %s", result.Detail)
}

// TestActivities_FinalizePublish_NoGHCRToken_FailsBatch is the regression
// test for issue #996: a GitHub App installation token cannot write to
// organization-owned GHCR packages outside a GitHub Actions run, so
// FinalizePublish no longer mints one via a.GitHub.token(ctx) for the
// finalize-app retag -- it requires the static a.GHCRToken (RELEASE_GHCR_TOKEN)
// instead. An unset a.GHCRToken must fail fast with a clear error, for any
// batch with app targets, rather than attempting (and failing at the
// registry with a confusing DENIED) an App-minted token.
func TestActivities_FinalizePublish_NoGHCRToken_FailsBatch(t *testing.T) {
	appManifest := `{"domain":"demo","app":"widget","full_name":"demo-widget","repository":"ghcr.io/whale-net/demo-widget","digest":"sha256:aaa"}`
	buildManifestZip := zipDir(t, map[string]string{"demo-widget.json": appManifest})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":7,"name":"build-manifest","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/7/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildManifestZip)
	})

	a := &Activities{
		GitHub: newTestDispatcher(t, mux),
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"image:demo-widget": "v1.2.3"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	_, err := a.FinalizePublish(context.Background(), plan, ref)
	require.ErrorContains(t, err, "GHCR token not configured")
}

// TestActivities_FinalizePublish_NoOpApp_ChartPinsEffectiveVersion is the
// regression test for the bug found alongside release.yml's
// release-helm-charts (PR #961): a member app whose finalize-app call is a
// no-op rebuild (identical digest to an already-published version) reuses
// that older version instead of publishing under plan.Versions' plan-time
// version -- see finalizeCLIResult's doc comment. finalize-chart's
// --app-versions must reflect that actual effective version, not the
// unpublished plan-time one, or chart composition fails the same
// "chart pins N unpublished app(s)" hermeticity check
// https://github.com/whale-net/everything/actions/runs/32581668230/job/97053015545
// hit in release.yml.
func TestActivities_FinalizePublish_NoOpApp_ChartPinsEffectiveVersion(t *testing.T) {
	appManifest := `{"domain":"demo","app":"widget","full_name":"demo-widget","repository":"ghcr.io/whale-net/demo-widget","digest":"sha256:aaa"}`
	buildManifestZip := zipDir(t, map[string]string{"demo-widget.json": appManifest})
	chartManifestJSON := `[{"chart_name":"helm-demo","domain":"demo","full_name":"demo-demo","chart_dir":"helm-demo"}]`
	chartSourcesZip := zipDir(t, map[string]string{"manifest.json": chartManifestJSON})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":7,"name":"build-manifest","expired":false},{"id":8,"name":"chart-sources","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/7/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildManifestZip)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/8/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chartSourcesZip)
	})

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "fake-release-helper-go")
	appArgsFile := filepath.Join(binDir, "app-args.txt")
	chartArgsFile := filepath.Join(binDir, "chart-args.txt")
	// finalize-app always reports effective_version "v1.0.0" -- simulating
	// a no-op rebuild reusing an older published version regardless of the
	// plan-time --version ("v1.2.3") this test's plan requests. finalize-app
	// and finalize-chart each record their full argument list (appArgsFile/
	// chartArgsFile) so this test can also assert neither is invoked with
	// --create-git-tag -- see the regression assertions below, guarding
	// against that flag (and the ephemeral-clone plumbing it required in
	// finalize.go) being reintroduced (issue #982, FR17-FR19 of #979).
	script := fmt.Sprintf(`#!/bin/sh
cmd="$1"; shift
if [ "$cmd" = "finalize-app" ]; then
  for a in "$@"; do printf '%%s\n' "$a"; done > %q
  domain=""; app=""; outdir=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --domain) domain="$2"; shift 2;;
      --app) app="$2"; shift 2;;
      --output-dir) outdir="$2"; shift 2;;
      *) shift;;
    esac
  done
  mkdir -p "$outdir"
  printf '{"effective_version":"v1.0.0"}' > "$outdir/$domain-$app.json"
  exit 0
elif [ "$cmd" = "finalize-chart" ]; then
  for a in "$@"; do printf '%%s\n' "$a"; done > %q
  domain=""; chart=""; version=""; outdir=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --domain) domain="$2"; shift 2;;
      --chart) chart="$2"; shift 2;;
      --version) version="$2"; shift 2;;
      --output-dir) outdir="$2"; shift 2;;
      *) shift;;
    esac
  done
  if [ -n "$outdir" ]; then
    mkdir -p "$outdir"
    printf '{"effective_version":"%%s"}' "$version" > "$outdir/$domain-$chart.json"
  fi
  exit 0
fi
exit 0
`, appArgsFile, chartArgsFile)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		GHCRToken:      "test-ghcr-token",
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions: map[string]string{
			"image:demo-widget": "v1.2.3",
			"chart:demo-demo":   "v2.0.0",
		},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err)
	require.True(t, result.Succeeded, "detail: %s", result.Detail)

	appData, err := os.ReadFile(appArgsFile)
	require.NoError(t, err)
	require.NotContains(t, string(appData), "--create-git-tag", "finalize-app must not be invoked with --create-git-tag -- FinalizePublish's version-of-record lives in App Registry, not a git tag (issue #982)")

	data, err := os.ReadFile(chartArgsFile)
	require.NoError(t, err)
	joined := string(data)
	require.Contains(t, joined, `"demo-widget":"v1.0.0"`, "chart's --app-versions must pin the app's actual effective (published) version")
	require.NotContains(t, joined, `"demo-widget":"v1.2.3"`, "chart's --app-versions must not pin the unpublished plan-time version")
	require.NotContains(t, joined, "--create-git-tag", "finalize-chart must not be invoked with --create-git-tag -- FinalizePublish's version-of-record lives in App Registry, not a git tag (issue #982)")

	// FinalizeResult.Targets must reflect the app's actual reused
	// EffectiveVersion ("v1.0.0"), not the unpublished plan-time version
	// ("v1.2.3") -- this is what lets VerifyPublished (record.go) compare
	// correctly instead of misreporting a legitimate no-op rebuild as a
	// failure (issue #973's PR #976 first-pass gap).
	require.Equal(t, FinalizeTargetOutcome{EffectiveVersion: "v1.0.0"}, result.Targets["image:demo-widget"])
	require.Equal(t, FinalizeTargetOutcome{EffectiveVersion: "v2.0.0"}, result.Targets["chart:demo-demo"])
}

// TestActivities_FinalizePublish_MissingFinalizeAppResult_NoTargetEntry is
// this PR's Finding 1 regression test: a target whose finalize-app
// subprocess exited 0 (the actual retag/publish/RecordArtifact work
// succeeded) but never wrote a readable --output-dir result file (e.g. a
// disk-full/permission error writing the bookkeeping sidecar, or an older
// release_helper_go binary that doesn't support --output-dir yet) must NOT
// be reported as a per-target failure here. A bookkeeping-only write
// failure has no bearing on whether the actual publish succeeded, and must
// not have the power to falsely fail a release that actually succeeded --
// this target is left with no entry in FinalizeResult.Targets at all, so
// workflow.go's ReleaseWorkflow falls through to VerifyPublished's real
// presence/version check (record.go's defensive fallback) to decide its
// fate from actual App Registry state, instead of a guess made here. (Prior
// to this fix, this exact scenario populated Failed: true here, which
// bypassed VerifyPublished entirely and always failed the release --
// wrongly, since the publish had already succeeded.)
func TestActivities_FinalizePublish_MissingFinalizeAppResult_NoTargetEntry(t *testing.T) {
	appManifest := `{"domain":"demo","app":"widget","full_name":"demo-widget","repository":"ghcr.io/whale-net/demo-widget","digest":"sha256:aaa"}`
	buildManifestZip := zipDir(t, map[string]string{"demo-widget.json": appManifest})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":7,"name":"build-manifest","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/7/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildManifestZip)
	})

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "fake-release-helper-go")
	// finalize-app exits 0 (publish succeeded) but never writes an
	// --output-dir result file -- simulating the bookkeeping-sidecar
	// write failure this test guards against (or an older CLI binary that
	// predates --output-dir support).
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		GHCRToken:      "test-ghcr-token",
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"image:demo-widget": "v1.2.3"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err)
	require.True(t, result.Succeeded, "a missing bookkeeping result file must not be reported as a finalize failure, detail: %s", result.Detail)
	require.Empty(t, result.Detail)

	_, ok := result.Targets["image:demo-widget"]
	require.False(t, ok, "FinalizeResult.Targets must have NO entry for demo-widget -- VerifyPublished's real check must decide its fate, not a guess made here")
}

// TestActivities_FinalizePublish_MissingFinalizeAppResult_ChartStillGetsAppVersionsEntry
// is this PR's Finding 1 regression test: the same bookkeeping-write-
// failure scenario as
// TestActivities_FinalizePublish_MissingFinalizeAppResult_NoTargetEntry
// above, but with the affected app composed into a chart in the same
// batch. Before this fix, `continue` ran before appVersions[fullName] was
// ever set, so the app was silently missing from the JSON marshaled into
// finalize-chart's --app-versions -- not merely absent from
// FinalizeResult.Targets (correct), but also invisible to
// finalize_chart.go's hermeticity-pin check (which only iterates keys
// present in AppVersions) and build_helm.go's packageChartWithVersion
// (which only rewrites values.yaml imageTag entries for keys present in
// the map). The chart would then ship whatever imageTag was baked in at
// build time, with the release recorded fully Succeeded and no signal of
// the stale pin. The fix falls back to the plan-time requested version for
// appVersions in this case -- safe because ExecuteRelease's Build step
// (the real crane.Tag registry write) already ran unconditionally before
// any no-op-rebuild bookkeeping decision, so that version tag already
// points at the right digest regardless (see finalize.go's apps loop for
// the full reasoning).
func TestActivities_FinalizePublish_MissingFinalizeAppResult_ChartStillGetsAppVersionsEntry(t *testing.T) {
	appManifest := `{"domain":"demo","app":"widget","full_name":"demo-widget","repository":"ghcr.io/whale-net/demo-widget","digest":"sha256:aaa"}`
	buildManifestZip := zipDir(t, map[string]string{"demo-widget.json": appManifest})
	chartManifestJSON := `[{"chart_name":"helm-demo","domain":"demo","full_name":"demo-demo","chart_dir":"helm-demo"}]`
	chartSourcesZip := zipDir(t, map[string]string{"manifest.json": chartManifestJSON})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":7,"name":"build-manifest","expired":false},{"id":8,"name":"chart-sources","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/7/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildManifestZip)
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/8/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chartSourcesZip)
	})

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "fake-release-helper-go")
	chartArgsFile := filepath.Join(binDir, "chart-args.txt")
	// finalize-app exits 0 (publish succeeded) but never writes an
	// --output-dir result file -- the same bookkeeping-write failure as
	// TestActivities_FinalizePublish_MissingFinalizeAppResult_NoTargetEntry
	// above. finalize-chart records its full argument list (including
	// --app-versions) to chartArgsFile so the test can inspect it.
	script := fmt.Sprintf(`#!/bin/sh
cmd="$1"; shift
if [ "$cmd" = "finalize-app" ]; then
  exit 0
elif [ "$cmd" = "finalize-chart" ]; then
  for a in "$@"; do printf '%%s\n' "$a"; done > %q
  domain=""; chart=""; version=""; outdir=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --domain) domain="$2"; shift 2;;
      --chart) chart="$2"; shift 2;;
      --version) version="$2"; shift 2;;
      --output-dir) outdir="$2"; shift 2;;
      *) shift;;
    esac
  done
  if [ -n "$outdir" ]; then
    mkdir -p "$outdir"
    printf '{"effective_version":"%%s"}' "$version" > "$outdir/$domain-$chart.json"
  fi
  exit 0
fi
exit 0
`, chartArgsFile)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		GHCRToken:      "test-ghcr-token",
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions: map[string]string{
			"image:demo-widget": "v1.2.3",
			"chart:demo-demo":   "v2.0.0",
		},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err)
	require.True(t, result.Succeeded, "a missing bookkeeping result file must not be reported as a finalize failure, detail: %s", result.Detail)

	_, ok := result.Targets["image:demo-widget"]
	require.False(t, ok, "the app must still have NO entry in FinalizeResult.Targets -- VerifyPublished's real check decides its fate, not this function")

	data, err := os.ReadFile(chartArgsFile)
	require.NoError(t, err)
	joined := string(data)
	require.Contains(t, joined, `"demo-widget":"v1.2.3"`, "the composed chart's --app-versions must still pin the app to its plan-time requested version, not silently drop the key (this PR's Finding 1)")
}

// TestActivities_FinalizePublish_FinalizeAppCLIFailure_FailsThatTarget
// proves a target whose finalize-app subprocess itself exits non-zero
// (e.g. GHCR retag DENIED -- issue #973's original report) is reported as
// a per-target failure in FinalizeResult.Targets -- the signal
// workflow.go's ReleaseWorkflow now routes straight to that target's
// Failed state, without depending on VerifyPublished's indirect presence
// check at all.
func TestActivities_FinalizePublish_FinalizeAppCLIFailure_FailsThatTarget(t *testing.T) {
	appManifest := `{"domain":"demo","app":"widget","full_name":"demo-widget","repository":"ghcr.io/whale-net/demo-widget","digest":"sha256:aaa"}`
	buildManifestZip := zipDir(t, map[string]string{"demo-widget.json": appManifest})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":7,"name":"build-manifest","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/7/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(buildManifestZip)
	})

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "fake-release-helper-go")
	// Simulates a GHCR retag DENIED: finalize-app always exits non-zero.
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho 'DENIED: permission_denied' >&2\nexit 1\n"), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		GHCRToken:      "test-ghcr-token",
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"image:demo-widget": "v1.2.3"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err)
	require.False(t, result.Succeeded)

	outcome, ok := result.Targets["image:demo-widget"]
	require.True(t, ok)
	require.True(t, outcome.Failed)
	require.Contains(t, outcome.Detail, "finalize-app")
	require.Contains(t, outcome.Detail, "DENIED")
}

// TestActivities_FinalizePublish_FinalizeChartCLIFailure_FailsThatTarget is
// the chart-side equivalent of the app CLI-failure test above: a chart
// target whose finalize-chart subprocess exits non-zero must be reported
// Failed in FinalizeResult.Targets.
func TestActivities_FinalizePublish_FinalizeChartCLIFailure_FailsThatTarget(t *testing.T) {
	chartManifestJSON := `[{"chart_name":"helm-demo","domain":"demo","full_name":"demo-demo","chart_dir":"helm-demo"}]`
	chartSourcesZip := zipDir(t, map[string]string{"manifest.json": chartManifestJSON})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":8,"name":"chart-sources","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/8/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chartSourcesZip)
	})

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "fake-release-helper-go")
	script := `#!/bin/sh
cmd="$1"; shift
if [ "$cmd" = "finalize-chart" ]; then
  echo "chart upload failed" >&2
  exit 1
fi
exit 0
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		GHCRToken:      "test-ghcr-token",
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"chart:demo-demo": "v2.0.0"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err)
	require.False(t, result.Succeeded)

	outcome, ok := result.Targets["chart:demo-demo"]
	require.True(t, ok)
	require.True(t, outcome.Failed)
	require.Contains(t, outcome.Detail, "finalize-chart")
	require.Contains(t, outcome.Detail, "chart upload failed")
}

// TestActivities_FinalizePublish_NoOpChart_ReportsEffectiveVersion is the
// chart-side inverse regression test to
// TestActivities_FinalizePublish_NoOpApp_ChartPinsEffectiveVersion: a chart
// target itself (not just a member app) can hit ExecuteRelease's no-op-
// rebuild path and get reused an older EffectiveVersion than the plan-time
// --version requested. FinalizeResult.Targets must report that actual
// EffectiveVersion, still as a success (not Failed) -- this is exactly the
// case PR #976's resolved-plan-JSON comparison would have misreported as a
// version mismatch / failure (issue #973's proper-fix requirement).
func TestActivities_FinalizePublish_NoOpChart_ReportsEffectiveVersion(t *testing.T) {
	chartManifestJSON := `[{"chart_name":"helm-demo","domain":"demo","full_name":"demo-demo","chart_dir":"helm-demo"}]`
	chartSourcesZip := zipDir(t, map[string]string{"manifest.json": chartManifestJSON})

	mux := http.NewServeMux()
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"ghs_test"}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/runs/42/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artifacts":[{"id":8,"name":"chart-sources","expired":false}]}`))
	})
	mux.HandleFunc("/repos/whale-net/everything/actions/artifacts/8/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(chartSourcesZip)
	})

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "fake-release-helper-go")
	// finalize-chart always reports effective_version "v1.9.0" -- simulating
	// a no-op rebuild reusing an older published chart version regardless of
	// the plan-time --version ("v2.0.0") this test's plan requests.
	script := `#!/bin/sh
cmd="$1"; shift
if [ "$cmd" = "finalize-chart" ]; then
  domain=""; chart=""; outdir=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --domain) domain="$2"; shift 2;;
      --chart) chart="$2"; shift 2;;
      --output-dir) outdir="$2"; shift 2;;
      *) shift;;
    esac
  done
  mkdir -p "$outdir"
  printf '{"effective_version":"v1.9.0"}' > "$outdir/$domain-$chart.json"
  exit 0
fi
exit 0
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		GHCRToken:      "test-ghcr-token",
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"chart:demo-demo": "v2.0.0"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err, "detail: %s", result.Detail)
	require.True(t, result.Succeeded, "a no-op rebuild reusing an older chart version is a legitimate success, detail: %s", result.Detail)

	require.Equal(t, FinalizeTargetOutcome{EffectiveVersion: "v1.9.0"}, result.Targets["chart:demo-demo"])
}

// fakeUploader is the binaryUploader test seam (Activities.S3Uploader)
// publishCLIBinaries' tests substitute in place of a real libs/go/s3.Client,
// per issue #984's Testing scope ("check for a binaryUploader fake/test-seam
// already in place from the Scaffold phase ... and use it rather than
// hitting real S3"). Records every key/data pair it was asked to upload
// (uploads), or fails every call with err if set.
type fakeUploader struct {
	uploads map[string][]byte
	err     error
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{uploads: map[string][]byte{}}
}

func (f *fakeUploader) Upload(_ context.Context, key string, data []byte, _ *s3.UploadOptions) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.uploads[key] = data
	return key, nil
}

// refusingUploader fails the test outright if Upload is ever called -- used
// by tests proving publishCLIBinaries must not even attempt an upload (FR9/
// FR10's "never silently skip, but also never wrongly publish" guarantee
// cuts both ways: a target with no confirmed version, or an app that isn't a
// CLI-binary target at all, must not reach the uploader).
type refusingUploader struct {
	t *testing.T
}

func (r *refusingUploader) Upload(_ context.Context, key string, _ []byte, _ *s3.UploadOptions) (string, error) {
	r.t.Fatalf("Upload must not be called, got key %q", key)
	return "", nil
}

// writeCLIBinaryFiles creates cliBinariesDir/<binaryName>/ with a fake set
// of #981's build-cli-binaries output: two platform binaries, checksums.txt,
// and (mirroring package_assets.go's generateChecksumFiles) a SHA256SUMS
// file that publishCLIBinaries must NOT upload (issue #984's Implementation
// scope: "does not publish" SHA256SUMS, only checksums.txt parity).
func writeCLIBinaryFiles(t *testing.T, cliBinariesDir, binaryName string) {
	t.Helper()
	dir := filepath.Join(cliBinariesDir, binaryName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, binaryName+"-linux-amd64"), []byte("linux-amd64-binary"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, binaryName+"-darwin-arm64"), []byte("darwin-arm64-binary"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte("deadbeef  "+binaryName+"-linux-amd64\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte("not-published"), 0o644))
}

// TestPublishCLIBinaries_ConfirmedVersion_UploadsWithCorrectKeys is scenario
// (a): a confirmed-version CLI-binary target (release_helper_go, non-empty
// non-Failed EffectiveVersion) must have its platform binaries and
// checksums.txt uploaded under cliBinaryS3Key's exact convention
// ("<binary>/<version>/<file>"), and must NOT upload the non-manifest
// SHA256SUMS file alongside them.
func TestPublishCLIBinaries_ConfirmedVersion_UploadsWithCorrectKeys(t *testing.T) {
	cliBinariesDir := t.TempDir()
	writeCLIBinaryFiles(t, cliBinariesDir, "release_helper_go")

	uploader := newFakeUploader()
	a := &Activities{S3Uploader: uploader}
	versions := map[string]string{"image:tools-release_helper_go": "v1.2.3"}
	finalizeTargets := map[string]FinalizeTargetOutcome{}

	failures := a.publishCLIBinaries(context.Background(), []string{"tools-release_helper_go"}, versions, finalizeTargets, cliBinariesDir, true)

	require.Empty(t, failures)
	require.Equal(t, FinalizeTargetOutcome{EffectiveVersion: "v1.2.3"}, finalizeTargets["image:tools-release_helper_go"], "a successful publish must record the plan version as this target's outcome")

	require.Len(t, uploader.uploads, 3, "expected exactly the two platform binaries plus checksums.txt, got keys %v", uploadedKeys(uploader))
	require.Equal(t, []byte("linux-amd64-binary"), uploader.uploads["release_helper_go/v1.2.3/release_helper_go-linux-amd64"])
	require.Equal(t, []byte("darwin-arm64-binary"), uploader.uploads["release_helper_go/v1.2.3/release_helper_go-darwin-arm64"])
	require.Contains(t, uploader.uploads, "release_helper_go/v1.2.3/checksums.txt")

	_, gotSHA256SUMS := uploader.uploads["release_helper_go/v1.2.3/SHA256SUMS"]
	require.False(t, gotSHA256SUMS, "SHA256SUMS must not be published -- only checksums.txt parity is in scope")
}

func uploadedKeys(u *fakeUploader) []string {
	keys := make([]string, 0, len(u.uploads))
	for k := range u.uploads {
		keys = append(keys, k)
	}
	return keys
}

// TestPublishCLIBinaries_UsesPlanVersion proves publishCLIBinaries uploads
// under the plan's resolved version for the app-registry CLI target too
// (not just release_helper_go) -- these apps have no image to
// finalize-app-retag (app_type "cli"), so there is no EffectiveVersion
// confirmation to wait on; the plan-time version passed in `versions` is
// what's actually published under.
func TestPublishCLIBinaries_UsesPlanVersion(t *testing.T) {
	cliBinariesDir := t.TempDir()
	writeCLIBinaryFiles(t, cliBinariesDir, "app-registry")

	uploader := newFakeUploader()
	a := &Activities{S3Uploader: uploader}
	versions := map[string]string{"image:tools-app-registry": "v0.9.0"}
	finalizeTargets := map[string]FinalizeTargetOutcome{}

	failures := a.publishCLIBinaries(context.Background(), []string{"tools-app-registry"}, versions, finalizeTargets, cliBinariesDir, true)

	require.Empty(t, failures)
	require.Contains(t, uploader.uploads, "app-registry/v0.9.0/checksums.txt")
	require.Contains(t, uploader.uploads, "app-registry/v0.9.0/app-registry-linux-amd64")
}

// TestPublishCLIBinaries_NonCLIBinaryApp_NeverTouched is scenario (b): an
// app not in cliBinaryTargets (i.e. not release_helper_go/app-registry) must
// be skipped entirely -- no uploader call, no finalizeTargets mutation, no
// failure recorded -- regardless of what its EffectiveVersion is or whether
// a cli-binaries artifact is even present.
func TestPublishCLIBinaries_NonCLIBinaryApp_NeverTouched(t *testing.T) {
	uploader := &refusingUploader{t: t}
	a := &Activities{S3Uploader: uploader}
	versions := map[string]string{"image:demo-widget": "v1.0.0"}
	finalizeTargets := map[string]FinalizeTargetOutcome{
		"image:demo-widget": {EffectiveVersion: "v1.0.0"},
	}
	original := finalizeTargets["image:demo-widget"]

	failures := a.publishCLIBinaries(context.Background(), []string{"demo-widget"}, versions, finalizeTargets, t.TempDir(), true)

	require.Empty(t, failures)
	require.Equal(t, original, finalizeTargets["image:demo-widget"], "a non-CLI-binary app's outcome must be untouched")
}

// TestPublishCLIBinaries_NoConfirmedVersion_NeverUploads is scenario (c) of
// the worker directive (FR9/FR10's guarantee): a CLI-binary-target app with
// no version in the plan's `versions` map -- covering both ways that can
// happen (missing entry, empty string) -- must never trigger an upload.
// Uses refusingUploader so any Upload call fails the test outright rather
// than merely being asserted against afterward.
func TestPublishCLIBinaries_NoConfirmedVersion_NeverUploads(t *testing.T) {
	cliBinariesDir := t.TempDir()
	writeCLIBinaryFiles(t, cliBinariesDir, "release_helper_go")
	writeCLIBinaryFiles(t, cliBinariesDir, "app-registry")

	cases := []struct {
		name     string
		versions map[string]string
		apps     []string
	}{
		{
			name:     "missing versions entry",
			versions: map[string]string{},
			apps:     []string{"tools-release_helper_go"},
		},
		{
			name:     "empty version string",
			versions: map[string]string{"image:tools-app-registry": ""},
			apps:     []string{"tools-app-registry"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uploader := &refusingUploader{t: t}
			a := &Activities{S3Uploader: uploader}
			finalizeTargets := map[string]FinalizeTargetOutcome{}

			failures := a.publishCLIBinaries(context.Background(), tc.apps, tc.versions, finalizeTargets, cliBinariesDir, true)

			require.Empty(t, failures, "no confirmed version means nothing to fail either -- this target is simply not touched")
			require.Empty(t, finalizeTargets, "a target with no confirmed version must not be mutated")
		})
	}
}

// TestPublishCLIBinaries_UploadFailure_MarksTargetFailed is scenario (d): an
// uploader error must be surfaced as a per-target failure (finalizeTargets
// entry overwritten to Failed, detail returned in the failures slice) --
// never silently swallowed, matching every other failure path in this file.
func TestPublishCLIBinaries_UploadFailure_MarksTargetFailed(t *testing.T) {
	cliBinariesDir := t.TempDir()
	writeCLIBinaryFiles(t, cliBinariesDir, "release_helper_go")

	uploader := newFakeUploader()
	uploader.err = errors.New("simulated S3 write failure")
	a := &Activities{S3Uploader: uploader}
	versions := map[string]string{"image:tools-release_helper_go": "v1.2.3"}
	finalizeTargets := map[string]FinalizeTargetOutcome{}

	failures := a.publishCLIBinaries(context.Background(), []string{"tools-release_helper_go"}, versions, finalizeTargets, cliBinariesDir, true)

	require.Len(t, failures, 1)
	require.Contains(t, failures[0], "simulated S3 write failure")

	outcome := finalizeTargets["image:tools-release_helper_go"]
	require.True(t, outcome.Failed, "an upload error must mark the target Failed, not leave its prior success outcome in place")
	require.Contains(t, outcome.Detail, "simulated S3 write failure")
}

// TestPublishCLIBinaries_MissingCLIBinariesArtifact_FailsThatTarget covers
// #984's Implementation scope explicitly ("If the cli-binaries artifact is
// missing ... for a target that IS release_helper_go/app-registry and did
// finalize successfully, treat this as a per-target failure ... do not
// silently skip a confirmed-version target's publish"): haveCLIBinaries
// false must fail the target, not merely skip it.
func TestPublishCLIBinaries_MissingCLIBinariesArtifact_FailsThatTarget(t *testing.T) {
	uploader := newFakeUploader()
	a := &Activities{S3Uploader: uploader}
	versions := map[string]string{"image:tools-release_helper_go": "v1.2.3"}
	finalizeTargets := map[string]FinalizeTargetOutcome{}

	failures := a.publishCLIBinaries(context.Background(), []string{"tools-release_helper_go"}, versions, finalizeTargets, t.TempDir(), false /* haveCLIBinaries */)

	require.Len(t, failures, 1)
	require.Contains(t, failures[0], "no cli-binaries artifact entry")
	require.Empty(t, uploader.uploads, "no upload must be attempted when the artifact never arrived")

	outcome := finalizeTargets["image:tools-release_helper_go"]
	require.True(t, outcome.Failed)
}

// TestCLIBinaryS3Key_MatchesDocumentedConvention pins cliBinaryS3Key's
// output format against tools/app_registry/ENV.md's documented convention
// and issue #983's read-side expectation:
// "<binary>/<version>/<file>". A deliberately wrong separator would be
// caught here first.
func TestCLIBinaryS3Key_MatchesDocumentedConvention(t *testing.T) {
	require.Equal(t, "release_helper_go/v1.2.3/release_helper_go-linux-amd64", cliBinaryS3Key("release_helper_go", "v1.2.3", "release_helper_go-linux-amd64"))
	require.Equal(t, "app-registry/v0.9.0/checksums.txt", cliBinaryS3Key("app-registry", "v0.9.0", "checksums.txt"))
}
