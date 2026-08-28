// Source-analysis coverage for FR72's read-path repoint requirement: "the
// corrected view's cost profile changed materially and the API read path
// deliberately does not use it." No build tag -- this test needs no
// database at all, so it runs in every plain `bazel test //leaflab/...`,
// not just the Docker-gated integration suite. readings.go's contents are
// embedded at compile time (go:embed) rather than read from the working
// directory at run time, since a Bazel test binary's runtime cwd is not
// this package's source directory -- see this repo's other go:embed users
// (e.g. leaflab/migrate) for the same pattern.
package readings

import (
	_ "embed"
	"strings"
	"testing"
)

//go:embed readings.go
var readingsSource string

// forbiddenView is the corrected FR72 view this package's read path must
// never touch -- see readings.go's package doc comment ("Never through
// v_sensor_reading_with_plant").
const forbiddenView = "v_sensor_reading_with_plant"

// sqlUsageMarkers are substrings that, present on the same line as
// forbiddenView, indicate the name is being used as a live SQL relation
// reference rather than mentioned in prose (a comment or doc string).
// This intentionally does not simply assert forbiddenView is entirely
// absent -- readings.go's own doc comments legitimately name the view as
// the thing they explicitly avoid, and that prose must stay, not be
// scrubbed just to make this test pass trivially.
var sqlUsageMarkers = []string{"FROM " + forbiddenView, "JOIN " + forbiddenView, "from " + forbiddenView, "join " + forbiddenView}

// TestReadPathNeverQueriesCorrectedPlantView is a source-analysis test
// (FR72, this task's Testing section: "a source-analysis or query-log
// test") over readings.go: it may not contain a live SQL reference
// (FROM/JOIN) to v_sensor_reading_with_plant. Series is served from the
// tiers with attribution applied above the aggregate
// (seriesForPlant/aggregatedPointsForPlantInterval), never through that
// view.
func TestReadPathNeverQueriesCorrectedPlantView(t *testing.T) {
	if readingsSource == "" {
		t.Fatal("test setup: embedded readings.go source is empty")
	}
	for _, line := range strings.Split(readingsSource, "\n") {
		trimmed := strings.TrimSpace(line)
		// Skip comment lines entirely -- readings.go's own package doc
		// comment legitimately names the view in prose describing what
		// NOT to do.
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		for _, marker := range sqlUsageMarkers {
			if strings.Contains(line, marker) {
				t.Errorf("readings.go contains a live SQL reference to %s: %q", forbiddenView, trimmed)
			}
		}
	}
}
