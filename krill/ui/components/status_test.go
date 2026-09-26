package components

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// milestoneStatuses is krill's full status vocabulary, spelled out rather
// than read from the store type -- see nav_test.go's requiredAreas for why
// a test must not derive its expectations from the thing it checks.
var milestoneStatuses = []string{
	"not started",
	"in design",
	"designed",
	"planned",
	"in progress",
	"shipped",
	"partially complete",
	"abandoned",
}

func TestMilestoneStatusStyle_EveryStatusIsVisuallyDistinct(t *testing.T) {
	// The point of the style tuple: "in progress" and "partially
	// complete" share a hue and are separated by the soft treatment, so
	// distinctness must be checked over (variant, soft), not variant
	// alone.
	seen := map[StatusStyle]string{}
	for _, status := range milestoneStatuses {
		style := MilestoneStatusStyle(status)
		if other, dup := seen[style]; dup {
			t.Errorf("%q and %q render identically (%+v)", status, other, style)
		}
		seen[style] = status
	}
}

func TestMilestoneStatusStyle_NoKnownStatusFallsBackToNeutral(t *testing.T) {
	// The neutral fallback is reserved for values this function does not
	// know, so a new status that silently picked it would be invisible.
	neutral := StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}
	for _, status := range milestoneStatuses {
		assert.NotEqual(t, neutral, MilestoneStatusStyle(status),
			"%q must not fall through to the unknown-status fallback", status)
	}
}

func TestMilestoneStatusStyle_UnknownStatusUsesNeutralFallback(t *testing.T) {
	assert.Equal(t,
		StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false},
		MilestoneStatusStyle("some-future-status"))
}

func TestMilestoneStatusStyle_ColourSemanticsFollowTheDesignSystem(t *testing.T) {
	// manmanv2/ui/DESIGN_SYSTEM.md via htmxui §6: success for shipped,
	// error for abandoned, warning for in-flight work.
	assert.Equal(t, htmxui.BadgeSuccess, MilestoneStatusStyle("shipped").Variant)
	assert.Equal(t, htmxui.BadgeError, MilestoneStatusStyle("abandoned").Variant)
	assert.Equal(t, htmxui.BadgeWarning, MilestoneStatusStyle("in progress").Variant)
}
