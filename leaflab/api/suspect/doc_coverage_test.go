package suspect

// Doc-coverage conformance check for NFR17 (Phase-3 attribution
// documentation, #1367's Testing section): "A test asserting every
// suspect-check identifier registered in leaflab/api/suspect/ is
// documented -- this fails when a check is added without documentation."
//
// leaflab/conformance/ (the shared conformance suite referenced by #1367)
// does not exist in this worktree's ancestry yet -- it is created by a
// sibling task (#1366) not merged into this branch. This is the local,
// same-package placeholder the situation calls for, mirroring
// leaflab/ui/nfr18_conformance_test.go's precedent of a same-package check
// standing in for a not-yet-available shared conformance package.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findLeaflabDataMD locates leaflab/DATA.md by walking up from the test's
// working directory, mirroring leaflab/ui/nfr18_conformance_test.go's
// leaflabUIDir marker-file pattern. Needed because go:embed cannot reach
// leaflab/DATA.md from this package (leaflab/api/suspect) with a ".."
// pattern, so DATA.md is staged as a plain `data` dependency instead (see
// BUILD.bazel) and read from the runfiles tree at test time.
func findLeaflabDataMD(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../..", "../../../../.."} {
		p := filepath.Join(c, "leaflab", "DATA.md")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	t.Fatal("could not locate leaflab/DATA.md -- check the data dependency in BUILD.bazel")
	return ""
}

// TestAllChecks_DocumentedInDataMD proves FR26.3's Check registry and
// DATA.md's "Suspect Checks" table never silently drift apart: every
// identifier All() returns must have a corresponding backtick-quoted entry
// in DATA.md. Deliberately checks the registry (source of truth) against
// the doc, not the other way around, so a check added to this package
// without a matching doc row fails here.
func TestAllChecks_DocumentedInDataMD(t *testing.T) {
	path := findLeaflabDataMD(t)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(b)

	if !strings.Contains(content, "Suspect Checks") {
		t.Fatalf("%s: missing a \"Suspect Checks\" section -- guard is vacuous, check the data dependency in BUILD.bazel", path)
	}

	all := All()
	if len(all) == 0 {
		t.Fatal("All() returned no checks -- guard is vacuous")
	}
	for _, c := range all {
		marker := "`" + c.String() + "`"
		if !strings.Contains(content, marker) {
			t.Errorf("%s: suspect check %q (registered in leaflab/api/suspect.All()) has no %s entry -- document it in DATA.md's suspect-check table", path, c.String(), marker)
		}
	}
}
