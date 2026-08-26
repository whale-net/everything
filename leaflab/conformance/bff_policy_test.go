package conformance

import (
	"regexp"
	"testing"
)

// TestBFFPolicy_NoRoundingOrCoarsening is the NFR18.1 conformance check:
// the BFF (browser-facing UI component) holds no rounding, coarsening,
// suppression, or label-selection rule.
//
// This test parses the leaflab/ui source tree and verifies that:
// - No data transformation rules are applied to responses from leaflab-api
// - Data is passed through to the browser without modification
// - All policy decisions remain in the API service layer
//
// If presentation-shaping rules are added to the BFF, this test FAILS the build.
func TestBFFPolicy_NoRoundingOrCoarsening(t *testing.T) {
	files := globGoFiles(t, "ui")

	// Patterns that indicate data transformation rules in the BFF
	// These include math operations on sensor data, filtering, or re-labeling
	forbiddenPatterns := []struct {
		name    string
		pattern *regexp.Regexp
	}{
		{
			name:    "rounding function calls (math.Round, etc)",
			pattern: regexp.MustCompile(`(?i)\b(math\.Round|Round|round)\s*\(`),
		},
		{
			name:    "floor/ceiling operations",
			pattern: regexp.MustCompile(`(?i)\b(math\.Floor|math\.Ceil|Floor|Ceil)\s*\(`),
		},
		{
			name:    "suppression/filtering of data fields",
			pattern: regexp.MustCompile(`(?i)\b(suppress|filter|omit)\s*\(`),
		},
		{
			name:    "label or description rewriting",
			pattern: regexp.MustCompile(`(?i)\b(RelabelSensor|RenameSensor|RenameField|RenameKey)\s*\(`),
		},
		{
			name:    "bit-shifting or scaling operations on sensor data",
			pattern: regexp.MustCompile(`(?i)SensorValue\s*[*/<>]=?\s*[0-9]`),
		},
	}

	for filePath, src := range files {
		for _, fp := range forbiddenPatterns {
			if fp.pattern.MatchString(src) {
				t.Errorf("%s: contains %s -- the BFF must hold no data transformation rules; "+
					"rounding, coarsening, suppression, and label-selection belong in the API layer (NFR18.1)",
					filePath, fp.name)
			}
		}
	}
}
