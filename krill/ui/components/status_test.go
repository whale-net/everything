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

// nonGoalKinds is krill's non-goal kind vocabulary, spelled out rather
// than read from the store type -- the same reason milestoneStatuses is.
var nonGoalKinds = []string{"permanent", "deferred"}

func TestNonGoalKindStyle_EveryKindIsVisuallyDistinct(t *testing.T) {
	// Distinctness is the point: the two kinds are told apart by the badge
	// as well as by the section heading, so identical tuples would leave a
	// row read on its own unable to say which kind it is.
	seen := map[StatusStyle]string{}
	for _, kind := range nonGoalKinds {
		style := NonGoalKindStyle(kind)
		if other, dup := seen[style]; dup {
			t.Errorf("%q and %q render identically (%+v)", kind, other, style)
		}
		seen[style] = kind
	}
	for _, kind := range nonGoalKinds {
		assert.NotEqual(t, neutralStyle, NonGoalKindStyle(kind),
			"%q must not fall through to the unknown-kind fallback", kind)
	}
}

func TestNonGoalKindStyle_UnknownKindUsesNeutralFallback(t *testing.T) {
	assert.Equal(t, neutralStyle, NonGoalKindStyle("some-future-kind"))
}

func TestNonGoalKindStyle_SeparatesSettledFromStillOpenWithoutSeverity(t *testing.T) {
	// Permanent is a closed boundary (secondary/slate); deferred is
	// explicitly not foreclosed, so it stays live-looking (info/indigo).
	assert.Equal(t, htmxui.BadgeSecondary, NonGoalKindStyle("permanent").Variant)
	assert.Equal(t, htmxui.BadgeInfo, NonGoalKindStyle("deferred").Variant)

	// Neither kind is a failure and neither is in flight, so neither may
	// take a severity colour: the Spec page is read-only and its body
	// carries no attention content, and a warning badge on a non-goal
	// would put a settled design decision in the same register as a task
	// that needs a human.
	for _, kind := range nonGoalKinds {
		variant := NonGoalKindStyle(kind).Variant
		assert.NotContains(t, []htmxui.BadgeVariant{
			htmxui.BadgeError, htmxui.BadgeWarning, htmxui.BadgeSuccess,
		}, variant, "%q must not render as a severity colour", kind)
	}
}

func TestNonGoalKindLabel_HumanWordingForEveryKindAndUnknownValues(t *testing.T) {
	// The wire values are lowercase; the badge an operator reads is not.
	assert.Equal(t, "Permanent", NonGoalKindLabel("permanent"))
	assert.Equal(t, "Deferred", NonGoalKindLabel("deferred"))
	// An unrecognised kind shows its own value rather than an empty badge,
	// so a third kind added later is still readable rather than blank.
	assert.Equal(t, "some-future-kind", NonGoalKindLabel("some-future-kind"))
}

// designSessionStages is store.Stage's full wire vocabulary, spelled out
// here rather than imported: this package carries no //krill/store
// dependency by design, and a test that read the list off the store type
// would silently agree with any future edit to it.
var designSessionStages = []string{
	"opened",
	"approved",
	"changes_requested",
	"architect_review",
	"in_draft",
	"answered",
	"ruled",
}

// designSessionStageLabels is the operator wording for each of those, in
// the same order. Snake_cased wire values are not what an operator reads.
var designSessionStageLabels = []string{
	"opened",
	"approved",
	"changes requested",
	"architect review",
	"in draft",
	"answered",
	"ruled",
}

// TestDesignSessionStageStyle_EveryStageIsVisuallyDistinct: the seven
// stages are told apart by the (variant, soft) tuple, which is what keeps
// the two info stages -- the two rounds an operator is waiting on -- from
// rendering identically.
func TestDesignSessionStageStyle_EveryStageIsVisuallyDistinct(t *testing.T) {
	seen := map[StatusStyle]string{}
	for _, stage := range designSessionStages {
		style := DesignSessionStageStyle(stage)
		if other, clash := seen[style]; clash {
			t.Errorf("stages %q and %q both render as %+v: the table cannot tell them apart", other, stage, style)
		}
		seen[style] = stage
	}

	for _, stage := range designSessionStages {
		assert.NotEqual(t, neutralStyle, DesignSessionStageStyle(stage),
			"%q must not fall through to the unknown-stage fallback", stage)
	}
}

