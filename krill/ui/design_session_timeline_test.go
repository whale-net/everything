// What goes IN the session detail's two regions: the timeline and the
// Open questions rail card (FRs e5ad1a5b, f2a2dfce).
//
// design_page_test.go pins parity -- the timeline against
// get_design_session's own wire, the rail against list_open_questions's --
// field by field. This file covers what parity cannot: that each of the five
// rounds renders a badge resolved through the one mapper that vocabulary has,
// that a round whose own content is the only prose it carries produces a
// summary an operator can read, that each read fails into its OWN region
// rather than taking the page down, and that the resolve boxes exist exactly
// once, in the rail, bound to the follow-up form without any script.
//
// Nothing here asserts a daisyUI class string or a badge colour literal: the
// claims are about which mapper produced the wording, and asserting against
// a restyleable class would pin the wrong thing (htmxui ARCHITECTURE §14).
package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// followUpFormID is the form the rail's boxes name, taken from the pages
// package's own exported constant rather than spelled out here. The two
// spellings drifting apart is the exact failure the constant exists to
// prevent: a box naming a form id nothing renders posts nothing, and does so
// silently, with JavaScript disabled.
var followUpFormID = pages.FollowUpFormAnchor

// renderTimeline serves one session's detail over fakes and returns the
// body. logErr and questionsErr fail the two accessors independently, which
// is what lets a case say "the LOG read broke" without also claiming the
// rail did.
func renderTimeline(t *testing.T, sessionID, productID uuid.UUID, log []store.RevisionEvent,
	questions []store.OpenQuestion, logErr, questionsErr error) string {
	t.Helper()
	app := newDesignReadApp(
		fakeDesignSessions{
			byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
			summaries: designSummaries(productID, designSummary(
				sessionID, productID, "operators need a rollback story", store.StageInDraft, time.Now(), 0)),
		},
		fakeRevisionEvents{
			bySession:     map[uuid.UUID][]store.RevisionEvent{sessionID: log},
			openQuestions: map[uuid.UUID][]store.OpenQuestion{sessionID: questions},
			logErr:        logErr,
			questionsErr:  questionsErr,
		},
	)
	rec := get(designReadMux(app), designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}

// TestSessionDetailTimeline_EveryRoundBadgeResolvesThroughOneMapper covers
// all five of store's rounds, and the case a sixth would take.
//
// The assertion is against components.DesignSessionEventTypeLabel, not a
// literal: the claim is that the five rounds have exactly one owner for
// their wording and their colour, which survives a restyle of either the
// mapper or the timeline and would not survive a duplicated badge.
func TestSessionDetailTimeline_EveryRoundBadgeResolvesThroughOneMapper(t *testing.T) {
	rounds := []store.EventType{
		store.EventTypeDraft, store.EventTypeReconciliation, store.EventTypeAnswer,
		store.EventTypeSignoff, store.EventTypeRuling,
		// A round this build does not know, which migration 008's CHECK
		// does not permit but a store that grows one ahead of the UI would
		// hand the page.
		"retrospective",
	}

	sessionID, productID := uuid.New(), uuid.New()
	log := make([]store.RevisionEvent, 0, len(rounds))
	for i, et := range rounds {
		ev := event(i+1, et)
		log = append(log, ev)
	}
	body := renderTimeline(t, sessionID, productID, log, nil, nil, nil)

	got := parseRenderedEvents(t, body)
	require.Len(t, got, len(rounds), "one timeline entry per round, whatever the round is")

	for i, et := range rounds {
		r := got[i]
		want := components.DesignSessionEventTypeLabel(string(et))
		assert.NotEmpty(t, want, "%q: a round badge is never blank", et)
		// The spine node is an UNLABELLED badge in the same colour, so the
		// assertion reads the labelled one specifically -- otherwise a
		// timeline could pass with no wording on its badges at all.
		assert.Contains(t, detailRegion(t, body, regionRevisionLog, "</section>"),
			`data-krill="design-session-event-type">`+want+"</span>",
			"%q: the entry's badge must carry the shared label for this round", et)
		assert.Equal(t, string(et), r.EventType, "seq %d: the entry records the store's own round", r.SeqNo)
	}

	// A round the mapper does not know still renders a readable badge rather
	// than a blank one, which is the whole contract of the default arm.
	assert.Equal(t, "retrospective",
		components.DesignSessionEventTypeLabel("retrospective"))
}

// TestSessionDetailTimeline_OneEntryPerRoundInLogOrder is FR a1b955e4's
// ordering claim at full size: every round of the log appears, in
// ListBySession's own ascending seq_no, with the store's own numbers on it.
func TestSessionDetailTimeline_OneEntryPerRoundInLogOrder(t *testing.T) {
	sessionID, productID := uuid.New(), uuid.New()

	// Seven rounds, so a renderer that showed only the first or the last,
	// or that deduplicated by event type, cannot pass.
	kinds := []store.EventType{
		store.EventTypeDraft, store.EventTypeReconciliation, store.EventTypeAnswer,
		store.EventTypeSignoff, store.EventTypeRuling, store.EventTypeDraft,
		store.EventTypeReconciliation,
	}
	alice := store.Subject{Iss: "https://idp.test", Sub: "alice", Kind: store.SubjectKindHuman}
	log := make([]store.RevisionEvent, 0, len(kinds))
	for i, et := range kinds {
		ev := event(i+1, et)
		ev.Acting = alice
		ev.OnBehalfOf = alice
		ev.CreatedAt = time.Date(2026, 3, 1, 9, i, 0, 0, time.UTC)
		log = append(log, ev)
	}
	body := renderTimeline(t, sessionID, productID, log, nil, nil, nil)

	got := parseRenderedEvents(t, body)
	require.Len(t, got, len(log), "one entry per round, no more and no fewer")
	for i, ev := range log {
		assert.Equal(t, ev.SeqNo, got[i].SeqNo, "entry %d renders the store's own seq_no", i)
		assert.Equal(t, subjectLabel(alice), got[i].Acting,
			"entry %d renders the acting identity the server recorded", i)
		// Acting and on-behalf-of are the same subject here, so no second
		// line: "on behalf of yourself" down every entry would read as a
		// second, different actor.
		assert.Empty(t, got[i].OnBehalfOf,
			"entry %d must not name an on-behalf-of actor when the store recorded the same one", i)
	}
	for i := 1; i < len(got); i++ {
		assert.Less(t, got[i-1].SeqNo, got[i].SeqNo,
			"the timeline is the store's ascending seq_no order, which is get_design_session's own")
	}
}

// TestSessionDetailTimeline_TimeIsRelativeToReadAndExactOnHover is the
// timestamp contract: the visible text is a readable age computed against a
// clock, and the exact instant rides along on the same element so hovering
// answers "exactly when" -- with or without the head script.
//
// The age is pinned at the BUILDER, where `now` is a parameter, because
// through the HTTP handler it is whatever the wall clock said and a case
// that had to guess it would be a flake rather than an assertion. The page
// case below then checks that the element carries both halves.
func TestSessionDetailTimeline_TimeIsRelativeToReadAndExactOnHover(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sessionID, productID := uuid.New(), uuid.New()
	created := now.Add(-3 * time.Hour)

	ev := event(1, store.EventTypeDraft)
	ev.CreatedAt = created
	app := newDesignReadApp(
		fakeDesignSessions{
			byID:      map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
			summaries: designSummaries(productID, designSummary(sessionID, productID, "an idea", store.StageInDraft, created, 0)),
		},
		fakeRevisionEvents{bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {ev}}},
	)
	page, err := app.buildDesignSessionDetail(t.Context(), productID, sessionID, now)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)

	row := page.Events[0]
	assert.Equal(t, "3 h ago", row.AtRelative,
		"the entry's visible text is a readable age, computed against the clock the page was built with")
	assert.Equal(t, relativeTime(created, now), row.AtRelative,
		"and it is package main's own relativeTime, so this page cannot drift from every other timestamp in the app")
	assert.Equal(t, created.UTC().Format(time.RFC3339), row.AtExact,
		"the exact instant is RFC3339, the form the head script's hook parses")

	// And on the served page, the element carries both halves.
	body := renderTimeline(t, sessionID, productID, []store.RevisionEvent{ev}, nil, nil, nil)
	got := parseRenderedEvents(t, body)
	require.Len(t, got, 1)
	wantExact := created.UTC().Format(time.RFC3339)
	assert.Equal(t, wantExact, got[0].AtExact, "the datetime carries the exact instant")
	assert.Equal(t, wantExact, got[0].AtTitle, "the title carries the same instant, so hovering answers it")
	assert.NotEmpty(t, got[0].AtRelative, "the element renders a readable age server-side")

	// And the shared head script's hook is present, which is what upgrades
	// the age client-side.
	logRegion := detailRegion(t, body, regionRevisionLog, "</section>")
	assert.Contains(t, logRegion, `data-krill-updated-at="`+wantExact+`"`,
		"the entry carries the hook the status register's timestamps use")
}

