// Structural proof of FR c4ab6c68's drift-prevention criterion, read back
// from the package's own source rather than from a database
// (mirroring active_edge_guard_test.go's and milestone_status_nfr2_test.go's
// technique -- the files are declared as `data` on this target).
//
// The criterion is that a count and the list it describes share one SQL
// clause and one filter set, so a filter added to a list cannot silently
// fail to reach its count. The shared builders (claimedTasksQuery and
// siblings) make that true today, but nothing stopped a later change from
// inlining a query of its own into a Count* method, at which point the two
// would drift with every test still green. These assertions are what make
// the sharing structural rather than incidental:
//
//   - no Count* method may contain SQL of its own (no SELECT, FROM or JOIN
//     string literal anywhere in its body), so a count cannot grow a second
//     predicate to disagree with;
//   - each Count* method must call the very builder its list calls, and the
//     list must call it too, so "one clause for both" is a named call site
//     on each side rather than two look-alike queries;
//   - the Overview's three sub-line figures must be their own queue's
//     builder narrowed by a named helper, never a query written to look
//     similar.
//
// A count's body naming the shared builder is what a fix looks like: extend
// the builder and both reads move together. Inlining SQL into a count is
// what turns this file red.
package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countReadsUnderTest pairs every count read with the list it describes and
// the one query builder both are required to call.
var countReadsUnderTest = []struct {
	file    string
	count   string
	list    string
	builder string
}{
	{"task_console.go", "CountClaimedTasks", "ListClaimedTasks", "claimedTasksQuery"},
	{"task_console.go", "CountCancelledTasks", "ListCancelledTasks", "cancelledTasksQuery"},
	{"task_console.go", "CountEscalatedTasks", "ListEscalatedTasks", "escalatedTasksQuery"},
	{"task_note_console.go", "CountOpenNotes", "ListOpenNotes", "openNotesQuery"},
	{"task_product_list.go", "CountProductTasks", "ListProductTasks", "productTasksQuery"},
}

// forbiddenCountSQL are the SQL statement openings a count read may not
// spell out for itself: its row set comes from the shared builder, so a
// SELECT, FROM or JOIN literal in a count's body is by definition a second
// predicate that can disagree with the list beside it.
var forbiddenCountSQL = []string{"SELECT", "FROM ", "JOIN "}

// parseStoreFile parses one of the package's own source files so its
// functions can be inspected as syntax rather than as text (is it a
// declared `data` file on this target?).
func parseStoreFile(t *testing.T, name string) *ast.File {
	t.Helper()
	path := filepath.Join(".", name)
	src, err := os.ReadFile(path)
	require.NoError(t, err, "read %s back (is it declared as `data` on this go_test target?): %v", name, err)

	file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	require.NoError(t, err, "parse %s: %v", name, err)
	return file
}

// findFunc returns the named function or method declared in file.
func findFunc(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	require.FailNowf(t, "function not found", "the file under test must declare %s", name)
	return nil
}

// calledFunctions is the set of function names a body calls directly.
func calledFunctions(body *ast.BlockStmt) map[string]bool {
	called := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			called[fn.Name] = true
		case *ast.SelectorExpr:
			called[fn.Sel.Name] = true
		}
		return true
	})
	return called
}

// stringLiterals is every string literal in a body, so a SQL fragment
// spelled inline can be found wherever it sits.
func stringLiterals(body *ast.BlockStmt) []string {
	var lits []string
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING {
			lits = append(lits, strings.ToUpper(lit.Value))
		}
		return true
	})
	return lits
}

// TestCountReads_CarryNoSQLOfTheirOwn is the anti-drift half of FR
// c4ab6c68: each Count* read's body is a guard, a call to its shared
// builder, and the count itself -- never a FROM/JOIN/WHERE of its own.
func TestCountReads_CarryNoSQLOfTheirOwn(t *testing.T) {
	for _, tc := range countReadsUnderTest {
		t.Run(tc.count, func(t *testing.T) {
			file := parseStoreFile(t, tc.file)
			count := findFunc(t, file, tc.count)

			for _, lit := range stringLiterals(count.Body) {
				for _, forbidden := range forbiddenCountSQL {
					assert.NotContains(t, lit, forbidden,
						"%s must not spell out %q: its row set comes from %s, shared with %s, and a query of its own is how a count drifts from the list it describes",
						tc.count, strings.TrimSpace(forbidden), tc.builder, tc.list)
				}
			}
		})
	}
}

