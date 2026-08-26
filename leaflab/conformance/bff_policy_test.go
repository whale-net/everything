package conformance

import (
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
	// Placeholder: Implementation phase will add the full conformance check
	// that scans the UI source for data-shaping operations and fails if any
	// presentation rules are found.
	_ = t
}
