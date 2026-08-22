package release

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
// -- see finalize.go's readFinalizeAppResult. Good enough for tests that
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

// TestActivities_FinalizePublish_WorkspaceRootUnset_ClonesAndSucceeds
// proves FinalizePublish no longer hard-fails when a.WorkspaceRoot is
// unset -- it clones its own scratch workspace instead (cloneWorkspace).
// cloneWorkspaceFn is overridden here to avoid a real network git clone in
// this hermetic test -- cloneWorkspace itself (finalize.go) is a thin,
// directly-readable wrapper around writeback.MintInstallationToken (already
// covered by github_test.go's token-minting tests) and `git clone`, with no
// independent branching logic worth a separate unit test. The fake
// release_helper_go binary is a no-op (exit 0): this test is about
// FinalizePublish's workspace handling, not finalize-app/finalize-chart's
// own logic.
func TestActivities_FinalizePublish_WorkspaceRootUnset_ClonesAndSucceeds(t *testing.T) {
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

	var clonedDirs []string
	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		cloneWorkspaceFn: func(ctx context.Context, dir string) error {
			clonedDirs = append(clonedDirs, dir)
			return nil
		},
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"image:demo-widget": "v1.2.3"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err, "WorkspaceRoot unset must not hard-fail -- FinalizePublish should clone its own scratch workspace")
	require.True(t, result.Succeeded, "detail: %s", result.Detail)
	require.Len(t, clonedDirs, 1, "expected exactly one clone for one FinalizePublish invocation")
}

// TestActivities_FinalizePublish_WorkspaceRootSet_SkipsClone proves an
// explicit a.WorkspaceRoot (e.g. an operator override) is used as-is, with
// no clone attempted at all.
func TestActivities_FinalizePublish_WorkspaceRootSet_SkipsClone(t *testing.T) {
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

	cloneCalled := false
	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		WorkspaceRoot:  t.TempDir(),
		cloneWorkspaceFn: func(ctx context.Context, dir string) error {
			cloneCalled = true
			return nil
		},
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"image:demo-widget": "v1.2.3"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err)
	require.True(t, result.Succeeded, "detail: %s", result.Detail)
	require.False(t, cloneCalled, "an explicit WorkspaceRoot must be used as-is with no clone")
}

// TestActivities_FinalizePublish_NoOpApp_ChartPinsEffectiveVersion is the
// regression test for the bug found alongside release.yml's
// release-helm-charts (PR #961): a member app whose finalize-app call is a
// no-op rebuild (identical digest to an already-published version) reuses
// that older version instead of publishing under plan.Versions' plan-time
// version -- see finalizeAppResult's doc comment. finalize-chart's
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
	chartArgsFile := filepath.Join(binDir, "chart-args.txt")
	// finalize-app always reports effective_version "v1.0.0" -- simulating
	// a no-op rebuild reusing an older published version regardless of the
	// plan-time --version ("v1.2.3") this test's plan requests. finalize-
	// chart records its full argument list to chartArgsFile.
	script := fmt.Sprintf(`#!/bin/sh
cmd="$1"; shift
if [ "$cmd" = "finalize-app" ]; then
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
  exit 0
fi
exit 0
`, chartArgsFile)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		WorkspaceRoot:  t.TempDir(),
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

	data, err := os.ReadFile(chartArgsFile)
	require.NoError(t, err)
	joined := string(data)
	require.Contains(t, joined, `"demo-widget":"v1.0.0"`, "chart's --app-versions must pin the app's actual effective (published) version")
	require.NotContains(t, joined, `"demo-widget":"v1.2.3"`, "chart's --app-versions must not pin the unpublished plan-time version")
}

// TestActivities_FinalizePublish_MissingFinalizeAppResult_FailsThatTarget
// proves a target whose finalize-app succeeded but never wrote a readable
// --output-dir result (e.g. an older release_helper_go binary that doesn't
// support it yet) is reported as a per-target failure -- silently falling
// back to the unpublished plan-time version would reintroduce the same bug
// TestActivities_FinalizePublish_NoOpApp_ChartPinsEffectiveVersion guards.
func TestActivities_FinalizePublish_MissingFinalizeAppResult_FailsThatTarget(t *testing.T) {
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
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	a := &Activities{
		GitHub:         newTestDispatcher(t, mux),
		PlanBinaryPath: bin,
		WorkspaceRoot:  t.TempDir(),
	}

	plan := ResolvedPlan{
		ReleaseRunID: "release-run-1",
		Versions:     map[string]string{"image:demo-widget": "v1.2.3"},
	}
	ref := BuildRef{ReleaseRunID: "release-run-1", RunID: "42"}

	result, err := a.FinalizePublish(context.Background(), plan, ref)
	require.NoError(t, err)
	require.False(t, result.Succeeded)
	require.Contains(t, result.Detail, "demo-widget")
	require.Contains(t, result.Detail, "read finalize-app result")
}
