// Coverage for the design-session read surface (design_page.go): a
// product's session list, one session's full ordered revision-event log,
// and its currently-open questions.
//
// These handlers are the browser's twin of the MCP read tools
// (get_design_session / list_open_questions), so what is asserted here is
// the parity itself: every session in the list is a working link, every
// event round is rendered in seq_no order with the fields the wire type
// exposes, and the open-question table matches list_open_questions. The
// stores are faked (no database) so the assertions target the view
// assembly, not the SQL beneath it.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// ---------------------------------------------------------------------------
// fake stores
// ---------------------------------------------------------------------------

// fakeDesignSessions is an in-memory DesignSessionStore, optionally failing
// every call with err so the handlers' error paths are reachable.
type fakeDesignSessions struct {
	byID      map[uuid.UUID]store.DesignSession
	byProduct map[uuid.UUID][]store.DesignSession
	err       error
}

func (f fakeDesignSessions) Open(context.Context, uuid.UUID, uuid.UUID, string, store.SessionID) (store.DesignSession, error) {
	return store.DesignSession{}, f.err
}

func (f fakeDesignSessions) GetByID(_ context.Context, id uuid.UUID) (store.DesignSession, error) {
	if f.err != nil {
		return store.DesignSession{}, f.err
	}
	ds, ok := f.byID[id]
	if !ok {
		return store.DesignSession{}, fmt.Errorf("%w: design_session id %s", store.ErrNotFound, id)
	}
	return ds, nil
}

func (f fakeDesignSessions) ListByProduct(_ context.Context, productID uuid.UUID) ([]store.DesignSession, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProduct[productID], nil
}

// fakeRevisionEvents is an in-memory RevisionEventStore returning canned,
// already-ordered logs and open questions.
type fakeRevisionEvents struct {
	bySession     map[uuid.UUID][]store.RevisionEvent
	openQuestions map[uuid.UUID][]store.OpenQuestion
	err           error
}

func (f fakeRevisionEvents) Append(context.Context, store.NewRevisionEvent) (store.RevisionEvent, error) {
	return store.RevisionEvent{}, f.err
}

func (f fakeRevisionEvents) ListBySession(_ context.Context, sessionID uuid.UUID) ([]store.RevisionEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.bySession[sessionID], nil
}

func (f fakeRevisionEvents) ListOpenQuestions(_ context.Context, sessionID uuid.UUID) ([]store.OpenQuestion, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.openQuestions[sessionID], nil
}

// newDesignReadApp builds an App whose only wired stores are the two the
// read surface touches. The read routes are mounted directly (not behind
// RequireAuthFunc) -- renderShell tolerates an absent user, and the FRs
// under test are about the rendered view, not the sign-in gate.
func newDesignReadApp(ds store.DesignSessionStore, re store.RevisionEventStore) *App {
	return &App{designSessions: ds, revisionEvents: re}
}

func designReadMux(app *App) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /design/products/{productID}/design-sessions", app.handleDesignSessionList)
	mux.HandleFunc("GET /design/design-sessions/{id}", app.handleDesignSessionDetail)
	return mux
}

func get(mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// event builds one RevisionEvent round with a deterministic identity so a
// test can assert the rendered event id.
func event(seqNo int, et store.EventType) store.RevisionEvent {
	return store.RevisionEvent{
		ID:        uuid.NewSHA1(uuid.Nil, []byte(fmt.Sprintf("event-%d", seqNo))),
		SessionID: uuid.Nil,
		SeqNo:     seqNo,
		Acting:    store.Subject{Iss: "https://idp.test", Sub: "producer", Kind: store.SubjectKindHuman},
		EventType: et,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seqNo) * time.Hour),
	}
}

func strptr(s string) *string { return &s }

func signoffptr(s store.SignoffStatus) *store.SignoffStatus { return &s }

// ---------------------------------------------------------------------------
// FR 0e9ccfc9 -- list every session, open and signed-off, as a working link
// ---------------------------------------------------------------------------

