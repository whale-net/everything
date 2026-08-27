package conformance

import "testing"

// TestBFFPolicy_NoRoundingCoarseningSuppressionOrLabelSelectionHelper is
// NFR18.1's conformance check: leaflab/ui holds no rounding, coarsening,
// suppression or label-selection rule -- that shaping belongs in
// leaflab-api. A source-analysis check over identifiers and imports (e.g.
// no math.Round, no local formatting of numeric measurement values), not a
// behavioral test.
//
// This is the real, cross-package version of the grep-style placeholder in
// leaflab/ui/nfr18_conformance_test.go (#1329's Testing section) -- see
// this package's doc comment in paths_test.go. Once this check is filled
// in, the leaflab/ui placeholder's doc comment referencing "the NFR1.a task
// later on this plan" is satisfied; whether to remove the now-redundant
// placeholder is an Implementation-phase call, not a Scaffold one.
//
// TODO(Implementation phase): walk globGoFiles(t, "ui") and
// globGoFiles(t, "ui/components") (plus any .templ sources exposed via
// //leaflab/ui/components:conformance_srcs) for forbidden patterns
// (math.Round, round(, coarsen, suppress(, label-selection helpers),
// failing with the offending file and pattern named.
func TestBFFPolicy_NoRoundingCoarseningSuppressionOrLabelSelectionHelper(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR18.1): scan leaflab/ui + leaflab/ui/components source (via globGoFiles/conformance_srcs data) for rounding/coarsening/suppression/label-selection helpers; fail naming the offending file and pattern.")
}
