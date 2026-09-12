//go:build integration

// Real-Postgres coverage for RevisionEventStore.ListOpenQuestions
// (open_questions.go, issue #2545's Testing section, FR6) -- the derived,
// last-event-wins view over open_questions_delta. Shares
// design_session_integration_test.go's fixture helpers (same go_test
// target -- see that file's package doc for why).
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/store:design_session_integration_test --test_output=all
package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// oqAppend is a small helper local to this file: appends a `ruling` event
// (no FR3/FR4 conditional fields to satisfy) carrying only the
// open_questions_delta a case cares about.
func oqAppend(t *testing.T, f reTestFixture, self store.Subject, delta store.OpenQuestionsDelta) store.RevisionEvent {
	t.Helper()
	e := baseRevisionEvent(f, self, self)
	e.OpenQuestionsDelta = delta
	ev, err := f.store.RevisionEvents().Append(context.Background(), e)
	require.NoError(t, err)
	return ev
}

// TestListOpenQuestions_OneDraftEvent_BothQuestionsReturned is this issue's
// store Testing case 1: one draft event opening Q1 (blocking) and Q2
// (non-blocking) returns both, flags correct, ordered by seq.
func TestListOpenQuestions_OneDraftEvent_BothQuestionsReturned(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/open-questions-one-draft-test")
	self := dsTestSubject("agent-1")

	oqAppend(t, f, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "what storage backend?"},
		{QuestionID: "q2", Blocking: false, Text: "naming bikeshed"},
	}})

	got, err := f.store.RevisionEvents().ListOpenQuestions(ctx, f.designSessionID)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "q1", got[0].QuestionID)
	assert.True(t, got[0].Blocking)
	assert.Equal(t, "what storage backend?", got[0].Text)
	assert.Equal(t, "q2", got[1].QuestionID)
	assert.False(t, got[1].Blocking)
	assert.Equal(t, "naming bikeshed", got[1].Text)
}

// TestListOpenQuestions_LaterResolve_OnlyRemainingQuestionReturned is this
// issue's store Testing case 2: a later answer event resolving Q1 leaves
// only Q2 -- Q1 absent.
func TestListOpenQuestions_LaterResolve_OnlyRemainingQuestionReturned(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/open-questions-resolve-test")
	self := dsTestSubject("agent-1")

	oqAppend(t, f, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "what storage backend?"},
		{QuestionID: "q2", Blocking: false, Text: "naming bikeshed"},
	}})
	oqAppend(t, f, self, store.OpenQuestionsDelta{Resolved: []string{"q1"}})

	got, err := f.store.RevisionEvents().ListOpenQuestions(ctx, f.designSessionID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "q2", got[0].QuestionID)
}

// TestListOpenQuestions_ReopenAfterResolve_LatestOpenedFlagsWin is this
// issue's store Testing case 3: re-opening Q1 in a still-later event
// returns Q1 again (last-event-wins), with the flag/text from the latest
// `opened` entry, not the original.
func TestListOpenQuestions_ReopenAfterResolve_LatestOpenedFlagsWin(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/open-questions-reopen-test")
	self := dsTestSubject("agent-1")

	oqAppend(t, f, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "what storage backend?"},
	}})
	oqAppend(t, f, self, store.OpenQuestionsDelta{Resolved: []string{"q1"}})
	oqAppend(t, f, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: false, Text: "reopened, lower priority now"},
	}})

	got, err := f.store.RevisionEvents().ListOpenQuestions(ctx, f.designSessionID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "q1", got[0].QuestionID)
	assert.False(t, got[0].Blocking, "the latest opened entry's blocking flag must win")
	assert.Equal(t, "reopened, lower priority now", got[0].Text, "the latest opened entry's text must win")
	assert.Equal(t, 3, got[0].OpenedAtSeqNo, "OpenedAtSeqNo must name the latest opening event, not the first")
}

// TestListOpenQuestions_ResolveThenResolveAgain_Idempotent is this issue's
// store Testing case 4: resolving an already-resolved question a second
// time is idempotent -- still absent, no error (and, per FR6, must not be
// rejected as "resolved names an unopened question").
func TestListOpenQuestions_ResolveThenResolveAgain_Idempotent(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/open-questions-double-resolve-test")
	self := dsTestSubject("agent-1")

	oqAppend(t, f, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "what storage backend?"},
	}})
	oqAppend(t, f, self, store.OpenQuestionsDelta{Resolved: []string{"q1"}})

	e := baseRevisionEvent(f, self, self)
	e.OpenQuestionsDelta = store.OpenQuestionsDelta{Resolved: []string{"q1"}}
	_, err := f.store.RevisionEvents().Append(ctx, e)
	require.NoError(t, err, "resolving an already-resolved question must not error")

	got, err := f.store.RevisionEvents().ListOpenQuestions(ctx, f.designSessionID)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestListOpenQuestions_OnlyTwoOfFiftyEventsTouchQuestions_SingleQuery is
