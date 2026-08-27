package main

// NFR19 grep-style conformance check (#1330's Testing section): "no
// string matching publishable|shareable|public_" anywhere under
// leaflab/ui/ -- "No V1 field, flag, column or default is described as
// 'publishable' or 'shareable'... private by default, so that when share
// tokens land the safe default is already the existing one."
//
// Reuses leaflabUIDir (nfr18_conformance_test.go, same package) to locate
// leaflab/ui via its own BUILD.bazel as a marker file, and the same data
// dependency (this package's own *.go glob plus
// //leaflab/ui/components:conformance_srcs) so the walk covers both the
// main package's handlers and the components package's .templ/elapsed.go
// sources -- everything the issue means by "leaflab/ui/".

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// nfr19Forbidden matches "publishable", "shareable", or a "public_"
// prefix, case-insensitively -- word-boundary-safe so it doesn't also flag
// unrelated words like "publication" or "publicity".
var nfr19Forbidden = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bpublishable\b`),
	regexp.MustCompile(`(?i)\bshareable\b`),
	regexp.MustCompile(`(?i)\bpublic_`),
}

func TestNFR19_NoPublishableShareablePublicUnderscoreStrings(t *testing.T) {
	dir := leaflabUIDir(t)

	found := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "design" {
				// Static reference wireframes, not shipped code -- out of
				// scope, same exclusion nfr18_conformance_test.go makes.
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".templ" {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			// This file's own patterns (and any other test's fixture
			// strings) must not trip the check on themselves.
			return nil
		}

		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		found++
		content := string(b)
		for _, re := range nfr19Forbidden {
			if re.MatchString(content) {
				t.Errorf("%s: matches forbidden pattern %s -- NFR19: private by default, no field/flag/column/default described as publishable or shareable", path, re.String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found < 3 {
		// Guards the guard: if the data dependency silently stopped
		// resolving, this check would vacuously pass.
		t.Fatalf("only scanned %d source files under %s -- check the data dependency in BUILD.bazel", found, dir)
	}
}

// TestNFR19_DeviceIdentifierRendersAsSupportDetailNotTitleOrLink is the
// positive half of NFR19 for this screen specifically ("Device identifiers
// render as copyable support details, never as page titles"): asserts
// boards.templ's device id cell carries the monospace/support-detail
// styling and title tooltip the scaffold's design settled on, and is never
// wrapped in an <a href> (never a link) nor rendered as a heading element.
func TestNFR19_DeviceIdentifierRendersAsSupportDetailNotTitleOrLink(t *testing.T) {
	dir := leaflabUIDir(t)
	path := filepath.Join(dir, "components", "boards.templ")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(b)

	if !strings.Contains(content, `font-mono`) {
		t.Errorf("%s: expected the device id cell to carry monospace styling (copyable support detail, NFR19), got:\n%s", path, content)
	}
	if !strings.Contains(content, `Device identifier`) {
		t.Errorf("%s: expected a support-detail title/tooltip labelling the device id cell, got:\n%s", path, content)
	}
	if regexp.MustCompile(`<a\b[^>]*>\s*\{\s*b\.GetDeviceId\(\)`).MatchString(content) {
		t.Errorf("%s: device id must never render as a link (<a href>) -- NFR19: never a page title or a link", path)
	}
	if regexp.MustCompile(`<h[1-6]\b[^>]*>\s*\{\s*b\.GetDeviceId\(\)`).MatchString(content) {
		t.Errorf("%s: device id must never render as a heading -- NFR19: never a page title", path)
	}
}
