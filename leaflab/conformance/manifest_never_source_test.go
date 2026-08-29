package conformance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// configPackageDir locates leaflab/api/config, using its doc.go (staged as
// a data dependency via that package's "conformance_srcs" filegroup) as a
// marker file. Bazel stages data relative to the runfiles root; a plain
// `go test` run from this package's own directory finds it by walking up.
// Mirrors tools/app_registry/conformance's appRegistryDir helper.
func configPackageDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../.."} {
		marker := filepath.Join(c, "leaflab", "api", "config", "doc.go")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "leaflab", "api", "config")
		}
		// Also try the marker relative to this package's own directory
		// (leaflab/conformance), for a plain `go test ./...` run from the
		// repo root or from within this package.
		marker = filepath.Join(c, "api", "config", "doc.go")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "api", "config")
		}
	}
	t.Fatal("could not locate leaflab/api/config/doc.go -- check the conformance_srcs data dependency in BUILD.bazel")
	return ""
}

// TestConfigPackageNeverReachesManifestData is FR49's structural guard,
// not just a doc comment: leaflab/api/config's own checked-in source is
// parsed and grepped to prove it has no import of, or reference to, the
// manifest-report machinery (board_manifest_report/
// board_manifest_report_entry, migration 035; leaflab/api's
// GetReportedInventory/GetConfigDrift and their supporting Repository
// methods/types built on those tables). Without this, a future change
// could quietly wire the reported manifest in as an EDIT-scope
// materialisation fallback -- and an entry the board failed to
// instantiate would then be silently deleted from the stored desired
// state the next time it's carried forward (FR82.3), exactly the failure
// mode FR49 exists to rule out.
//
// Two checks, deliberately layered:
//   - Import guard: no production source file in leaflab/api/config may
//     import the leaflab/api package (which owns the manifest-report
//     reads) or a database driver at all -- this package is documented
//     pure (doc.go: "no database dependency"), and importing either would
//     be the first observable step toward smuggling manifest data into
//     materialisation.
//   - Identifier guard: no production source file's identifiers or string
//     literals (deliberately excluding comments, which already document
//     the FR49 invariant this test enforces, e.g. "never the reported
//     manifest, FR49") may contain "manifest" (case-insensitive) -- catches
//     a same-package helper, a copy-pasted type, or a call reaching
//     manifest data by a route the import guard alone wouldn't see.
//
// Test-only files (_test.go) are excluded: a future behavioural test in
// this package may legitimately construct a manifest-shaped fixture to
// prove the production code path ignores it (see this task's own Testing
// phase), which is the opposite of the defect this guard rules out.
func TestConfigPackageNeverReachesManifestData(t *testing.T) {
	dir := configPackageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v (check the conformance_srcs data dependency in BUILD.bazel)", dir, err)
	}

	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		checked++

		file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			lower := strings.ToLower(importPath)
			switch {
			case strings.Contains(lower, "manifest"):
				t.Errorf("%s imports %q -- leaflab/api/config must never reach manifest-report data (FR49)", path, importPath)
			case strings.HasSuffix(importPath, "/leaflab/api"):
				t.Errorf("%s imports %q -- leaflab/api/config must never depend on the API server package that owns the reported manifest (FR49)", path, importPath)
			case strings.Contains(lower, "pgx"):
				t.Errorf("%s imports %q -- leaflab/api/config is documented to have no database dependency at all", path, importPath)
			}
		}

		// Identifier/literal guard, deliberately walking the AST rather
		// than grepping raw source text: comments (including this
		// package's own doc.go/materialise.go/removekey.go doc comments,
		// which correctly *name* the FR49 invariant in prose, e.g. "never
		// the reported manifest, FR49") must never trip this guard --
		// only an actual identifier or string literal in the code itself.
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.Ident:
				if strings.Contains(strings.ToLower(v.Name), "manifest") {
					t.Errorf("%s: identifier %q references \"manifest\" -- leaflab/api/config (FR82's EDIT-scope materialisation) must never reach reported-inventory data; the manifest is a report, never a source (FR49)", path, v.Name)
				}
			case *ast.BasicLit:
				if v.Kind == token.STRING && strings.Contains(strings.ToLower(v.Value), "manifest") {
					t.Errorf("%s: string literal %s references \"manifest\" -- leaflab/api/config must never reach reported-inventory data (FR49)", path, v.Value)
				}
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatalf("no production source files found under %s -- this guard would be vacuously true", dir)
	}
}