// TestCountReads_AndTheirLists_CallTheSameQueryBuilder is the positive half:
// for every queue, both the count and the list name the same builder. A
// count that stopped calling it, or a list that grew its own inline query,
// is what this turns red -- which is the drift the FR forbids.
func TestCountReads_AndTheirLists_CallTheSameQueryBuilder(t *testing.T) {
	byFile := map[string]*ast.File{}
	for _, tc := range countReadsUnderTest {
		if _, ok := byFile[tc.file]; !ok {
			byFile[tc.file] = parseStoreFile(t, tc.file)
		}
	}

	for _, tc := range countReadsUnderTest {
		t.Run(tc.count, func(t *testing.T) {
			file := byFile[tc.file]
			assert.True(t, calledFunctions(findFunc(t, file, tc.count).Body)[tc.builder],
				"%s must count over %s -- the clause %s pages over -- or the two describe different rows",
				tc.count, tc.builder, tc.list)
			assert.True(t, calledFunctions(findFunc(t, file, tc.list).Body)[tc.builder],
				"%s must read over %s, the same clause %s counts", tc.list, tc.builder, tc.count)
		})
	}
}

// TestCountConsoleOverview_CountsOverTheQueuesOwnBuilders proves the
// Overview is not seven look-alike queries: its four headline figures call
// the four queue builders, and its three sub-line figures call the named
// narrowing helpers built on top of them.
func TestCountConsoleOverview_CountsOverTheQueuesOwnBuilders(t *testing.T) {
	file := parseStoreFile(t, "console_overview.go")
	called := calledFunctions(findFunc(t, file, "CountConsoleOverview").Body)

	for _, builder := range []string{
		"escalatedTasksQuery", "claimedTasksQuery", "cancelledTasksQuery", "openNotesQuery",
		// The three sub-lines: each is its own queue's clause with one
		// conjunct added, not a query of its own.
		"escalatedTasksQuerySince", "claimedTasksQueryExpiringBefore", "openNotesQueryOfKind",
	} {
		assert.True(t, called[builder],
			"CountConsoleOverview must count over %s, so an Overview figure and the queue list beneath it cannot disagree", builder)
	}

	// And it counts through the one shared counter rather than scanning
	// rows itself, so "a failed count is an error" is one implementation
	// instead of seven.
	assert.True(t, called["countConsoleRows"],
		"CountConsoleOverview must count through countConsoleRows, which returns a failed query's error rather than 0")
	for _, lit := range stringLiterals(findFunc(t, file, "CountConsoleOverview").Body) {
		for _, forbidden := range forbiddenCountSQL {
			assert.NotContains(t, lit, forbidden,
				"CountConsoleOverview must not spell out %q: every figure is a COUNT(*) over a queue's own clause", strings.TrimSpace(forbidden))
		}
	}
}

// TestCountConsoleSubLineHelpers_BuildOnTheirQueuesClause pins the three
// sub-line helpers to that relationship directly: each is its queue's
// builder plus one conjunct, so a sub-line can never exceed its queue's
// total or count a row the queue would not return.
func TestCountConsoleSubLineHelpers_BuildOnTheirQueuesClause(t *testing.T) {
	for _, tc := range []struct {
		file    string
		helper  string
		builder string
	}{
		{"task_console.go", "escalatedTasksQuerySince", "escalatedTasksQuery"},
		{"task_console.go", "claimedTasksQueryExpiringBefore", "claimedTasksQuery"},
		{"task_note_console.go", "openNotesQueryOfKind", "openNotesQuery"},
	} {
		t.Run(tc.helper, func(t *testing.T) {
			file := parseStoreFile(t, tc.file)
			body := findFunc(t, file, tc.helper).Body
			assert.True(t, calledFunctions(body)[tc.builder],
				"%s must narrow %s, not spell a query of its own -- the Overview sub-line and the queue it sits under are the same rows", tc.helper, tc.builder)
			assert.False(t, calledFunctions(body)["countConsoleRows"],
				"%s builds a clause; counting is the caller's job", tc.helper)
		})
	}
}