// TestDesignSessionStageStyle_UnknownStageUsesNeutralFallback pins the
// eighth stage's behaviour: a new store value renders as the neutral
// badge rather than failing to compile, and never as a blank badge.
func TestDesignSessionStageStyle_UnknownStageUsesNeutralFallback(t *testing.T) {
	assert.Equal(t, neutralStyle, DesignSessionStageStyle("a stage this build does not know"))
}

// TestDesignSessionStageStyle_SizeIsUniform: every stage badge is the same
// size, so a table row's stage column cannot be read at two different
// scales depending on the stage.
func TestDesignSessionStageStyle_SizeIsUniform(t *testing.T) {
	for _, stage := range designSessionStages {
		assert.Equal(t, htmxui.BadgeSizeSM, DesignSessionStageStyle(stage).Size, "%q", stage)
	}
}

// TestDesignSessionStageLabel_HumanWordingForEveryStage: the label is the
// operator's wording, never the wire value -- "changes_requested" is not
// what a badge an operator scans for a blocked session should say.
func TestDesignSessionStageLabel_HumanWordingForEveryStage(t *testing.T) {
	for i, stage := range designSessionStages {
		assert.Equal(t, designSessionStageLabels[i], DesignSessionStageLabel(stage), "%q", stage)
	}
	// Not one label may be blank: a badge with no words is a row an
	// operator cannot read.
	for _, stage := range designSessionStages {
		assert.NotEmpty(t, DesignSessionStageLabel(stage), "%q", stage)
	}
}

// TestDesignSessionStageLabel_UnknownAndEmptyNeverBlank: an unrecognised
// stage shows its own value so a newly-added one is still readable, and
// an empty one reads as "unknown" rather than as nothing at all.
func TestDesignSessionStageLabel_UnknownAndEmptyNeverBlank(t *testing.T) {
	assert.Equal(t, "a stage this build does not know", DesignSessionStageLabel("a stage this build does not know"))
	assert.Equal(t, "unknown", DesignSessionStageLabel(""))
}

// designSessionEventTypes is store.EventType's full vocabulary -- the five
// rounds a revision_event may be -- spelled out here rather than imported,
// for the same reason designSessionStages is: this package carries no
// //krill/store dependency by design, and a test that read the list off the
// store type would silently agree with any future edit to it.
var designSessionEventTypes = []string{
	"draft",
	"reconciliation",
	"answer",
	"signoff",
	"ruling",
}

// TestDesignSessionEventTypeStyle_EveryRoundIsVisuallyDistinct: the five
// rounds are told apart by the badge alone, so a reader scanning a timeline
// cannot mistake an answer round for a ruling one.
func TestDesignSessionEventTypeStyle_EveryRoundIsVisuallyDistinct(t *testing.T) {
	seen := map[StatusStyle]string{}
	for _, eventType := range designSessionEventTypes {
		style := DesignSessionEventTypeStyle(eventType)
		if other, clash := seen[style]; clash {
			t.Errorf("rounds %q and %q both render as %+v: the timeline cannot tell them apart", other, eventType, style)
		}
		seen[style] = eventType
	}
	for _, eventType := range designSessionEventTypes {
		assert.NotEqual(t, neutralStyle, DesignSessionEventTypeStyle(eventType),
			"%q must not fall through to the unknown-round fallback", eventType)
	}
}

// TestDesignSessionEventTypeStyle_UnknownRoundUsesNeutralFallback pins the
// sixth round's behaviour: a value this build does not know renders as the
// neutral badge rather than failing to compile or rendering blank.
func TestDesignSessionEventTypeStyle_UnknownRoundUsesNeutralFallback(t *testing.T) {
	assert.Equal(t, neutralStyle, DesignSessionEventTypeStyle("a round this build does not know"))
}

// TestDesignSessionEventTypeStyle_SizeIsUniform: every round badge is one
// size, so a timeline cannot be read at two different scales depending on
// which round an entry is.
func TestDesignSessionEventTypeStyle_SizeIsUniform(t *testing.T) {
	for _, eventType := range designSessionEventTypes {
		assert.Equal(t, htmxui.BadgeSizeSM, DesignSessionEventTypeStyle(eventType).Size, "%q", eventType)
	}
}