// TestRevisionEventSummary_IsComposedFromTheRoundsOwnContent pins the one
// helper that writes the sentence a timeline box shows, across the three
// shapes a round can take.
//
// revision_event has no prose column (migration 008), so this string is
// derived from what the round recorded. The point of pinning it is that a
// timeline whose boxes read "3 changes" tells an operator nothing, and the
// only guard against regressing to that is an assertion on the wording.
func TestRevisionEventSummary_IsComposedFromTheRoundsOwnContent(t *testing.T) {
	sessionID, productID := uuid.New(), uuid.New()

	deltasOnly := event(1, store.EventTypeDraft)
	deltasOnly.EntityDeltas = []store.EntityDelta{
		{EntityID: uuid.New(), Change: store.EntityDeltaChangeCreated, SummaryLine: "added the rollback requirement"},
		{EntityID: uuid.New(), Change: store.EntityDeltaChangeUpdated, SummaryLine: "tightened the RTO to 5m"},
	}

	countOnly := event(2, store.EventTypeDraft)
	countOnly.EntityDeltas = []store.EntityDelta{
		{EntityID: uuid.New(), Change: store.EntityDeltaChangeCreated},
		{EntityID: uuid.New(), Change: store.EntityDeltaChangeUpdated},
		{EntityID: uuid.New(), Change: store.EntityDeltaChangeCreated},
	}

	openedOnly := event(3, store.EventTypeDraft)
	openedOnly.OpenQuestionsDelta.Opened = []store.OpenQuestionOpened{
		{QuestionID: "q-blocking", Blocking: true, Text: "which store holds the flag?"},
		{QuestionID: "q-nice", Blocking: false, Text: "roll back or rollback?"},
	}
	// A question opened with no text is still a question; the sentence names
	// its id rather than dropping it, because a dropped question reads as a
	// question nobody asked.
	openedOnly.OpenQuestionsDelta.Opened = append(openedOnly.OpenQuestionsDelta.Opened,
		store.OpenQuestionOpened{QuestionID: "q-bare", Blocking: false})

	resolveOnly := event(4, store.EventTypeAnswer)
	resolveOnly.OpenQuestionsDelta.Resolved = []string{"q-nice", "q-blocking", "q-third"}

	log := []store.RevisionEvent{deltasOnly, countOnly, openedOnly, resolveOnly}
	got := parseRenderedEvents(t, renderTimeline(t, sessionID, productID, log, nil, nil, nil))
	require.Len(t, got, 4)

	// Each sentence states what its own round recorded, verbatim.
	assert.Equal(t, "added the rollback requirement; tightened the RTO to 5m.", got[0].Summary,
		"a deltas-only round summarises its own summary lines")
	assert.Equal(t, "3 entity changes.", got[1].Summary,
		"a round that touched entities but wrote no summary line says how many, rather than nothing")
	assert.Equal(t, "3 questions opened: which store holds the flag?; roll back or rollback?; q-bare.", got[2].Summary,
		"an opened-questions-only round names each question, falling back to the id where there is no text")
	assert.Equal(t, "resolved q-nice, q-blocking and q-third.", got[3].Summary,
		"a resolve-only round names the ids it closed, which is all a resolution carries")

	// A round that recorded nothing at all still renders a sentence rather
	// than an empty line where the reader looks for one.
	bare := event(5, store.EventTypeSignoff)
	got = parseRenderedEvents(t, renderTimeline(t, sessionID, productID, []store.RevisionEvent{bare}, nil, nil, nil))
	require.Len(t, got, 1)
	assert.NotEmpty(t, got[0].Summary,
		"a round with no deltas and no question change still says something")
}