// TestDesignSessionList_LinksEverySessionAndTagsStatus is FR 0e9ccfc9: the
// product's list shows every session -- an approved one (signed off), a
// changes_requested one (reopened, so open), and a never-signed one (open)
// -- and each is a working link to its own detail page, so a contributor
// reaches a session without already holding its id.
func TestDesignSessionList_LinksEverySessionAndTagsStatus(t *testing.T) {
	productID := uuid.New()

	signedOffID := uuid.New()
	reopenedID := uuid.New()
	openID := uuid.New()

	approved := event(2, store.EventTypeSignoff)
	approved.SignoffStatus = signoffptr(store.SignoffStatusApproved)

	changes := event(3, store.EventTypeSignoff)
	changes.SignoffStatus = signoffptr(store.SignoffStatusChangesRequested)

	ds := fakeDesignSessions{byProduct: map[uuid.UUID][]store.DesignSession{productID: {
		{ID: signedOffID, ProductID: productID, OpeningSubmission: "first submission", CreatedAt: time.Now()},
		{ID: reopenedID, ProductID: productID, OpeningSubmission: "second submission", CreatedAt: time.Now()},
		{ID: openID, ProductID: productID, OpeningSubmission: "third submission", CreatedAt: time.Now()},
	}}}
	re := fakeRevisionEvents{bySession: map[uuid.UUID][]store.RevisionEvent{
		signedOffID: {event(1, store.EventTypeDraft), approved},
		reopenedID:  {approved, changes}, // changes_requested after approval reopens it
		openID:      {event(1, store.EventTypeDraft)},
	}}
	app := newDesignReadApp(ds, re)

	rec := get(designReadMux(app), "/design/products/"+productID.String()+"/design-sessions")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()

	// Every session is listed and links into its own detail page.
	assert.Contains(t, body, signedOffID.String())
	assert.Contains(t, body, reopenedID.String())
	assert.Contains(t, body, openID.String())
	for _, id := range []uuid.UUID{signedOffID, reopenedID, openID} {
		assert.Contains(t, body, "/design/design-sessions/"+id.String(), "each session must link to its detail page")
	}

	// The approved session reads signed off; the changes_requested one and
	// the never-signed one read open. Anchor on the status cell so a row's
	// own submission text can never satisfy the count.
	assert.Equal(t, 1, strings.Count(body, "<td>signed off</td>"), "only the approved signoff session is signed off")
	assert.Equal(t, 2, strings.Count(body, "<td>open</td>"), "changes_requested and never-signed sessions both read open")
}

// ---------------------------------------------------------------------------
// FR a1b955e4 -- the full ordered log, every event type, every wire field
// ---------------------------------------------------------------------------

// TestDesignSessionDetail_FullOrderedLog walks one session whose log covers
// all five event types and every field get_design_session's RevisionEventWire
// exposes -- proving the detail view carries the same content, in seq_no
// order, as the MCP tool's own log (FR a1b955e4).
func TestDesignSessionDetail_FullOrderedLog(t *testing.T) {
	sessionID := uuid.New()
	productID := uuid.New()

	draft := event(1, store.EventTypeDraft)
	draft.VerifiedAgainst = strptr("spec.md#1")
	draft.EntityDeltas = []store.EntityDelta{{
		EntityID:    uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"),
		Change:      store.EntityDeltaChangeCreated,
		SummaryLine: "added the rollback requirement",
	}}
	draft.OpenQuestionsDelta.Opened = []store.OpenQuestionOpened{{
		QuestionID: "q1", Blocking: true, Text: "which store holds the flag?",
	}}

	answer := event(2, store.EventTypeAnswer)
	answer.OpenQuestionsDelta.Resolved = []string{"q1"}

	signoff := event(3, store.EventTypeSignoff)
	signoff.SignoffStatus = signoffptr(store.SignoffStatusApproved)

	ruling := event(4, store.EventTypeRuling)

	log := []store.RevisionEvent{draft, answer, signoff, ruling}

	ds := fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{
		sessionID: {ID: sessionID, ProductID: productID, OpeningSubmission: "design the rollback story", CreatedAt: time.Now()},
	}}
	re := fakeRevisionEvents{bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: log}}
	app := newDesignReadApp(ds, re)

	rec := get(designReadMux(app), "/design/design-sessions/"+sessionID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()

	// Every event type appears.
	for _, et := range []store.EventType{store.EventTypeDraft, store.EventTypeAnswer, store.EventTypeSignoff, store.EventTypeRuling} {
		assert.Contains(t, body, string(et), "event type %q must be rendered", et)
	}

	// seq_no order is preserved: draft(1) before answer(2) before signoff(3)
	// before ruling(4), by position in the rendered body.
	order := []int{indexOf(body, "#1 draft"), indexOf(body, "#2 answer"), indexOf(body, "#3 signoff"), indexOf(body, "#4 ruling")}
	for i, pos := range order {
		require.NotEqual(t, -1, pos, "event %d must be rendered", i+1)
		if i > 0 {
			assert.Less(t, order[i-1], pos, "events must render in seq_no order")
		}
	}

	// Wire fields: each event's own id, verified_against, signoff_status,
	// the entity delta, and the opened/resolved questions all render.
	assert.Contains(t, body, draft.ID.String(), "each event's own id must render")
	assert.Contains(t, body, ruling.ID.String(), "each event's own id must render")
	assert.Contains(t, body, "spec.md#1", "verified_against must render")
	assert.Contains(t, body, string(store.SignoffStatusApproved), "signoff_status must render")
	assert.Contains(t, body, "added the rollback requirement", "entity delta summary must render")
	assert.Contains(t, body, draft.EntityDeltas[0].EntityID.String(), "entity delta id must render")
	assert.Contains(t, body, "which store holds the flag?", "opened question text must render")
	assert.Contains(t, body, "q1", "opened/resolved question id must render")

	// The detail page offers a way back to the list (navigability).
	assert.Contains(t, body, "/design/products/"+productID.String()+"/design-sessions",
		"the detail page must link back to the product's session list")
}