// TestDesignSessionEventTypeLabel_NeverBlank: the rounds are already words
// an operator reads, so the label is the wire value itself -- but a badge
// with no words is a round nobody can read, so nothing may be blank.
func TestDesignSessionEventTypeLabel_NeverBlank(t *testing.T) {
	for _, eventType := range designSessionEventTypes {
		assert.Equal(t, eventType, DesignSessionEventTypeLabel(eventType))
	}
	assert.Equal(t, "a round this build does not know",
		DesignSessionEventTypeLabel("a round this build does not know"),
		"an unrecognised round shows its own value rather than an empty badge")
	assert.Equal(t, "unknown", DesignSessionEventTypeLabel(""))
}

// questionBlockingTags is the question-blocking vocabulary: the two wire
// strings a question row's blocking flag takes, spelled out for the same
// reason as every other vocabulary in this file.
var questionBlockingTags = []string{"blocking", "non-blocking"}

func TestQuestionBlockingStyle_EveryTagIsVisuallyDistinct(t *testing.T) {
	seen := map[StatusStyle]string{}
	for _, tag := range questionBlockingTags {
		style := QuestionBlockingStyle(tag)
		if other, clash := seen[style]; clash {
			t.Errorf("question tags %q and %q both render as %+v", other, tag, style)
		}
		seen[style] = tag
	}
	for _, tag := range questionBlockingTags {
		assert.NotEqual(t, neutralStyle, QuestionBlockingStyle(tag),
			"%q must not fall through to the unknown-tag fallback", tag)
	}
}

func TestQuestionBlockingStyle_UnknownTagUsesNeutralFallback(t *testing.T) {
	assert.Equal(t, neutralStyle, QuestionBlockingStyle("maybe"))
}

// TestQuestionBlockingStyle_SharesTheSessionsRegister: the rail's blocking
// badge and the sessions table's "N blocking" cell are the same fact read at
// two scales -- what a session is waiting on -- so they wear one register.
// Asserting it here is what keeps a future restyle of one from quietly
// splitting the question's severity across two colours.
func TestQuestionBlockingStyle_SharesTheSessionsRegister(t *testing.T) {
	assert.Equal(t, DesignSessionBlockingCountStyle().Variant, QuestionBlockingStyle("blocking").Variant,
		"a blocking question and a session's blocking count are one register")
	assert.Equal(t, htmxui.BadgeError, QuestionBlockingStyle("blocking").Variant)

	// Non-blocking is the absence treatment, not a second severity: a
	// question nobody is held up by must never wear a tone that reads as
	// "needs attention".
	assert.Equal(t, htmxui.BadgeGhost, QuestionBlockingStyle("non-blocking").Variant)
	for _, tag := range questionBlockingTags {
		variant := QuestionBlockingStyle(tag).Variant
		if tag == "non-blocking" {
			continue
		}
		assert.NotContains(t, []htmxui.BadgeVariant{htmxui.BadgeSuccess}, variant,
			"%q must not render as a success tone", tag)
	}
}

func TestQuestionBlockingLabel_NeverBlank(t *testing.T) {
	for _, tag := range questionBlockingTags {
		assert.Equal(t, tag, QuestionBlockingLabel(tag))
	}
	assert.Equal(t, "maybe", QuestionBlockingLabel("maybe"))
	assert.Equal(t, "unknown", QuestionBlockingLabel(""))
}

// TestDesignSessionBlockingCountStyle_IsTheErrorVariant: the "N blocking"
// cell names a session an operator is being held up by, so it takes the
// error tone rather than the neutral one a plain count badge wears.
func TestDesignSessionBlockingCountStyle_IsTheErrorVariant(t *testing.T) {
	style := DesignSessionBlockingCountStyle()
	assert.Equal(t, htmxui.BadgeError, style.Variant)
	assert.Equal(t, htmxui.BadgeSizeSM, style.Size)
	assert.False(t, style.Soft,
		"the blocking count is a stated fact about the session, not a tinted warning about it")
}
