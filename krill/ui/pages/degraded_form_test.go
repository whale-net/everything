package pages

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestFollowUpFormCarriesNoCheckboxesOfItsOwn is the control for the one
// above: with a readable question list the form still renders NO resolve
// box. The rail owns them, and joins this form by reference through the
// `form` attribute -- so a second set here would be the same question
// answered twice, and an operator ticking one and not the other would have
// their answer silently disagree with themselves.
func TestFollowUpFormCarriesNoCheckboxesOfItsOwn(t *testing.T) {
	out := renderBody(t, FollowUpForm(DesignSessionDetailPage{
		ID:          "22222222-3333-4444-5555-666666666666",
		AnswersPath: "/design/design-sessions/abc/answers",
		OpenQuestions: []OpenQuestionRow{
			{QuestionID: "q-1", Blocking: "blocking", Text: "Which flag table?"},
		},
		CheckedResolve: map[string]bool{"q-1": true},
	}))

	assert.NotContains(t, out, `type="checkbox"`,
		"the rail owns the resolve boxes; a second set here would double-post them")
	assert.NotContains(t, out, "could not be reloaded")
	assert.NotContains(t, out, `type="hidden"`,
		"the degraded path must not leak into the normal one")
	// The form still carries its own id, which is the thing the rail's
	// boxes name: the association is by reference, so the id has to be there.
	assert.Contains(t, out, `id="`+FollowUpFormAnchor+`"`)
}

// TestOpenQuestionEntriesBelongToTheFollowUpForm is the other half of the
// reference: the rail's boxes name the form, carry each question's own id
// as their value, and keep a ticked question ticked -- all without any
// script, because `form` is an HTML association rather than a behaviour.
func TestOpenQuestionEntriesBelongToTheFollowUpForm(t *testing.T) {
	out := renderBody(t, designSessionQuestionList(DesignSessionDetailPage{
		OpenQuestions: []OpenQuestionRow{
			{QuestionID: "q-1", Blocking: "blocking", Text: "Which flag table?"},
			{QuestionID: "q-2", Blocking: "non-blocking", Text: "Roll back or rollback?"},
		},
		CheckedResolve: map[string]bool{"q-1": true},
	}))

	assert.Equal(t, 2, strings.Count(out, `type="checkbox"`), "one box per open question")
	assert.Contains(t, out, `name="resolve"`)
	assert.Contains(t, out, `value="q-1"`)
	assert.Contains(t, out, `value="q-2"`)
	assert.Equal(t, 2, strings.Count(out, `form="`+FollowUpFormAnchor+`"`),
		"every box names the follow-up form, which is how it reaches the wire with no JavaScript")
	assert.Contains(t, out, "Which flag table?")
	assert.Contains(t, out, "Roll back or rollback?")

	// The ticked question stays ticked, and only it: read off each input's
	// own tag, because attribute serialisation order is not a contract.
	ticked := regexp.MustCompile(`<input[^>]*value="q-1"[^>]*>`).FindString(out)
	require.NotEmpty(t, ticked)
	assert.Contains(t, ticked, " checked")
	assert.NotContains(t, regexp.MustCompile(`<input[^>]*value="q-2"[^>]*>`).FindString(out), " checked",
		"an unticked question must not come back ticked")
}

// TestOpenQuestionListStatesItsOwnEmptiness: an empty read renders the
// shared empty state rather than a card with nothing in it, which reads to
// an operator as a rendering fault (NFR ca90dc03).
func TestOpenQuestionListStatesItsOwnEmptiness(t *testing.T) {
	out := renderBody(t, designSessionQuestionList(DesignSessionDetailPage{}))

	assert.Contains(t, out, `data-krill="open-questions-empty"`)
	assert.Contains(t, out, "No open questions.")
	assert.NotContains(t, out, `type="checkbox"`)
}
