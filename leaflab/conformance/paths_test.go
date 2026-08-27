// Package conformance holds NFR1.a authentication conformance tests --
// fails the build when a route or RPC serving household data ships without
// an authenticated principal -- plus NFR18.1 ("the BFF holds no rounding,
// coarsening, suppression or label-selection rule") for the LeafLab API and
// UI. These tests read real checked-in source/proto files as data (never a
// copy or a fixture) so a regression in leaflab/api or leaflab/ui is caught
// directly, not by proxy.
//
// The checks enumerate every `rpc` in leaflab/api/proto/api.proto's
// LeafLabAPI service and every browser-reachable route registered in
// leaflab/ui/main.go's setupRoutes, then verify each sits behind its
// package's authenticating interceptor/middleware or appears on an
// explicit, single-entry anonymous allowlist (GetHealth for the API; the
// enumerated public set -- login, callback, static assets, health -- for
// the BFF).
//
// Mirrors the pattern in tools/app_registry/conformance/paths_test.go's
// appRegistryDir helper.
package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// leaflabDir locates leaflab, using BUILD.bazel (staged as a data
// dependency via the "//leaflab:build_file" filegroup) as the marker file.
// Bazel stages data relative to the runfiles root; a plain `go test` run
// from this package's own directory finds it by walking up.
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
// leaflab/<subdir> (e.g. "api", "api/contract", "ui", "ui/components"),
// keyed by their path relative to leaflab. Does not recurse -- callers
// that need multiple subpackages call this once per subdir, matching the
// per-package conformance_srcs filegroups wired in each subpackage's
// BUILD.bazel.
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

// rpcNames is the return type of enumerateRPCsFromProto: the RPC method
// names declared on LeafLabAPI in leaflab/api/proto/api.proto, in
// declaration order.
type rpcNames []string

// enumerateRPCsFromProto parses leaflab/api/proto/api.proto's LeafLabAPI
// service and returns every declared `rpc` name.
//
// TODO(Implementation phase, NFR1.a RPC coverage): parse the `service
// LeafLabAPI { ... }` block for every `rpc <Name>(...)` declaration. A
// regex/line-scan over the proto text is sufficient (mirrors
// tools/app_registry/conformance's source-analysis style) -- this does not
// need a real protoc/protoreflect compile.
func enumerateRPCsFromProto(t *testing.T, proto string) rpcNames {
	t.Helper()
	_ = proto
	return nil
}

// bffRoute is one route registration found in leaflab/ui/main.go's
// setupRoutes: the pattern passed to mux.HandleFunc and whether it's
// wrapped in app.auth.RequireAuthFunc.
type bffRoute struct {
	pattern      string
	requiresAuth bool
}

// enumerateBFFRoutes parses leaflab/ui/main.go's setupRoutes function and
// returns every mux.HandleFunc registration, noting whether each is
// wrapped in app.auth.RequireAuthFunc.
//
// TODO(Implementation phase, NFR1.a BFF route coverage): use go/parser to
// walk setupRoutes' *ast.FuncDecl body for mux.HandleFunc(pattern, handler)
// call expressions, extracting the string literal pattern and checking
// whether the handler expression's outermost call is
// app.auth.RequireAuthFunc(...).
func enumerateBFFRoutes(t *testing.T, mainGoSrc string) []bffRoute {
	t.Helper()
	_ = mainGoSrc
	return nil
}
