// Package conformance holds conformance tests for LeafLab documentation (#1189).
// These tests read real checked-in source/doc files as data (never a copy or a fixture)
// so a regression in the API/UI main.go, ENV.md, or configuration is caught directly.
package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// leaflabDir locates leaflab, using BUILD.bazel (staged as a data dependency)
// as the marker file. Bazel stages data relative to the runfiles root; a plain
// `go test` run from this package's own directory finds it by walking up.
func leaflabDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../.."} {
		marker := filepath.Join(c, "leaflab", "BUILD.bazel")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "leaflab")
		}
	}
	t.Fatal("could not locate leaflab/BUILD.bazel -- check the data dependency in BUILD.bazel")
	return ""
}

// mustReadFile reads a file relative to leaflabDir, failing the test on any error.
func mustReadFile(t *testing.T, rel string) string {
	t.Helper()
	dir := leaflabDir(t)
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v (check the data dependency in BUILD.bazel)", rel, err)
	}
	return string(b)
}
