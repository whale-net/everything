package components

import "github.com/whale-net/everything/audience_score_system/store"

// NoVerdictGlyph is the glyph rendered when an Idea has no verdict recorded
// yet -- distinct from every store.VerdictValue glyph and from
// unknownVerdictGlyph, so the three states (has-a-known-verdict,
// no-verdict-yet, unrecognized-verdict) never collapse into each other.
const NoVerdictGlyph = "➖"

// unknownVerdictGlyph is the explicit fallback for a store.VerdictValue
// that isn't one of the three declared constants (FR32's regression
// guard). It must never be reused as a "real" glyph -- a value hitting
// this arm means a new VerdictValue was added to store/models.go without
// updating verdictGlyphs/verdictGlyphTitles below, and that should be
// visibly wrong (an unmistakable "?" glyph) rather than silently
// mislabeled as one of the known verdicts.
const unknownVerdictGlyph = "❓"

// unknownVerdictGlyphTitle is VerdictGlyphTitle's counterpart to
// unknownVerdictGlyph.
const unknownVerdictGlyphTitle = "Unknown verdict"

// verdictGlyphs is the explicit, exhaustive lookup FR32 requires: every
// declared store.VerdictValue maps to a single, distinct glyph. This is a
// map (not a boolean branch) precisely so a future VerdictValue that isn't
// added here falls through to VerdictGlyph's explicit unknownVerdictGlyph
// default rather than silently reusing one of these.
var verdictGlyphs = map[store.VerdictValue]string{
	store.VerdictViable:            "✅",
	store.VerdictNotViable:         "❌",
	store.VerdictNeedsMoreResearch: "🔍",
}

// verdictGlyphTitles is VerdictGlyphTitle's backing lookup, reusing
// research.verdictLabel's existing text so the glyph's accessible label
// matches what the UI called this verdict before FR31/FR32's badge->glyph
// swap.
var verdictGlyphTitles = map[store.VerdictValue]string{
	store.VerdictViable:            "Viable",
	store.VerdictNotViable:         "Not viable",
	store.VerdictNeedsMoreResearch: "Needs more research",
}

// VerdictGlyph returns the single-glyph indicator for v, driven by the
// explicit verdictGlyphs lookup (FR32) -- never a boolean branch. An
// unrecognized v (not one of the three declared store.VerdictValue
// constants) renders unknownVerdictGlyph, not a silent reuse of an
// existing glyph.
func VerdictGlyph(v store.VerdictValue) string {
	if glyph, ok := verdictGlyphs[v]; ok {
		return glyph
	}
	return unknownVerdictGlyph
}

// VerdictGlyphTitle returns the accessible/hover label for v, mirroring
// research.verdictLabel's text so every surface's title/aria-label reads
// identically to what the pre-glyph badge said.
func VerdictGlyphTitle(v store.VerdictValue) string {
	if title, ok := verdictGlyphTitles[v]; ok {
		return title
	}
	return unknownVerdictGlyphTitle
}
