package importer_test

// This is the non-manual half of the drift protection
// krill/conformance/whagent_net_import_integration_test.go's package doc
// describes (FR11, issue #2629): that test's own capability/milestone
// counts are derived from whagent_net's real files at run time (rather
// than hardcoded), but the target carrying it is `manual` -- it needs a
// real Postgres (see //libs/go/dbtest's README) -- so it does not run
// under default `bazel test //...`. This test needs no database: it
// parses the same real, committed whagent_net doc set and re-derives its
// own expected capability and milestone counts by counting the matching
// lines directly in the source files, independently of Parse's own
// regexes. A perfectly ordinary additive change to whagent_net's brief
// never breaks it; a genuine Parse regression (missing a line,
// double-counting one) still does.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/bazelbuild/rules_go/go/runfiles"

	"github.com/whale-net/everything/krill/importer"
)

var (
	smokeCapabilityLinePattern   = regexp.MustCompile(`(?m)^C\d+ — `)
	smokeMilestoneHeadingPattern = regexp.MustCompile(`(?m)^### M\d+ `)
)

func whagentNetDocsRootForSmoke(t *testing.T) string {
	t.Helper()
	productMD, err := runfiles.Rlocation("_main/whagent_net/PRODUCT.md")
	if err != nil {
		t.Fatalf("runfiles.Rlocation(whagent_net/PRODUCT.md): %v (is //whagent_net:docs still a data dep of this test target?)", err)
	}
	return filepath.Dir(productMD)
}

func countSmokeMatches(t *testing.T, path string, re *regexp.Regexp) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return len(re.FindAllIndex(b, -1))
}

// TestSmoke_WhagentNetParse_CapabilityAndMilestoneCountsMatchSourceFiles
// parses whagent_net's real doc set and checks Parse's own capability and
// milestone counts against an independent count of the same source
// files, run by default (`bazel test //...`), so this class of drift
// (krill/conformance's manual FR11 conformance test hardcoding a snapshot
// of whagent_net's brief that later goes stale) is also caught outside a
// manual run.
func TestSmoke_WhagentNetParse_CapabilityAndMilestoneCountsMatchSourceFiles(t *testing.T) {
	root := whagentNetDocsRootForSmoke(t)

	parsed, err := importer.Parse(root)
	if err != nil {
		t.Fatalf("Parse(whagent_net's real doc set): %v", err)
	}

	gotCaps := 0
	for _, b := range parsed.Buckets {
		gotCaps += len(b.Capabilities)
	}
	wantCaps := countSmokeMatches(t, filepath.Join(root, "product", "02-capability-map.md"), smokeCapabilityLinePattern)
	if gotCaps != wantCaps {
		t.Errorf("Parse found %d capabilities, want %d (count of `C<n> —` lines in 02-capability-map.md)", gotCaps, wantCaps)
	}

	wantMilestones := countSmokeMatches(t, filepath.Join(root, "product", "03-roadmap.md"), smokeMilestoneHeadingPattern)
	if len(parsed.Milestones) != wantMilestones {
		t.Errorf("Parse found %d milestones, want %d (count of `### M<n>` headings in 03-roadmap.md)", len(parsed.Milestones), wantMilestones)
	}
}
