// Package conformance holds cross-package NFR-3/deployment-rendering
// conformance tests for the App Registry UI (#656). These tests read real
// checked-in source/doc/config files as data (never a copy or a fixture) so
// a regression in the UI package, the domain BUILD.bazel, the Tiltfile, or
// ENV.md is caught directly, not by proxy.
package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// appRegistryDir locates tools/app_registry, using BUILD.bazel (staged as a
// data dependency via the "build_file" filegroup) as the marker file. Bazel
// stages data relative to the runfiles root; a plain `go test` run from
// this package's own directory finds it by walking up. Mirrors the pattern
// in //tools/app_registry/citest's githubDir helper.
func appRegistryDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../.."} {
		marker := filepath.Join(c, "tools", "app_registry", "BUILD.bazel")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "tools", "app_registry")
		}
	}
	t.Fatal("could not locate tools/app_registry/BUILD.bazel -- check the data dependency in BUILD.bazel")
	return ""
}

// mustReadFile reads a file relative to appRegistryDir, failing the test on
// any error.
func mustReadFile(t *testing.T, rel string) string {
	t.Helper()
	dir := appRegistryDir(t)
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v (check the data dependency in BUILD.bazel)", rel, err)
	}
	return string(b)
}

// mustReadDir returns the base names of every entry directly inside dir.
func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v (check the data dependency in BUILD.bazel)", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// globGoFiles returns the contents of every *.go file directly inside
// tools/app_registry/<subdir>, keyed by their path relative to
// tools/app_registry.
func globGoFiles(t *testing.T, subdir string) map[string]string {
	t.Helper()
	dir := filepath.Join(appRegistryDir(t), subdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v (check the data dependency in BUILD.bazel)", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s/%s: %v", dir, e.Name(), err)
		}
		out[filepath.Join(subdir, e.Name())] = string(b)
	}
	return out
}