// this issue's store Testing case 5: a session with 50 events where only 2
// touch questions returns the right answer, via one SQL statement (not a
// Go-side fold over ListBySession's full log) -- see open_questions.go's
// doc comment for why review, not statement-counting infrastructure, is
// this project's chosen way to prove "one query" (Validation phase).
func TestListOpenQuestions_OnlyTwoOfFiftyEventsTouchQuestions_SingleQuery(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/open-questions-fifty-events-test")
	self := dsTestSubject("agent-1")

	for i := 0; i < 48; i++ {
		e := baseRevisionEvent(f, self, self)
		// entity_deltas content is irrelevant to this test -- these events
		// exist only to pad the log so ListOpenQuestions has 48 events to
		// skip over that neither open nor resolve anything.
		e.EntityDeltas = []store.EntityDelta{{
			EntityID:    uuid.New(),
			Change:      store.EntityDeltaChangeCreated,
			SummaryLine: fmt.Sprintf("noise event %d", i),
		}}
		_, err := f.store.RevisionEvents().Append(ctx, e)
		require.NoError(t, err)
	}
	oqAppend(t, f, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "what storage backend?"},
	}})
	oqAppend(t, f, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q2", Blocking: false, Text: "naming bikeshed"},
	}})

	list, err := f.store.RevisionEvents().ListBySession(ctx, f.designSessionID)
	require.NoError(t, err)
	require.Len(t, list, 50, "sanity: the session really has 50 events")

	got, err := f.store.RevisionEvents().ListOpenQuestions(ctx, f.designSessionID)
	require.NoError(t, err)
	require.Len(t, got, 2, "only the 2 of 50 events that touch a question should surface here")
	assert.Equal(t, "q1", got[0].QuestionID)
	assert.Equal(t, "q2", got[1].QuestionID)
}

// TestListOpenQuestions_QuestionsDoNotLeakAcrossSessions is this issue's
// store Testing case 6: two sessions each opening a question with the same
// question-id string return only their own.
func TestListOpenQuestions_QuestionsDoNotLeakAcrossSessions(t *testing.T) {
	ctx := context.Background()
	f1 := newRevisionEventFixture(t, "whale-net/open-questions-cross-session-a-test")
	f2 := newRevisionEventFixture(t, "whale-net/open-questions-cross-session-b-test")
	self := dsTestSubject("agent-1")

	oqAppend(t, f1, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: true, Text: "session A's question"},
	}})
	oqAppend(t, f2, self, store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
		{QuestionID: "q1", Blocking: false, Text: "session B's question"},
	}})

	got1, err := f1.store.RevisionEvents().ListOpenQuestions(ctx, f1.designSessionID)
	require.NoError(t, err)
	require.Len(t, got1, 1)
	assert.Equal(t, "session A's question", got1[0].Text)
	assert.True(t, got1[0].Blocking)

	got2, err := f2.store.RevisionEvents().ListOpenQuestions(ctx, f2.designSessionID)
	require.NoError(t, err)
	require.Len(t, got2, 1)
	assert.Equal(t, "session B's question", got2[0].Text)
	assert.False(t, got2[0].Blocking)
}

// TestRevisionEventStore_Append_ResolvedNamesUnopenedQuestion_Rejected
// proves FR6's stateful validation: a `resolved` entry naming a question
// id never opened anywhere in the session is rejected (ErrInvalidRevisionEvent)
// and writes no row.
func TestRevisionEventStore_Append_ResolvedNamesUnopenedQuestion_Rejected(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/open-questions-unopened-resolve-test")
	self := dsTestSubject("agent-1")

	e := baseRevisionEvent(f, self, self)
	e.OpenQuestionsDelta = store.OpenQuestionsDelta{Resolved: []string{"never-opened"}}
	_, err := f.store.RevisionEvents().Append(ctx, e)
	assert.ErrorIs(t, err, store.ErrInvalidRevisionEvent)

	list, err := f.store.RevisionEvents().ListBySession(ctx, f.designSessionID)
	require.NoError(t, err)
	assert.Empty(t, list, "a rejected Append must insert no row")
}

// TestRevisionEventStore_Append_ResolveWithinSameEventOpened_Succeeds
// proves an edge case the "resolved must have been opened" check must
// handle: a resolved id can name a question opened by the *same* event's
// own Opened list, not only a question opened by an earlier event.
func TestRevisionEventStore_Append_ResolveWithinSameEventOpened_Succeeds(t *testing.T) {
	ctx := context.Background()
	f := newRevisionEventFixture(t, "whale-net/open-questions-same-event-test")
	self := dsTestSubject("agent-1")

	e := baseRevisionEvent(f, self, self)
	e.OpenQuestionsDelta = store.OpenQuestionsDelta{
		Opened:   []store.OpenQuestionOpened{{QuestionID: "q1", Blocking: true, Text: "opened and resolved in one round"}},
		Resolved: []string{"q1"},
	}
	_, err := f.store.RevisionEvents().Append(ctx, e)
	require.NoError(t, err)
}
