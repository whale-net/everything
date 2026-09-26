package pages

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFollowUpFormCarriesTickedIdsWhenQuestionsAreUnreadable pins the
// degraded answer form. When the page behind it could not be re-read,
// FollowUpForm has no open questions to render checkboxes against -- but
// the ids the operator ticked are still known, and dropping them would
// silently discard their answer on resubmit. They ride as hidden inputs
// instead, which is honest: the operator is not being asked to re-approve
// anything, we are preserving what they already chose.
//
// The comment on the handler's degradedForm closure used to claim this
// was preserved while the fieldset was not rendered at all, so the ticks
// were lost on the wire. This is the guard on that claim.
func TestFollowUpFormCarriesTickedIdsWhenQuestionsAreUnreadable(t *testing.T) {
	out := renderBody(t, FollowUpForm(DesignSessionDetailPage{
		ID:             "22222222-3333-4444-5555-666666666666",
		AnswersPath:    "/design/design-sessions/abc/answers",
		FollowUp:       "It should use the postgres flag table.",
		CheckedResolve: map[string]bool{"q-1": true, "q-2": true},
		OpenQuestions:  nil,
	}))

	assert.Contains(t, out, `name="resolve" value="q-1"`,
		"a ticked id must survive the degraded re-render")
	assert.Contains(t, out, `name="resolve" value="q-2"`)
	assert.Contains(t, out, "It should use the postgres flag table.",
		"the typed follow-up must survive too")
	assert.Contains(t, out, "could not be reloaded",
		"the operator is told the question list is unavailable")
}

// TestFollowUpFormRendersCheckboxesWhenQuestionsAreAvailable is the
// control: with a readable list the operator gets real checkboxes, and no
// hidden inputs.
func TestFollowUpFormRendersCheckboxesWhenQuestionsAreAvailable(t *testing.T) {
	out := renderBody(t, FollowUpForm(DesignSessionDetailPage{
		ID:          "22222222-3333-4444-5555-666666666666",
		AnswersPath: "/design/design-sessions/abc/answers",
		OpenQuestions: []OpenQuestionRow{
			{QuestionID: "q-1", Blocking: "blocking", Text: "Which flag table?"},
		},
		CheckedResolve: map[string]bool{"q-1": true},
	}))

	assert.Contains(t, out, `type="checkbox"`)
	assert.Contains(t, out, `value="q-1"`)
	assert.Contains(t, out, "checked", "a ticked question stays ticked")
	assert.NotContains(t, out, "could not be reloaded")
	assert.NotContains(t, out, `type="hidden"`,
		"the degraded path must not leak into the normal one")
}
