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
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

	var rpcs []string
	lines := strings.Split(proto, "\n")
	inService := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Enter the service block
		if strings.Contains(trimmed, "service LeafLabAPI") {
			inService = true
			continue
		}

		// Exit the service block
		if inService && strings.HasPrefix(trimmed, "}") {
			break
		}

		// Extract RPC declarations: "rpc MethodName(Request) returns (Response);"
		if inService && strings.HasPrefix(trimmed, "rpc ") && strings.Contains(trimmed, "(") {
			// Parse "rpc MethodName(Request) returns (Response);"
			// Extract method name between "rpc " and "("
			startIdx := len("rpc ")
			endIdx := strings.Index(trimmed, "(")
			if endIdx > startIdx {
				methodName := strings.TrimSpace(trimmed[startIdx:endIdx])
				rpcs = append(rpcs, methodName)
			}
		}
	}

	return rpcs
}

// enumerateBFFRoutes parses leaflab/ui/main.go and returns a map of
// routes to their authentication status. The implementation uses go/parser
// to extract route patterns and their handlers.
func enumerateBFFRoutes(t *testing.T) map[string]bool {
	t.Helper()
	mainGoSrc := mustReadFile(t, "ui/main.go")

	// Parse the Go source code
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "ui/main.go", mainGoSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse ui/main.go: %v", err)
	}

	routes := make(map[string]bool) // route -> is_protected

	// Walk the AST to find mux.HandleFunc calls
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check if this is a mux.HandleFunc call
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}

		if sel.Sel.Name != "HandleFunc" {
			return true
		}

		// Extract the route path (first argument, must be a string literal)
		if len(call.Args) < 2 {
			return true
		}

		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}

		route, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}

		// Check if the handler is wrapped in RequireAuthFunc
		// If the second argument is a CallExpr to RequireAuthFunc, it's protected
		isProtected := isHandlerProtected(call.Args[1])
		routes[route] = isProtected

		return true
	})

	return routes
}

// isHandlerProtected checks if a handler expression is wrapped in RequireAuthFunc
func isHandlerProtected(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		// Check if it's a direct identifier (not sel.Sel)
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return false
		}
		return id.Name == "RequireAuthFunc"
	}

	return sel.Sel.Name == "RequireAuthFunc"
}

// getRPCsRequiringAuth returns a set of RPCs that call requireAuthentication.
// It parses the server implementation (server.go) and identifies methods
// that include a requireAuthentication call.
func getRPCsRequiringAuth(t *testing.T) map[string]bool {
	t.Helper()
	serverSrc := mustReadFile(t, "api/server.go")

	// Parse the Go source code
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "api/server.go", serverSrc, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse api/server.go: %v", err)
	}

	rpcsWithAuth := make(map[string]bool)

	// Walk the AST to find RPC methods and check for requireAuthentication calls
	ast.Inspect(f, func(n ast.Node) bool {
		// Track which function we're in
		if fn, ok := n.(*ast.FuncDecl); ok {
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				// This is a method on LeafLabAPIServer
				if recvType, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := recvType.X.(*ast.Ident); ok && id.Name == "LeafLabAPIServer" {
						// Check if this function contains requireAuthentication call
						hasAuthCheck := containsRequireAuthCall(fn.Body)
						rpcsWithAuth[fn.Name.Name] = hasAuthCheck
					}
				}
			}
			return true
		}
		return true
	})

	return rpcsWithAuth
}

// containsRequireAuthCall checks if a block statement contains a call to requireAuthentication
func containsRequireAuthCall(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}

	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Check if this is a call to requireAuthentication
		if id, ok := call.Fun.(*ast.Ident); ok {
			if id.Name == "requireAuthentication" {
				found = true
				return false
			}
		}

		return true
	})

	return found
}
