package conformance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestNFR3_UIPackageIssuesNoRegistryDomainSQL is the NFR-3 conformance
// check: the UI package's only direct database use is htmxauth's session
// storage (via libs/go/db, wired up once in ui/main.go and handed straight
// to htmxauth.NewDBSessionManager) -- it must never issue SQL of its own,
// and in particular never against a registry domain table (apps,
// environments, promotions, builds, artifacts, ...). NFR-3 is enforced "by
// convention and test" rather than by database privileges (the UI holds
// the same credential as the API), so this test IS the enforcement
// mechanism -- it must fail the moment someone adds a direct query.
//
// Both checks below parse real Go syntax (go/parser), not raw text, so a
// comment merely mentioning a package path or the word "select" can't
// trip either one -- and, more importantly, can't be used to quiet a real
// violation either:
//   - import check: no ui source file other than main.go may import
//     libs/go/db, database/sql, a Postgres driver, or an app_registry
//     server-internal package (which owns the real registry-table SQL).
//   - string-literal check: no ui source file's string/backtick literal
//     looks like a SQL statement (SELECT ... FROM / INSERT INTO ... VALUES
//     / UPDATE ... SET / DELETE FROM) -- this catches a query built inline
//     even if routed through some other already-imported helper.
func TestNFR3_UIPackageIssuesNoRegistryDomainSQL(t *testing.T) {
	files := map[string]string{}
	for k, v := range globGoFiles(t, "ui") {
		files[k] = v
	}
	for k, v := range globGoFiles(t, "ui/components") {
		files[k] = v
	}
	for k, v := range globGoFiles(t, "ui/pages") {
		files[k] = v
	}
	for k, v := range globGoFiles(t, "ui/matrix") {
		files[k] = v
	}
	if len(files) < 5 {
		// Guards the guard: if the data deps silently stopped resolving
		// (e.g. a filegroup renamed), every check below would vacuously
		// pass. The real ui/ tree has well over a dozen .go files.
		t.Fatalf("only found %d ui source files -- check the data dependencies (conformance_srcs filegroups) in BUILD.bazel", len(files))
	}

	disallowedImports := []string{
		"github.com/whale-net/everything/libs/go/db",
		"database/sql",
		"jackc/pgx",
		"lib/pq",
		"github.com/whale-net/everything/tools/app_registry/server",
	}

	// SELECT needs a FROM clause to count -- otherwise ordinary English
	// sentences containing the word "select" (a real UI label: "Select a
	// version to promote.") would false-positive.
	sqlStmt := regexp.MustCompile(`(?is)^\s*(select\s+.+\bfrom\b|insert\s+into\s+\w|update\s+\w+\s+set\b|delete\s+from\s+\w)`)

	fset := token.NewFileSet()
	for path, src := range files {
		isMain := path == "ui/main.go"

		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		for _, imp := range f.Imports {
			importPath, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote import %s: %v", path, imp.Path.Value, err)
			}
			for _, disallowed := range disallowedImports {
				if !strings.Contains(importPath, disallowed) {
					continue
				}
				if disallowed == "github.com/whale-net/everything/libs/go/db" && isMain {
					// main.go is the one place allowed to open the pool
					// that backs htmxauth's session store -- NFR-3's
					// documented exception, see ui/main.go's doc comment.
					continue
				}
				t.Errorf("%s: imports %q -- the UI's only direct database use is htmxauth's session "+
					"storage in main.go (NFR-3); registry domain data must come over gRPC from "+
					"app-registry-api", path, importPath)
			}
		}

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			inner, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if sqlStmt.MatchString(inner) {
				t.Errorf("%s: string literal looks like a SQL statement (%q) -- the UI must never "+
					"issue SQL against registry domain tables (NFR-3); route this through the gRPC "+
					"registry client instead", path, inner)
			}
			return true
		})
	}
}
