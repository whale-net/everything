package release

import (
	"archive/zip"
	"bytes"
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

func TestPlanBuildID_ExtractsFieldOrEmpty(t *testing.T) {
	require.Equal(t, "", planBuildID(nil))
	require.Equal(t, "", planBuildID([]byte("not json")))
	require.Equal(t, "", planBuildID([]byte(`{"version":"v1.0.0"}`)))
	require.Equal(t, "build-123", planBuildID([]byte(`{"build_id":"build-123","version":"v1.0.0"}`)))
}
