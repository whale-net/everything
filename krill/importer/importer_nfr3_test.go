package importer_test

import (
	"os"
	"strings"
	"testing"
)

// forbiddenFileReadCalls mirrors krill/store's own list of the same name:
// nothing downstream of ImportCompletion.SourcePath may open the file it
// names -- SourcePath is an audit-trail-only record of where an import
// came from, never a second readable (or writable) source of truth for
// the imported content (NFR3, LB5).
var forbiddenFileReadCalls = []string{"os.Open(", "os.ReadFile(", "ioutil.ReadFile("}

// TestNFR3_ImporterSourceNeverOpensSourcePath is the importer-side half of
// the NFR3 structural check issue #2548's Testing item 8 names:
// refuseIfAlreadyImported (importer.go) reads SourcePath back only to
// *compare* it against the requested --path -- never to open a file.
// This reads importer.go's own source back (declared as `data` on this
// test's BUILD.bazel target) and asserts it never calls a file-opening
// function. If a future change adds a file-read call to importer.go, this
// test goes red and names the file directly.
//
// This is deliberately narrow, not a repo-wide guarantee: a grep across
// krill/ at the time this test was written found exactly one other file
// referencing SourcePath, krill/store/import_completion.go, which carries
// the matching check, TestNFR3_ImportCompletionSourceNeverOpensAFile. A
// third reference added anywhere else in krill/ needs its own copy of
// this check.
func TestNFR3_ImporterSourceNeverOpensSourcePath(t *testing.T) {
	src, err := os.ReadFile("importer.go")
	if err != nil {
		t.Fatalf("read importer.go back (is it declared as `data` on this go_test target?): %v", err)
	}
	body := string(src)
	for _, forbidden := range forbiddenFileReadCalls {
		if strings.Contains(body, forbidden) {
			t.Errorf("importer.go must never open a file by SourcePath (NFR3) -- found %q", forbidden)
		}
	}
}
