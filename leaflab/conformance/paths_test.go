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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

// globFilesWithExt returns the contents of every file directly inside
// leaflab/<subdir> whose extension is one of exts (e.g. ".go", ".templ"),
// keyed by their path relative to leaflab. Does not recurse -- callers
// that need multiple subpackages call this once per subdir, matching the
// per-package conformance_srcs filegroups wired in each subpackage's
// BUILD.bazel.
func globFilesWithExt(t *testing.T, subdir string, exts ...string) map[string]string {
	t.Helper()
	dir := filepath.Join(leaflabDir(t), subdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v (check the data dependency in BUILD.bazel)", dir, err)
	}
	allowed := map[string]bool{}
	for _, ext := range exts {
		allowed[ext] = true
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !allowed[filepath.Ext(e.Name())] {
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

// globGoFiles returns the contents of every *.go file directly inside
// leaflab/<subdir> (e.g. "api", "api/contract", "ui", "ui/components"),
// keyed by their path relative to leaflab. Does not recurse -- see
// globFilesWithExt.
func globGoFiles(t *testing.T, subdir string) map[string]string {
	t.Helper()
	return globFilesWithExt(t, subdir, ".go")
}

// rpcNames is the return type of enumerateRPCsFromProto: the RPC method
// names declared on LeafLabAPI in leaflab/api/proto/api.proto, in
// declaration order.
type rpcNames []string

// serviceBlockRe locates a `service <Name> { ... }` block. Non-greedy so a
// proto file with more than one service (not the case today, but the
// negative-fixture tests synthesize small proto snippets) only captures up
// to the first closing brace at the top level of that block.
var serviceBlockRe = regexp.MustCompile(`(?s)service\s+(\w+)\s*\{(.*?)\n\}`)

// rpcDeclRe matches one `rpc <Name>(...) returns (...);` declaration line
// inside a service block.
var rpcDeclRe = regexp.MustCompile(`\brpc\s+(\w+)\s*\(`)

// enumerateRPCsFromProto parses proto's `service LeafLabAPI { ... }` block
// (or, for a synthetic fixture, whatever single service block is present)
// and returns every declared `rpc` name in declaration order. A
// regex/line-scan over the proto text, mirroring
// tools/app_registry/conformance's source-analysis style -- this does not
// need a real protoc/protoreflect compile.
func enumerateRPCsFromProto(t *testing.T, proto string) rpcNames {
	t.Helper()
	m := serviceBlockRe.FindStringSubmatch(proto)
	if m == nil {
		t.Fatal("no `service <Name> { ... }` block found in proto source -- check the data dependency in BUILD.bazel or the fixture text")
		return nil
	}
	body := m[2]
	matches := rpcDeclRe.FindAllStringSubmatch(body, -1)
	names := make(rpcNames, 0, len(matches))
	for _, rm := range matches {
		names = append(names, rm[1])
	}
	return names
}

// shortRPCName returns the trailing "<Method>" segment of a full gRPC
// method name ("/pkg.Service/Method" -> "Method"), or the input unchanged
// if it isn't in that form (e.g. already a bare RPC name, as used directly
// by some synthetic fixtures).
func shortRPCName(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// anonymousMethodsBlockRe locates the `anonymousMethods = map[string]bool{
// ... }` (or `var anonymousMethods map[string]bool{ ... }`) literal in
// leaflab/api/auth.go.
var anonymousMethodsBlockRe = regexp.MustCompile(`(?s)anonymousMethods\s*(?:map\[string\]bool)?\s*=\s*map\[string\]bool\{(.*?)\}`)

// anonymousMethodsEntryRe matches one `<key>: true` entry inside the
// anonymousMethods map literal. <key> is either a quoted string literal or
// a bare identifier referencing a `const <identifier> = "<value>"`
// elsewhere in the same source (auth.go's healthFullMethod pattern).
var anonymousMethodsEntryRe = regexp.MustCompile(`(?m)("[^"]*"|\w+)\s*:\s*true`)

// constStringRe matches a `const <Name> = "<value>"` (or `const <Name>
// string = "<value>"`) declaration.
var constStringRe = regexp.MustCompile(`const\s+(\w+)\s+(?:\w+\s+)?=\s*"([^"]*)"`)

// enumerateAnonymousAllowlist parses authGoSrc's anonymousMethods
// map[string]bool literal (leaflab/api/auth.go's FR11.2/FR63.2 allowlist)
// and returns the short RPC name for every entry -- resolving a bare
// identifier key through any `const <identifier> = "<full-method>"`
// declaration in the same source, matching auth.go's own
// healthFullMethod pattern. Returns nil (not an error) when no
// anonymousMethods literal is present, so callers can distinguish "empty
// allowlist" from "malformed source" by checking the map block match
// themselves if needed.
func enumerateAnonymousAllowlist(t *testing.T, authGoSrc string) []string {
	t.Helper()
	m := anonymousMethodsBlockRe.FindStringSubmatch(authGoSrc)
	if m == nil {
		return nil
	}
	consts := map[string]string{}
	for _, cm := range constStringRe.FindAllStringSubmatch(authGoSrc, -1) {
		consts[cm[1]] = cm[2]
	}
	entries := anonymousMethodsEntryRe.FindAllStringSubmatch(m[1], -1)
	names := make([]string, 0, len(entries))
	for _, em := range entries {
		key := em[1]
		if strings.HasPrefix(key, `"`) {
			unq, err := strconv.Unquote(key)
			if err != nil {
				t.Fatalf("unquote anonymousMethods key %s: %v", key, err)
			}
			names = append(names, shortRPCName(unq))
			continue
		}
		if v, ok := consts[key]; ok {
			names = append(names, shortRPCName(v))
			continue
		}
		// Unresolved identifier (no matching const in this source) --
		// keep the raw identifier so it still shows up in a failure
		// message rather than silently vanishing from the allowlist.
		names = append(names, key)
	}
	return names
}

// authInterceptorWired reports whether mainGoSrc wires both
// NewAuthEnforcementUnaryInterceptor and NewAuthEnforcementStreamInterceptor
// (leaflab/api/auth.go's fail-closed enforcement interceptors -- see that
// file's doc comment for why grpcauth's own interceptor alone doesn't
// enforce this) into the server. This is the mechanism that authenticates
// every RPC not on the anonymous allowlist: it is server-wide, not
// per-RPC, so a single boolean covers every enumerated RPC at once.
func authInterceptorWired(mainGoSrc string) bool {
	unary := regexp.MustCompile(`\bNewAuthEnforcementUnaryInterceptor\s*\(\s*\)`)
	stream := regexp.MustCompile(`\bNewAuthEnforcementStreamInterceptor\s*\(\s*\)`)
	return unary.MatchString(mainGoSrc) && stream.MatchString(mainGoSrc)
}

// bffRoute is one route registration found in leaflab/ui/main.go's
// setupRoutes: the pattern passed to mux.HandleFunc and whether it's
// wrapped in app.auth.RequireAuthFunc.
type bffRoute struct {
	pattern      string
	requiresAuth bool
}

// enumerateBFFRoutes parses mainGoSrc's setupRoutes function (or, for a
// synthetic fixture, the first function found) and returns every
// mux.HandleFunc registration, noting whether the handler expression is
// wrapped in a call to RequireAuthFunc anywhere in its call chain (e.g.
// app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoards))).
// Uses go/parser to walk the real syntax tree rather than a text scan, so
// a route pattern that merely appears in a comment or string elsewhere in
// the file can't be mistaken for a registration.
func enumerateBFFRoutes(t *testing.T, mainGoSrc string) []bffRoute {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", mainGoSrc, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Name.Name == "setupRoutes" {
			fn = fd
			break
		}
	}
	if fn == nil {
		// Fall back to the first function declaration for a synthetic
		// fixture that doesn't bother naming its function setupRoutes.
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok {
				fn = fd
				break
			}
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatal("no setupRoutes (or any) function body found -- check the data dependency in BUILD.bazel or the fixture text")
		return nil
	}

	var routes []bffRoute
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("unquote route pattern %s: %v", lit.Value, err)
		}
		routes = append(routes, bffRoute{
			pattern:      pattern,
			requiresAuth: callChainContains(call.Args[1], "RequireAuthFunc"),
		})
		return true
	})
	return routes
}

// callChainContains reports whether expr, or any call expression nested
// inside it (e.g. an outer wrapper call around an inner one), invokes a
// method/function named name -- e.g. RequireAuthFunc anywhere in
// app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoards)).
func callChainContains(expr ast.Expr, name string) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == name {
				found = true
			}
		case *ast.Ident:
			if fn.Name == name {
				found = true
			}
		}
		return true
	})
	return found
}
