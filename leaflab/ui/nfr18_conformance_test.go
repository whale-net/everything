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
// nfr62LandingImportPath is leaflab/api/landing's import path (FR62's
// server-side five-condition classifier, #1350). This task's Testing
// section: "The classifier is called from the service, never from
// leaflab/ui/" -- leaflab/ui has not yet built the household landing
// screen (see leaflab/api/landing.go's doc comment); this guard fails the
// moment a future change imports the classifier directly into the BFF
// instead of rendering whatever leaflab-api's GetHouseholdLanding RPC
// already decided (NFR18.1).
const nfr62LandingImportPath = `"github.com/whale-net/everything/leaflab/api/landing"`

// TestClassifierNeverImportedFromUI is a source-analysis guard, same
// grep-over-checked-in-source shape as
// TestNFR18_1Placeholder_NoRoundingCoarseningSuppressionOrLabelSelectionHelper
// above: leaflab/ui's own Go/templ source never imports
// leaflab/api/landing.
func TestClassifierNeverImportedFromUI(t *testing.T) {
	dir := leaflabUIDir(t)

	found := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "design" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			// This file's own constant above would otherwise trip itself.
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found++
		if strings.Contains(string(b), nfr62LandingImportPath) {
			t.Errorf("%s imports leaflab/api/landing -- FR62/NFR18.1: the classifier is called from the service, never from leaflab/ui/", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found < 3 {
		t.Fatalf("only scanned %d source files under %s -- check the data dependency in BUILD.bazel", found, dir)
	}
}
