// Source-analysis coverage for FR26.2's "marked, never rewritten" rule:
// retroactive re-stamping of a reading's stored region_id is permanently
// out of scope, not deferred (this task's own text). No build tag -- this
// test needs no database, so it runs in every plain `bazel test
// //leaflab/...`, same rationale as source_analysis_test.go. Source files
// are embedded at compile time (go:embed), same pattern as
// source_analysis_test.go, since a Bazel test binary's runtime cwd is not
// this package's source directory.
package readings

import (
	_ "embed"
	"regexp"
	"strings"
	"testing"
)

//go:embed readings.go
var noRestampReadingsSource string

//go:embed suspect_detect.go
var noRestampSuspectDetectSource string

//go:embed suspect_ranges.go
var noRestampSuspectRangesSource string

// updateRegionIDPattern matches an UPDATE statement writing sensor_reading's
// region_id column -- case-insensitively, and tolerant of the exact target
// list an UPDATE ... SET clause might use (region_id = ..., or a list that
// includes it) -- the shape FR26.2 forbids anywhere in this package: every
// suspect.Check this package computes is a response-side annotation, never
// a write back to the row a reading was originally stamped with.
var updateRegionIDPattern = regexp.MustCompile(`(?i)UPDATE\s+sensor_reading\b[^;]*\bSET\b[^;]*\bregion_id\s*=`)

// TestNoRegionIDRestamping asserts no source file in this package contains
// an UPDATE statement that writes sensor_reading.region_id -- this task's
// Implementation section: "Add a test asserting no code path updates
// sensor_reading.region_id after insert." Checked against every literal
// SQL string this package embeds, not just readings.go, since a future
// suspect-detection helper is exactly the kind of file that could
// accidentally reintroduce a write-back.
func TestNoRegionIDRestamping(t *testing.T) {
	sources := map[string]string{
		"readings.go":       noRestampReadingsSource,
		"suspect_detect.go": noRestampSuspectDetectSource,
		"suspect_ranges.go": noRestampSuspectRangesSource,
	}
	for name, source := range sources {
		if source == "" {
			t.Fatalf("test setup: embedded %s source is empty", name)
		}
		// Strip comment lines before matching, same rationale as
		// source_analysis_test.go: this file's own doc comments and the
		// production files' doc comments legitimately describe the
		// forbidden statement in prose (e.g. FR26.2's "marked, never
		// rewritten" explanation), and that prose must stay, not be
		// scrubbed just to make this test pass trivially.
		var codeOnly strings.Builder
		for _, line := range strings.Split(source, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			codeOnly.WriteString(line)
			codeOnly.WriteString("\n")
		}
		if loc := updateRegionIDPattern.FindString(codeOnly.String()); loc != "" {
			t.Errorf("%s contains a live UPDATE ... SET region_id statement against sensor_reading: %q -- FR26.2 forbids retroactive re-stamping", name, loc)
		}
	}
}
