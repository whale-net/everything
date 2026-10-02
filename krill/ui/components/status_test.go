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

// taskLanes and taskStates are krill's lane and task-state vocabularies,
// spelled out rather than read from the store types for the same reason
// milestoneStatuses is.
var taskLanes = []string{"Scaffold", "Implementation", "Testing", "Validation", "Done"}

var taskStates = []string{"escalated", "claimed", "lease-expired", "ready", "capped", "cancelled"}

// neutralStyle is the unknown-value fallback both task mappers share.
var neutralStyle = StatusStyle{htmxui.BadgeNeutral, htmxui.BadgeSizeSM, false}

func TestTaskLaneStyle_EveryLaneIsVisuallyDistinct(t *testing.T) {
	seen := map[StatusStyle]string{}
	for _, lane := range taskLanes {
		style := TaskLaneStyle(lane)
		if other, dup := seen[style]; dup {
			t.Errorf("%q and %q render identically (%+v)", lane, other, style)
		}
		seen[style] = lane
	}
	for _, lane := range taskLanes {
		assert.NotEqual(t, neutralStyle, TaskLaneStyle(lane),
			"%q must not fall through to the unknown-lane fallback", lane)
	}
}

func TestTaskStateStyle_EveryStateIsVisuallyDistinct(t *testing.T) {
	// The same (variant, soft) tuple rule as the milestone statuses:
	// "escalated" and "cancelled" share a hue and are separated by the
	// soft treatment, so distinctness is over the tuple, not the variant.
	seen := map[StatusStyle]string{}
	for _, state := range taskStates {
		style := TaskStateStyle(state)
		if other, dup := seen[style]; dup {
			t.Errorf("%q and %q render identically (%+v)", state, other, style)
		}
		seen[style] = state
	}
	for _, state := range taskStates {
		assert.NotEqual(t, neutralStyle, TaskStateStyle(state),
			"%q must not fall through to the unknown-state fallback", state)
	}
}

func TestTaskStyle_UnknownValueUsesNeutralFallback(t *testing.T) {
	assert.Equal(t, neutralStyle, TaskLaneStyle("some-future-lane"))
	assert.Equal(t, neutralStyle, TaskStateStyle("some-future-state"))
}

func TestTaskStyle_ColourSemanticsFollowTheDesignSystem(t *testing.T) {
	// The lanes read as progress towards Done; the states read as how
	// much attention a task needs. A Done task is shipped (success), an
	// escalated one needs a human (error), a capped or lapsed one is in
	// flight but at its limit (warning), and an unclaimed one is nobody's
	// problem yet (ghost).
	assert.Equal(t, htmxui.BadgeSuccess, TaskLaneStyle("Done").Variant)
	assert.Equal(t, htmxui.BadgeError, TaskStateStyle("escalated").Variant)
	assert.Equal(t, htmxui.BadgeWarning, TaskStateStyle("lease-expired").Variant)
	assert.Equal(t, htmxui.BadgeGhost, TaskStateStyle("ready").Variant)
}
