package pages

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The round region is the one thing a follow-up write swaps, so its id is
// load-bearing three times over: the form's hx-target names it, the write
// handler renders it as the fragment, and htmx needs the served fragment's
// ROOT to carry it. A disagreement between any two of those three does not
// throw -- it silently freezes the page, because htmx returns early when it
// cannot resolve a target, before it even issues the request.
//
// These tests are at the pages layer because that is where the wiring is
// authored; the handler's behaviour against it is covered in krill/ui.

// TestFollowUpFormSwapsTheRoundRegion: the form's hx-target is the region
// wrapping the timeline, the rail and the form -- not the form itself.
//
// A round changes all three at once (the log gains the event, the rail
// loses the questions it closed, the textarea empties), so a swap of the
// form alone would leave the other two showing the state from BEFORE the
// answer. On a refusal the same swap would drop the operator's ticked
// questions outright, since those boxes live in the rail (FR 1942d934).
func TestFollowUpFormSwapsTheRoundRegion(t *testing.T) {
	out := renderBody(t, FollowUpForm(DesignSessionDetailPage{
		ID:          "22222222-3333-4444-5555-666666666666",
		AnswersPath: "/design/products/p/design-sessions/s/answers",
		OpenQuestions: []OpenQuestionRow{
			{QuestionID: "q-1", Blocking: "blocking", Text: "Which flag table?"},
		},
	}))

	target, ok := attrOf(t, out, "design-session-follow-up-form", "hx-target")
	require.True(t, ok, "the follow-up form must name an hx-target")
	assert.Equal(t, "#"+DesignSessionRoundAnchor, target,
		"the swap target must be the round region, not the form")
	assert.NotEqual(t, "#"+FollowUpFormAnchor, target,
		"swapping the form alone cannot express what a round changes")
}

// TestDesignSessionRoundIsItsOwnSwapTarget is the other half of that pair:
// the served fragment's ROOT is the region the form's hx-target names, or
// htmx's swapOuterHTML inserts the response before the target and then
// REMOVES the target -- leaving a page whose form can no longer be
// submitted at all.
func TestDesignSessionRoundIsItsOwnSwapTarget(t *testing.T) {
	out := renderBody(t, DesignSessionRound(DesignSessionDetailPage{
		ID:             "22222222-3333-4444-5555-666666666666",
		ProductName:    "krill",
		OpeningRequest: "operators need a rollback story",
		AnswersPath:    "/design/products/p/design-sessions/s/answers",
		Events: []RevisionEventRow{{
			ID: "e-1", SeqNo: 1, EventType: "draft", Summary: "first round",
		}},
		OpenQuestions: []OpenQuestionRow{
			{QuestionID: "q-1", Blocking: "blocking", Text: "Which flag table?"},
		},
	}))

	top := topLevelElements(out)
	require.Len(t, top, 1, "a single top-level element, or a swap splices the extras in every time")
	assert.Equal(t, "section", top[0])
	assert.Contains(t, out, `id="`+DesignSessionRoundAnchor+`"`,
		"the served fragment must carry the id the requesting form's hx-target names")
}

// TestDesignSessionRoundContainsTheTimelineTheRailAndTheForm: the one region
// has to actually CONTAIN all three, or "swap the round" is a name and not
// a behaviour. The properties card and the opening statement ride along,
// which is harmless -- they are re-derived from the same read.
func TestDesignSessionRoundContainsTheTimelineTheRailAndTheForm(t *testing.T) {
	out := renderBody(t, DesignSessionRound(DesignSessionDetailPage{
		ID:             "22222222-3333-4444-5555-666666666666",
		AnswersPath:    "/design/products/p/design-sessions/s/answers",
		Events:         []RevisionEventRow{{ID: "e-1", SeqNo: 1, EventType: "draft"}},
		OpenQuestions:  []OpenQuestionRow{{QuestionID: "q-1", Blocking: "blocking", Text: "Which flag table?"}},
		FollowUp:       "a half-typed thought",
		CheckedResolve: map[string]bool{"q-1": true},
	}))

	for _, region := range []string{designSessionEventsAnchor, designSessionQuestionsAnchor} {
		assert.Contains(t, out, region, "the round region must contain %s", region)
	}
	assert.Contains(t, out, `id="`+FollowUpFormAnchor+`"`,
		"and the form, which is what the swap is for")
	assert.Equal(t, 1, strings.Count(out, `data-krill="open-question-resolve"`),
		"exactly one resolve box: the rail's, not a second one inside the form")
	assert.True(t, resolveCheckedIn(t, out, "q-1"),
		"a refused round re-renders the rail with the operator's tick intact")
}

// TestDesignSessionRoundOmitsTheFormWhenSignedOff is FR d81d2283's terminal
// case. A signed-off session takes no further rounds, so the page offers no
// way to send one -- and says why, so the missing form reads as a decision
// rather than a rendering fault.
//
// The region's OWN id is present either way: it is the swap target, and a
// swap target that vanishes with the form is the exact thing the swap-target
// rule exists to prevent.
func TestDesignSessionRoundOmitsTheFormWhenSignedOff(t *testing.T) {
	out := renderBody(t, DesignSessionRound(DesignSessionDetailPage{
		ID:          "22222222-3333-4444-5555-666666666666",
		AnswersPath: "/design/products/p/design-sessions/s/answers",
		Stage:       "approved",
		SignedOff:   true,
		OpenQuestions: []OpenQuestionRow{
			{QuestionID: "q-1", Blocking: "blocking", Text: "Which flag table?"},
		},
	}))

	assert.NotContains(t, out, `id="`+FollowUpFormAnchor+`"`,
		"a signed-off session shows no follow-up form at all")
	assert.NotContains(t, out, `name="follow_up"`)
	assert.NotContains(t, out, "Submit follow-up")
	assert.Contains(t, out, `data-krill="design-session-signed-off"`,
		"and it says why the form is gone")

	// Still a valid swap target, and the read view is untouched.
	assert.Contains(t, out, `id="`+DesignSessionRoundAnchor+`"`)
	assert.Contains(t, out, designSessionQuestionsAnchor)
	assert.Contains(t, out, `data-krill="open-question-resolve"`,
		"the rail still reads its questions; only the form is gone")
}

// resolveCheckedIn reports whether the resolve box for questionID rendered
// ticked, read off the input's own tag -- attribute serialisation order is
// not a contract.
func resolveCheckedIn(t *testing.T, body, questionID string) bool {
	t.Helper()
	re := regexp.MustCompile(`<input[^>]*value="` + regexp.QuoteMeta(questionID) + `"[^>]*>`)
	input := re.FindString(body)
	require.NotEmpty(t, input, "no resolve box for %q", questionID)
	return strings.Contains(input, " checked")
}
