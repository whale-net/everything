package conformance

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// nfr181Forbidden are word-boundary-safe patterns for the four shaping
// operations NFR18.1 reserves to leaflab-api: rounding, coarsening,
// suppression, and label selection. Matched case-insensitively against Go
// and templ source text (not AST -- a source-analysis check, mirroring
// tools/app_registry/conformance's style, not a behavioral test). Mirrors
// leaflab/ui/nfr18_conformance_test.go's placeholder pattern list -- kept
// in sync manually since that file scans only leaflab/ui itself and this
// one is the real, cross-package (ui + ui/components) version referenced
// by that file's doc comment.
var nfr181Forbidden = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bmath\.Round\b`),
	regexp.MustCompile(`(?i)\bround\(`),
	regexp.MustCompile(`(?i)\bcoarsen`),
	regexp.MustCompile(`(?i)\bsuppress\(`),
	regexp.MustCompile(`(?i)\bselectlabel\b`),
	regexp.MustCompile(`(?i)\blabelselect`),
}

// TestBFFPolicy_NoRoundingCoarseningSuppressionOrLabelSelectionHelper is
// NFR18.1's conformance check: leaflab/ui holds no rounding, coarsening,
// suppression or label-selection rule -- that shaping belongs in
// leaflab-api. A source-analysis check over identifiers and imports (e.g.
// no math.Round, no local formatting of numeric measurement values), not a
// behavioral test.
//
// Scans leaflab/ui and leaflab/ui/components -- every checked-in .go and
// .templ source, excluding _test.go files (a test's own fixtures/patterns,
// like this file's own nfr181Forbidden literals, must not trip the check
// on themselves; leaflab/ui's conformance_srcs filegroup is glob(["*.go"])
// and includes test files, so this exclusion is load-bearing, not
// defensive dead code).
func TestBFFPolicy_NoRoundingCoarseningSuppressionOrLabelSelectionHelper(t *testing.T) {
	files := map[string]string{}
	for k, v := range globFilesWithExt(t, "ui", ".go", ".templ") {
		files[k] = v
	}
	for k, v := range globFilesWithExt(t, "ui/components", ".go", ".templ") {
		files[k] = v
	}
	if len(files) < 5 {
		// Guards the guard: if the data dependencies silently stopped
		// resolving (e.g. a filegroup renamed), every check below would
		// vacuously pass. The real ui/ + ui/components tree has well
		// over a dozen source files.
		t.Fatalf("only found %d ui source files -- check the ui:conformance_srcs / ui/components:conformance_srcs data dependencies in BUILD.bazel", len(files))
	}

	for _, msg := range nfr181Offenses(files) {
		t.Error(msg)
	}
}

// nfr181Offenses computes NFR18.1's forbidden-pattern failures: for every
// non-_test.go file in files whose content matches one of nfr181Forbidden,
// returns the failure message
// TestBFFPolicy_NoRoundingCoarseningSuppressionOrLabelSelectionHelper would
// report for it. Factored out of that test's loop so a synthetic fixture
// (negative_fixtures_test.go) can assert on violation messages directly.
func nfr181Offenses(files map[string]string) []string {
	var msgs []string
	for path, content := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, re := range nfr181Forbidden {
			if re.MatchString(content) {
				msgs = append(msgs, fmt.Sprintf("%s: matches forbidden pattern %s -- NFR18.1: leaflab/ui holds no rounding/"+
					"coarsening/suppression/label-selection rule; move this shaping into leaflab-api "+
					"instead", path, re.String()))
			}
		}
	}
	return msgs
}
