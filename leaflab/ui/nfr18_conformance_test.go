package main

// NFR18.1 placeholder conformance check (#1329's Testing section): "the
// BFF holds no rounding, coarsening, suppression or label-selection rule."
// A real cross-package conformance suite (mirroring
// tools/app_registry/conformance) is the NFR1.a task later on this plan --
// this is deliberately the "local placeholder" the issue asks for: a
// same-package grep over leaflab/ui's own checked-in source that fails the
// moment someone adds presentation-shaping logic here instead of in
// leaflab-api.

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// leaflabUIDir locates leaflab/ui using its own BUILD.bazel (staged as a
// data dependency, see BUILD.bazel's ui_test data attribute) as a marker
// file. Bazel stages data relative to the runfiles root; a plain `go test`
// run from this package's own directory finds it by walking up. Mirrors
// tools/app_registry/conformance/paths_test.go's appRegistryDir.
func leaflabUIDir(t *testing.T) string {
	t.Helper()
	for _, c := range []string{".", "..", "../..", "../../..", "../../../.."} {
		marker := filepath.Join(c, "leaflab", "ui", "BUILD.bazel")
		if st, err := os.Stat(marker); err == nil && !st.IsDir() {
			return filepath.Join(c, "leaflab", "ui")
		}
	}
	t.Fatal("could not locate leaflab/ui/BUILD.bazel -- check the data dependency in BUILD.bazel")
	return ""
}

// nfr181Forbidden are word-boundary-safe patterns for the four shaping
// operations NFR18.1 reserves to the API: rounding, coarsening,
// suppression, and label selection. Matched case-insensitively against Go
// and templ source text (not AST -- this is the placeholder, not the full
// conformance check).
var nfr181Forbidden = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bmath\.Round\b`),
	regexp.MustCompile(`(?i)\bround\(`),
	regexp.MustCompile(`(?i)\bcoarsen`),
	regexp.MustCompile(`(?i)\bsuppress\(`),
	regexp.MustCompile(`(?i)\bselectlabel\b`),
	regexp.MustCompile(`(?i)\blabelselect`),
}

func TestNFR18_1Placeholder_NoRoundingCoarseningSuppressionOrLabelSelectionHelper(t *testing.T) {
	dir := leaflabUIDir(t)

	found := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Design wireframes are static reference HTML, not shipped
			// code -- out of scope for this conformance check.
			if d.Name() == "design" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".templ" {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			// Test doubles/helpers (like this file's own patterns above)
			// must not trip the check on themselves.
			return nil
		}

		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found++
		content := string(b)
		for _, re := range nfr181Forbidden {
			if re.MatchString(content) {
				t.Errorf("%s: matches forbidden pattern %s -- NFR18.1: the BFF holds no rounding/coarsening/"+
					"suppression/label-selection rule; that shaping belongs in leaflab-api", path, re.String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found < 3 {
		// Guards the guard: if the data dependency silently stopped
		// resolving, every check above would vacuously pass.
		t.Fatalf("only scanned %d source files under %s -- check the data dependency in BUILD.bazel", found, dir)
	}
}
