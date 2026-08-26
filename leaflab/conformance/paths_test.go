// Package conformance holds NFR1 authentication and NFR18.1 BFF policy
// conformance tests for the LeafLab API and UI. These tests read real
// checked-in source/proto files as data (never a copy or a fixture) so
// a regression in the API or UI package is caught directly, not by proxy.
//
// The tests enumerate RPCs from leaflab/api/proto/api.proto and browser-reachable
// routes from leaflab/ui/ handler registration, then verify that every RPC and
// route either sits behind the authentication interceptor/middleware or appears
// on an explicit allowlist of anonymous endpoints.
package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// leaflabDir locates leaflab, using BUILD.bazel (staged as a data dependency
// via the "build_file" filegroup) as the marker file. Bazel stages data
// relative to the runfiles root; a plain `go test` run from this package's
// own directory finds it by walking up. Mirrors the pattern in
// //tools/app_registry/conformance's appRegistryDir helper.
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

// mustReadFile reads a file relative to leaflabDir, failing the test on
// any error.
func mustReadFile(t *testing.T, rel string) string {
	t.Helper()
	dir := leaflabDir(t)
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
// leaflab/<subdir>, keyed by their path relative to leaflab.
func globGoFiles(t *testing.T, subdir string) map[string]string {
	t.Helper()
	dir := filepath.Join(leaflabDir(t), subdir)
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

// enumerateRPCsFromProto parses leaflab/api/proto/api.proto and returns
// a set of RPC method names from the LeafLabAPI service. The proto parser
// does not instantiate a real protoc compiler; instead it uses regex and
// string parsing to extract rpc declarations.
func enumerateRPCsFromProto(t *testing.T) []string {
	t.Helper()
	proto := mustReadFile(t, "api/proto/api.proto")
	// This is a placeholder; the actual implementation will parse the proto
	// and extract RPC names. For scaffolding, we return an empty list.
	// Implementation phase will add the real parsing logic.
	_ = proto
	return []string{}
}

// enumerateBFFRoutes parses leaflab/ui/main.go and returns a set of
// browser-reachable routes registered via mux.HandleFunc(). The implementation
// uses go/parser to extract route patterns and their handlers.
func enumerateBFFRoutes(t *testing.T) []string {
	t.Helper()
	// This is a placeholder; the actual implementation will parse the UI source
	// and extract route patterns. For scaffolding, we return an empty list.
	// Implementation phase will add the real parsing logic.
	return []string{}
}
