package store_test

import (
	"os"
	"strings"
	"testing"
)

// forbiddenFileReadCalls are the file-opening calls NFR3 forbids anywhere
// downstream of ImportCompletion.SourcePath: nothing in krill ever opens
// the path an import_completion row names, because that column is an
// audit-trail-only record of where an import came from, never a second
// readable (or writable) source of truth for the imported content (LB5).
var forbiddenFileReadCalls = []string{"os.Open(", "os.ReadFile(", "ioutil.ReadFile("}

// TestNFR3_ImportCompletionSourceNeverOpensAFile is the cheap form of the
// NFR3 structural check issue #2548's Testing item 8 names: rather than
// grep the whole krill/ tree (which would need every source file staged as
// Bazel data), this reads this package's own import_completion.go back
// (declared as `data` on this test's BUILD.bazel target) and asserts it
// never calls a file-opening function. If a future change adds a
// file-read call to import_completion.go, this test goes red and names
// the file directly, rather than NFR3 regressing silently.
//
// This is deliberately narrow, not a repo-wide guarantee: a grep across
// krill/ at the time this test was written found exactly one other file
// referencing SourcePath, krill/importer/importer.go, which carries the
// same check as TestNFR3_ImporterSourceNeverOpensSourcePath. A third
// reference added anywhere else in krill/ needs its own copy of this
// check -- see this test's own name for why "the whole tree" is not what
// it actually covers.
func TestNFR3_ImportCompletionSourceNeverOpensAFile(t *testing.T) {
	src, err := os.ReadFile("import_completion.go")
	if err != nil {
		t.Fatalf("read import_completion.go back (is it declared as `data` on this go_test target?): %v", err)
	}
	body := string(src)
	for _, forbidden := range forbiddenFileReadCalls {
		if strings.Contains(body, forbidden) {
			t.Errorf("import_completion.go must never open a file (NFR3: SourcePath is audit-trail only) -- found %q", forbidden)
		}
	}
}