// indexOf returns the byte offset of sub in body, or -1.
func indexOf(body, sub string) int { return strings.Index(body, sub) }

// TestDesignSessionDetail_SameFromListAndDirect proves a session reached by
// clicking through the list renders identically to one reached by typing its
// id (FR 0e9ccfc9's navigability requirement): both go through the same
// detail handler over the same store reads.
func TestDesignSessionDetail_SameFromListAndDirect(t *testing.T) {
	sessionID := uuid.New()
	productID := uuid.New()
	ds := fakeDesignSessions{
		byID:      map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
		byProduct: map[uuid.UUID][]store.DesignSession{productID: {{ID: sessionID, ProductID: productID}}},
	}
	re := fakeRevisionEvents{bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}}}
	app := newDesignReadApp(ds, re)
	mux := designReadMux(app)

	// The list's DetailPath is exactly the URL the detail handler serves.
	detailURL := designSessionPath(sessionID)
	listRec := get(mux, designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	assert.Contains(t, listRec.Body.String(), `href="`+detailURL+`"`, "the list must link to the detail path the handler serves")

	detailRec := get(mux, detailURL)
	require.Equal(t, http.StatusOK, detailRec.Code, detailRec.Body.String())
	assert.Contains(t, detailRec.Body.String(), sessionID.String())
}

// ---------------------------------------------------------------------------
// FR db08d930 -- open questions, each tagged blocking or non-blocking
// ---------------------------------------------------------------------------

// TestDesignSessionDetail_OpenQuestionsTagged is FR db08d930: the open
// questions table shows every currently-open question, each tagged blocking
// or non-blocking, matching list_open_questions.
func TestDesignSessionDetail_OpenQuestionsTagged(t *testing.T) {
	sessionID := uuid.New()
	productID := uuid.New()
	ds := fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{
		sessionID: {ID: sessionID, ProductID: productID},
	}}
	re := fakeRevisionEvents{
		bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}},
		openQuestions: map[uuid.UUID][]store.OpenQuestion{sessionID: {
			{QuestionID: "blocker", Text: "needs a decision", Blocking: true, OpenedAtSeqNo: 1},
			{QuestionID: "nit", Text: "wording tweak", Blocking: false, OpenedAtSeqNo: 1},
		}},
	}
	app := newDesignReadApp(ds, re)

	rec := get(designReadMux(app), "/design/design-sessions/"+sessionID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()

	for _, q := range []store.OpenQuestion{
		{QuestionID: "blocker", Text: "needs a decision", Blocking: true},
		{QuestionID: "nit", Text: "wording tweak", Blocking: false},
	} {
		assert.Contains(t, body, q.QuestionID)
		assert.Contains(t, body, q.Text)
	}
	assert.Equal(t, 1, strings.Count(body, ">blocking<"), "exactly one question is blocking")
	assert.Equal(t, 1, strings.Count(body, ">non-blocking<"), "exactly one question is non-blocking")
}

// ---------------------------------------------------------------------------
// error paths: 400 / 404 / 500
// ---------------------------------------------------------------------------

func TestDesignSessionDetail_ErrorPaths(t *testing.T) {
	sessionID := uuid.New()
	okDS := fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID}}}
	okRE := fakeRevisionEvents{}

	t.Run("malformed id is 400", func(t *testing.T) {
		rec := get(designReadMux(newDesignReadApp(okDS, okRE)), "/design/design-sessions/not-a-uuid")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()), "a 400 must carry a message, not a blank page")
	})

	t.Run("unknown id is 404", func(t *testing.T) {
		rec := get(designReadMux(newDesignReadApp(okDS, okRE)), "/design/design-sessions/"+uuid.NewString())
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("store error is 500 with a message", func(t *testing.T) {
		boom := fmt.Errorf("db is down")
		app := newDesignReadApp(fakeDesignSessions{err: boom}, fakeRevisionEvents{err: boom})
		rec := get(designReadMux(app), "/design/design-sessions/"+sessionID.String())
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
		assert.NotContains(t, rec.Body.String(), "db is down", "a store error must not leak its text to the browser")
	})
}

func TestDesignSessionList_ErrorPaths(t *testing.T) {
	t.Run("malformed product id is 400", func(t *testing.T) {
		app := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})
		rec := get(designReadMux(app), "/design/products/not-a-uuid/design-sessions")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
	})

	t.Run("store error is 500 with a message", func(t *testing.T) {
		boom := fmt.Errorf("db is down")
		app := newDesignReadApp(fakeDesignSessions{err: boom}, fakeRevisionEvents{})
		rec := get(designReadMux(app), "/design/products/"+uuid.NewString()+"/design-sessions")
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "db is down")
	})

	t.Run("empty product renders an empty list, not an error", func(t *testing.T) {
		app := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})
		rec := get(designReadMux(app), "/design/products/"+uuid.NewString()+"/design-sessions")
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "No design sessions")
	})
}