// TestSessionDetailTimeline_LogReadFailureDegradesOneRegion is FR e5ad1a5b:
// a log that cannot be read costs the operator the timeline and nothing
// else. The rail, the opening statement and the follow-up form all still
// render, because they are answers to different questions and none of them
// depends on the log read having succeeded.
func TestSessionDetailTimeline_LogReadFailureDegradesOneRegion(t *testing.T) {
	sessionID, productID := uuid.New(), uuid.New()
	questions := []store.OpenQuestion{
		{QuestionID: "blocker", Text: "needs a decision", Blocking: true, OpenedAtSeqNo: 1},
	}
	// The control case first: the same session with working reads really
	// does render a timeline, so what the failing cases below observe is
	// the failure's doing and not the fixture's.
	require.Contains(t, detailRegion(t,
		renderTimeline(t, sessionID, productID, []store.RevisionEvent{event(1, store.EventTypeDraft)}, questions, nil, nil),
		regionRevisionLog, "</section>"),
		`data-krill="revision-event-timeline"`)

	t.Run("a failed log is an alert, never an empty timeline", func(t *testing.T) {
		broken := renderTimeline(t, sessionID, productID, nil, questions,
			fmt.Errorf("pq: password authentication failed for user krill"), nil)
		timeline := detailRegion(t, broken, regionRevisionLog, "</section>")

		assert.Contains(t, timeline, `data-krill="revision-events-error"`,
			"the failing read is stated inline where the timeline was")
		assert.NotContains(t, timeline, `data-krill="revision-event-timeline"`)
		// NFR ca90dc03: a read that failed is never rendered as an empty
		// result. "This timeline is unavailable" and "this session has no
		// rounds" are different facts.
		assert.NotContains(t, timeline, "No revision events yet.")

		// No store text: the log read can fail for reasons naming an
		// internal URL or a driver, and the browser is not the place.
		assert.NotContains(t, broken, "password authentication")
	})

	t.Run("the rail still answers", func(t *testing.T) {
		broken := renderTimeline(t, sessionID, productID, nil, questions,
			fmt.Errorf("pq: password authentication failed for user krill"), nil)
		rail := detailRegion(t, broken, regionOpenQuestion, "</section>")
		assert.Contains(t, rail, "needs a decision",
			"a question an operator still has to answer does not disappear because the log read broke")
	})

	t.Run("a failed question read degrades the rail alone", func(t *testing.T) {
		broken := renderTimeline(t, sessionID, productID,
			[]store.RevisionEvent{event(1, store.EventTypeDraft)}, nil, nil,
			fmt.Errorf("pq: password authentication failed for user krill"))
		rail := detailRegion(t, broken, regionOpenQuestion, "</section>")
		assert.Contains(t, rail, `data-krill="open-questions-error"`,
			"the question read's failure is stated inside the rail")
		assert.NotContains(t, rail, `data-krill="open-questions-empty"`,
			"a read that failed is never rendered as a card with nothing in it")
		// And the timeline -- which this failure did not touch -- is whole.
		assert.Contains(t, detailRegion(t, broken, regionRevisionLog, "</section>"),
			`data-krill="revision-event-timeline"`)
	})
}

