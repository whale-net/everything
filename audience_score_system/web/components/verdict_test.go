package components

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/audience_score_system/store"
)

// TestVerdictGlyph_EveryDeclaredValue_MapsToDistinctNonEmptyGlyph is
// FR32's core coverage: every store.VerdictValue declared in
// store/models.go maps, through VerdictGlyph's explicit lookup (never a
// boolean branch), to its own non-empty glyph -- no two known verdicts
// ever collapse onto the same glyph.
func TestVerdictGlyph_EveryDeclaredValue_MapsToDistinctNonEmptyGlyph(t *testing.T) {
	values := []store.VerdictValue{
		store.VerdictViable,
		store.VerdictNotViable,
		store.VerdictNeedsMoreResearch,
	}

	seen := make(map[string]store.VerdictValue, len(values))
	for _, v := range values {
		glyph := VerdictGlyph(v)
		assert.NotEmpty(t, glyph, "VerdictGlyph(%q) must not be empty", v)

		if other, ok := seen[glyph]; ok {
			t.Errorf("VerdictGlyph(%q) and VerdictGlyph(%q) both render glyph %q -- every declared VerdictValue must have a distinct glyph", v, other, glyph)
		}
		seen[glyph] = v

		title := VerdictGlyphTitle(v)
		assert.NotEmpty(t, title, "VerdictGlyphTitle(%q) must not be empty", v)
	}

	assert.Equal(t, "✅", VerdictGlyph(store.VerdictViable))
	assert.Equal(t, "❌", VerdictGlyph(store.VerdictNotViable))
	assert.Equal(t, "🔍", VerdictGlyph(store.VerdictNeedsMoreResearch))
}

// TestVerdictGlyph_NoVerdictGlyph_DistinctFromEveryKnownGlyph proves the
// "no verdict recorded yet" state renders its own glyph, never reusing
// one of the three known-verdict glyphs.
func TestVerdictGlyph_NoVerdictGlyph_DistinctFromEveryKnownGlyph(t *testing.T) {
	assert.NotEmpty(t, NoVerdictGlyph)

	known := []store.VerdictValue{
		store.VerdictViable,
		store.VerdictNotViable,
		store.VerdictNeedsMoreResearch,
	}
	for _, v := range known {
		assert.NotEqual(t, NoVerdictGlyph, VerdictGlyph(v), "NoVerdictGlyph must differ from VerdictGlyph(%q)", v)
	}
}

// TestVerdictGlyph_UnrecognizedValue_RendersExplicitUnknownMarker is
// FR32's regression guard: a store.VerdictValue that isn't one of the
// three declared constants must render an explicit "unknown" marker, not
// a silent fallback to any of the known glyphs (and not NoVerdictGlyph
// either -- an unrecognized value is a distinct failure mode from "no
// verdict yet"). This is the case a future added VerdictValue would hit
// if VerdictGlyph's lookup weren't updated alongside it.
func TestVerdictGlyph_UnrecognizedValue_RendersExplicitUnknownMarker(t *testing.T) {
	unrecognized := store.VerdictValue("something-new")

	glyph := VerdictGlyph(unrecognized)
	assert.NotEmpty(t, glyph)
	assert.NotEqual(t, NoVerdictGlyph, glyph, "an unrecognized VerdictValue must not render as NoVerdictGlyph")

	known := []store.VerdictValue{
		store.VerdictViable,
		store.VerdictNotViable,
		store.VerdictNeedsMoreResearch,
	}
	for _, v := range known {
		assert.NotEqual(t, VerdictGlyph(v), glyph, "an unrecognized VerdictValue must not silently reuse VerdictGlyph(%q)'s glyph", v)
	}

	title := VerdictGlyphTitle(unrecognized)
	assert.NotEmpty(t, title)
	for _, v := range known {
		assert.NotEqual(t, VerdictGlyphTitle(v), title, "an unrecognized VerdictValue must not silently reuse VerdictGlyphTitle(%q)'s title", v)
	}
}