// TestSessionDetailTimeline_RailListsExactlyTheOpenQuestions is FR
// f2a2dfce's rail: one entry per currently-open question, each with its
// blocking badge through the shared mapper and a box whose value is the id
// the answer posts, and nothing for a question that is no longer open.
func TestSessionDetailTimeline_RailListsExactlyTheOpenQuestions(t *testing.T) {
	sessionID, productID := uuid.New(), uuid.New()

	// A round that RESOLVED one of them: the store's read is where that
	// disappears, so the rail cannot show it -- which is the claim.
	resolving := event(2, store.EventTypeAnswer)
	resolving.OpenQuestionsDelta.Resolved = []string{"q-answered"}
	log := []store.RevisionEvent{event(1, store.EventTypeDraft), resolving}
	questions := []store.OpenQuestion{
		{QuestionID: "q-blocking", Text: "which store holds the flag?", Blocking: true, OpenedAtSeqNo: 1},
		{QuestionID: "q-nice", Text: "roll back or rollback?", Blocking: false, OpenedAtSeqNo: 1},
	}

	body := renderTimeline(t, sessionID, productID, log, questions, nil, nil)
	rail := detailRegion(t, body, regionOpenQuestion, "</section>")

	got := parseRenderedOpenQuestions(t, body)
	require.Len(t, got, 2, "one entry per currently-open question")

	for i, want := range []store.OpenQuestion{questions[0], questions[1]} {
		assert.Equal(t, want.QuestionID, got[i].QuestionID)
		assert.Equal(t, want.Text, got[i].Text)
		assert.Equal(t, components.QuestionBlockingLabel(blockingTag(want.Blocking)), got[i].Tag,
			"%s: the badge carries the shared label for this tag", want.QuestionID)
		assert.Equal(t, want.QuestionID, got[i].ResolveValue,
			"%s: the box posts the question's own id", want.QuestionID)
		assert.Equal(t, followUpFormID, got[i].Form,
			"%s: the box belongs to the follow-up form by reference", want.QuestionID)
		assert.False(t, got[i].Checked, "%s: nothing is ticked on a fresh read", want.QuestionID)
	}

	// The blocking and non-blocking badges are told apart by the shared
	// mapper, so a rail cannot render both in one colour by accident.
	assert.NotEqual(t,
		components.QuestionBlockingStyle("blocking").Variant,
		components.QuestionBlockingStyle("non-blocking").Variant,
		"a blocking question and a non-blocking one must not wear the same register")

	// A question the store resolved is absent from the RAIL even though the
	// timeline still records the round that resolved it.
	assert.NotContains(t, rail, "q-answered",
		"a resolved question is not offered for answering again")
	assert.Contains(t, detailRegion(t, body, regionRevisionLog, "</section>"), "q-answered",
		"but the round that resolved it is still in the log")

	// And with none open the card says so rather than rendering an empty
	// body, which reads to an operator as a fault.
	empty := renderTimeline(t, sessionID, productID, log, nil, nil, nil)
	emptyRail := detailRegion(t, empty, regionOpenQuestion, "</section>")
	assert.Contains(t, emptyRail, `data-krill="open-questions-empty"`)
	assert.NotContains(t, emptyRail, `data-krill="open-question-row"`)
}

// TestSessionDetailTimeline_ResolveBoxesExistExactlyOnce is the whole
// point of moving them: the wireframe puts the boxes in the rail and the
// submit in the main column, so the boxes join the form by REFERENCE. A
// second copy inside the form would be the same question offered twice, and
// an operator who ticked one and not the other would have their answer
// silently disagree with themselves.
func TestSessionDetailTimeline_ResolveBoxesExistExactlyOnce(t *testing.T) {
	sessionID, productID := uuid.New(), uuid.New()
	questions := []store.OpenQuestion{
		{QuestionID: "q-1", Text: "which store holds the flag?", Blocking: true, OpenedAtSeqNo: 1},
		{QuestionID: "q-2", Text: "roll back or rollback?", Blocking: false, OpenedAtSeqNo: 1},
		{QuestionID: "q-3", Text: "is the flag durable?", Blocking: true, OpenedAtSeqNo: 1},
	}
	body := renderTimeline(t, sessionID, productID, []store.RevisionEvent{event(1, store.EventTypeDraft)}, questions, nil, nil)

	// One box per question across the WHOLE page -- not one per region.
	assert.Equal(t, 3, strings.Count(body, `data-krill="open-question-resolve"`),
		"exactly one resolve box per open question, wherever it is rendered")
	assert.Equal(t, 3, strings.Count(body, `name="resolve"`),
		"exactly one resolve input per question, so one submission cannot post a question twice")

	// The form still exists and still names the boxes' target, because the
	// association is by reference rather than by nesting.
	assert.Contains(t, body, `id="`+followUpFormID+`"`,
		"the follow-up form the rail's boxes name must be on the page")
	assert.Contains(t, body, `form="`+followUpFormID+`"`,
		"the boxes reach the wire through the form attribute, which works with JavaScript disabled")
}

// TestSessionDetailTimeline_RendersNoScriptOfItsOwn: the timeline and the
// rail are server-rendered. A timeline that needed a script to place its
// nodes, or to keep its boxes attached to its form, would render empty for
// an operator whose JavaScript never loaded -- and the boxes are the part
// that has to work there.
func TestSessionDetailTimeline_RendersNoScriptOfItsOwn(t *testing.T) {
	sessionID, productID := uuid.New(), uuid.New()

	// The HX-Request fragment is the part a swap drops into an existing
	// document, so it is the part that must carry no script at all.
	mux := designReadMux(newDesignReadApp(
		fakeDesignSessions{
			byID:      map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
			summaries: designSummaries(productID, designSummary(sessionID, productID, "an idea", store.StageInDraft, time.Now(), 0)),
		},
		fakeRevisionEvents{
			bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}},
			openQuestions: map[uuid.UUID][]store.OpenQuestion{sessionID: {
				{QuestionID: "blocker", Text: "needs a decision", Blocking: true, OpenedAtSeqNo: 1},
			}},
		},
	))
	rec := hxGet(mux, designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, rec.Code)

	fragment := rec.Body.String()
	assert.NotContains(t, fragment, "<script",
		"the session detail fragment must carry no script of its own")
	assert.Contains(t, fragment, `data-krill="revision-event-timeline"`)
	assert.Contains(t, fragment, `data-krill="open-question-resolve"`,
		"the resolve boxes render server-side, so they exist before any script runs")
}
