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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
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

// ---------------------------------------------------------------------------
// parity against the exact wire types the MCP read tools return
//
// The three tests below drive their expectations from
// handlers.NewDesignSessionResponse (get_design_session) and
// handlers.NewListOpenQuestionsResponse (list_open_questions) -- the very
// values the MCP tools marshal -- so a divergence in either the wire type or
// the UI template shows up as a field-by-field mismatch, not as a missing
// substring somewhere on the page.
// ---------------------------------------------------------------------------

// renderedEvent / renderedDelta / renderedOpened are the values parsed back
// out of one rendered <li> of the revision-event log, so they can be
// compared field by field against one RevisionEventWire.
type renderedDelta struct {
	Change      string
	EntityID    string
	SummaryLine string
}

type renderedOpened struct {
	QuestionID string
	Tag        string
	Text       string
}

type renderedEvent struct {
	SeqNo           int
	EventType       string
	EventID         string
	Acting          string
	OnBehalfOf      string
	CreatedAt       string
	VerifiedAgainst string
	SignoffStatus   string
	Deltas          []renderedDelta
	Opened          []renderedOpened
	Resolved        []string
}

var (
	reEventHeader = regexp.MustCompile(`<strong>#(\d+) ([^<]+)</strong>`)
	reSignoffCell = regexp.MustCompile(`signoff: ([^<]+)`)
	reEventMeta   = regexp.MustCompile(`acting: ([^&]*) &middot; on behalf of: ([^&]*) &middot; ([^<]*)`)
	reEventID     = regexp.MustCompile(`event id: <code>([^<]+)</code>`)
	reVerified    = regexp.MustCompile(`verified against: ([^<]+)`)
	reDeltaLI     = regexp.MustCompile(`(?s)<li>\s*(created|updated) <code>([^<]+)</code> &mdash; (.*?)</li>`)
	reOpenedLI    = regexp.MustCompile(`(?s)<li><code>([^<]+)</code> \((blocking|non-blocking)\): (.*?)</li>`)
	reResolvedSec = regexp.MustCompile(`(?s)Resolved questions:(.*)$`)
	reCodeTag     = regexp.MustCompile(`<code>([^<]*)</code>`)
)

// pageSection returns the slice of body between the two given markers, so
// parsing one section never picks up the shell's or a neighbouring
// section's markup.
func pageSection(t *testing.T, body, from, to string) string {
	t.Helper()
	i := strings.LastIndex(body, from)
	require.NotEqual(t, -1, i, "body must contain section start %q", from)
	rest := body[i+len(from):]
	if to == "" {
		return rest
	}
	j := strings.LastIndex(rest, to)
	require.NotEqual(t, -1, j, "body must contain section end %q", to)
	return rest[:j]
}

// topLevelLIs splits a region into its outermost <li>...</li> blocks, so an
// event's nested entity-delta and opened-question <li>s stay inside their
// own event's block.
func topLevelLIs(region string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i < len(region); {
		switch {
		case strings.HasPrefix(region[i:], "<li>"):
			if depth == 0 {
				start = i
			}
			depth++
			i += len("<li>")
		case strings.HasPrefix(region[i:], "</li>"):
			depth--
			i += len("</li>")
			if depth == 0 && start >= 0 {
				out = append(out, region[start:i])
				start = -1
			}
		default:
			i++
		}
	}
	return out
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func allSubmatch(re *regexp.Regexp, s string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func parseRenderedEvents(t *testing.T, body string) []renderedEvent {
	t.Helper()
	region := pageSection(t, body, "<h3>Revision events</h3>", "<h3>Open questions</h3>")
	blocks := topLevelLIs(region)
	events := make([]renderedEvent, 0, len(blocks))
	for _, b := range blocks {
		head := reEventHeader.FindStringSubmatch(b)
		require.NotNil(t, head, "every event block must open with #seq type: %q", b)
		seq, err := strconv.Atoi(head[1])
		require.NoError(t, err)

		meta := reEventMeta.FindStringSubmatch(b)
		require.NotNil(t, meta, "every event block must render its acting/on-behalf-of meta: %q", b)

		deltas := make([]renderedDelta, 0)
		for _, m := range reDeltaLI.FindAllStringSubmatch(b, -1) {
			deltas = append(deltas, renderedDelta{Change: m[1], EntityID: m[2], SummaryLine: strings.TrimSpace(m[3])})
		}
		opened := make([]renderedOpened, 0)
		for _, m := range reOpenedLI.FindAllStringSubmatch(b, -1) {
			opened = append(opened, renderedOpened{QuestionID: m[1], Tag: m[2], Text: strings.TrimSpace(m[3])})
		}
		resolved := make([]string, 0)
		if m := reResolvedSec.FindStringSubmatch(b); m != nil {
			resolved = allSubmatch(reCodeTag, m[1])
		}

		events = append(events, renderedEvent{
			SeqNo:           seq,
			EventType:       head[2],
			EventID:         firstSubmatch(reEventID, b),
			Acting:          strings.TrimSpace(meta[1]),
			OnBehalfOf:      strings.TrimSpace(meta[2]),
			CreatedAt:       strings.TrimSpace(meta[3]),
			VerifiedAgainst: firstSubmatch(reVerified, b),
			SignoffStatus:   firstSubmatch(reSignoffCell, b),
			Deltas:          deltas,
			Opened:          opened,
			Resolved:        resolved,
		})
	}
	return events
}

// wantBlockingTag is the test's own rendering of a wire-level blocking
// bool, written out independently of the view's tagger so a change to one
// without the other is caught.
func wantBlockingTag(blocking bool) string {
	if blocking {
		return "blocking"
	}
	return "non-blocking"
}

func wantSubject(w handlers.SubjectWire) string {
	return fmt.Sprintf("%s %s@%s", w.Kind, w.Sub, w.Iss)
}

func wantDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestDesignSessionDetail_LogMatchesGetDesignSessionWire is FR a1b955e4's
// parity criterion: every field of every RevisionEventWire
// get_design_session returns is rendered by the detail page, field by
// field, in the wire's own ascending seq order.
func TestDesignSessionDetail_LogMatchesGetDesignSessionWire(t *testing.T) {
	sessionID := uuid.New()
	productID := uuid.New()
	ds := store.DesignSession{
		ID:                     sessionID,
		ProductID:              productID,
		OpeningSubmission:      "design the rollback story",
		OpenedByKrillSessionID: store.SessionID(uuid.New()),
		CreatedAt:              time.Date(2026, 2, 1, 9, 30, 0, 0, time.UTC),
	}
	alice := store.Subject{Iss: "https://idp.test", Sub: "alice", Kind: store.SubjectKindHuman}
	svc := store.Subject{Iss: "https://svc.test", Sub: "rollback-svc", Kind: store.SubjectKindService}

	at := func(h int) time.Time { return time.Date(2026, 2, 1, h, 0, 0, 0, time.UTC) }
	log := []store.RevisionEvent{
		{
			ID: uuid.MustParse("11111111-0000-0000-0000-000000000001"), SessionID: sessionID, SeqNo: 1,
			Acting: alice, OnBehalfOf: svc, EventType: store.EventTypeDraft,
			VerifiedAgainst: strptr("spec.md#FR2"), CreatedAt: at(10),
			EntityDeltas: []store.EntityDelta{
				{EntityID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"), Change: store.EntityDeltaChangeCreated, SummaryLine: "added the rollback requirement"},
				{EntityID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002"), Change: store.EntityDeltaChangeUpdated, SummaryLine: "tightened the RTO to 5m"},
			},
			OpenQuestionsDelta: store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
				{QuestionID: "q-blocking", Blocking: true, Text: "which store holds the flag?"},
				{QuestionID: "q-nice", Blocking: false, Text: "roll back or rollback?"},
			}},
		},
		{
			ID: uuid.MustParse("11111111-0000-0000-0000-000000000002"), SessionID: sessionID, SeqNo: 2,
			Acting: svc, OnBehalfOf: alice, EventType: store.EventTypeAnswer, CreatedAt: at(11),
			OpenQuestionsDelta: store.OpenQuestionsDelta{Resolved: []string{"q-nice", "q-blocking"}},
		},
		{
			ID: uuid.MustParse("11111111-0000-0000-0000-000000000003"), SessionID: sessionID, SeqNo: 3,
			Acting: alice, EventType: store.EventTypeSignoff, SignoffStatus: signoffptr(store.SignoffStatusChangesRequested), CreatedAt: at(12),
		},
		{
			ID: uuid.MustParse("11111111-0000-0000-0000-000000000004"), SessionID: sessionID, SeqNo: 4,
			Acting: alice, OnBehalfOf: svc, EventType: store.EventTypeReconciliation,
			VerifiedAgainst: strptr("reconciliation.md#round-1"), CreatedAt: at(13),
			EntityDeltas: []store.EntityDelta{
				{EntityID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"), Change: store.EntityDeltaChangeUpdated, SummaryLine: "narrowed the rollback trigger"},
			},
		},
		{
			ID: uuid.MustParse("11111111-0000-0000-0000-000000000005"), SessionID: sessionID, SeqNo: 5,
			Acting: alice, EventType: store.EventTypeSignoff, SignoffStatus: signoffptr(store.SignoffStatusApproved), CreatedAt: at(14),
		},
		{
			ID: uuid.MustParse("11111111-0000-0000-0000-000000000006"), SessionID: sessionID, SeqNo: 6,
			Acting: svc, OnBehalfOf: alice, EventType: store.EventTypeRuling, CreatedAt: at(15),
		},
	}

	// The MCP tool's own answer for this session.
	wire := handlers.NewDesignSessionResponse(ds, log)

	app := newDesignReadApp(
		fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{sessionID: ds}},
		fakeRevisionEvents{bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: log}},
	)
	rec := get(designReadMux(app), designSessionPath(sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()

	// The session's own header fields, as get_design_session returns them.
	assert.Contains(t, body, wire.ID)
	assert.Contains(t, body, wire.ProductID)
	assert.Contains(t, body, wire.OpeningSubmission)
	assert.Contains(t, body, wire.OpenedByKrillSessionID)

	got := parseRenderedEvents(t, body)
	require.Len(t, got, len(wire.RevisionEvents), "one rendered block per wire event, in order")

	for i, w := range wire.RevisionEvents {
		r := got[i]
		where := fmt.Sprintf("event %d (seq %d)", i, w.SeqNo)
		assert.Equal(t, w.SeqNo, r.SeqNo, "%s: seq_no", where)
		assert.Equal(t, w.EventType, r.EventType, "%s: event_type", where)
		assert.Equal(t, w.ID, r.EventID, "%s: the event's own id", where)
		assert.Equal(t, strings.TrimSpace(wantSubject(w.Acting)), r.Acting, "%s: acting identity triple", where)
		assert.Equal(t, strings.TrimSpace(wantSubject(w.OnBehalfOf)), r.OnBehalfOf, "%s: on_behalf_of identity triple", where)
		assert.Equal(t, w.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), r.CreatedAt, "%s: created_at", where)
		assert.Equal(t, wantDeref(w.VerifiedAgainst), r.VerifiedAgainst, "%s: verified_against", where)
		assert.Equal(t, wantDeref(w.SignoffStatus), r.SignoffStatus, "%s: signoff_status", where)

		wantDeltas := make([]renderedDelta, 0, len(w.EntityDeltas))
		for _, d := range w.EntityDeltas {
			wantDeltas = append(wantDeltas, renderedDelta{Change: d.Change, EntityID: d.EntityID, SummaryLine: d.SummaryLine})
		}
		assert.Equal(t, wantDeltas, r.Deltas, "%s: entity_deltas", where)

		wantOpened := make([]renderedOpened, 0, len(w.OpenQuestionsDelta.Opened))
		for _, o := range w.OpenQuestionsDelta.Opened {
			wantOpened = append(wantOpened, renderedOpened{QuestionID: o.QuestionID, Tag: wantBlockingTag(o.Blocking), Text: o.Text})
		}
		assert.Equal(t, wantOpened, r.Opened, "%s: open_questions_delta.opened", where)

		wantResolved := w.OpenQuestionsDelta.Resolved
		if wantResolved == nil {
			wantResolved = []string{}
		}
		assert.Equal(t, wantResolved, r.Resolved, "%s: open_questions_delta.resolved", where)
	}

	// Ascending seq order, read off the rendered ids.
	for i := 1; i < len(got); i++ {
		assert.Less(t, got[i-1].SeqNo, got[i].SeqNo, "events must render in ascending seq_no order")
	}
}

// TestDesignSessionList_StatusMatchesLastSignoffWins is FR 0e9ccfc9's parity
// criterion: every session ListByProduct returns gets exactly one row, and
// its status cell matches the session's own last-signoff-wins derivation --
// computed here independently of the view's.
func TestDesignSessionList_StatusMatchesLastSignoffWins(t *testing.T) {
	productID := uuid.New()

	// wantSignedOff re-derives, in the test, the expected state from a
	// session's log: the last signoff round decides.
	wantSignedOff := func(events []store.RevisionEvent) bool {
		signedOff := false
		for _, ev := range events {
			if ev.EventType != store.EventTypeSignoff {
				continue
			}
			if ev.SignoffStatus != nil && *ev.SignoffStatus == store.SignoffStatusApproved {
				signedOff = true
			} else {
				signedOff = false
			}
		}
		return signedOff
	}

	approved := func(seq int) store.RevisionEvent {
		ev := event(seq, store.EventTypeSignoff)
		ev.SignoffStatus = signoffptr(store.SignoffStatusApproved)
		return ev
	}
	changes := func(seq int) store.RevisionEvent {
		ev := event(seq, store.EventTypeSignoff)
		ev.SignoffStatus = signoffptr(store.SignoffStatusChangesRequested)
		return ev
	}
	nilStatus := func(seq int) store.RevisionEvent {
		ev := event(seq, store.EventTypeSignoff)
		ev.SignoffStatus = nil
		return ev
	}

	cases := []struct {
		name   string
		events []store.RevisionEvent
	}{
		{"never signed", []store.RevisionEvent{event(1, store.EventTypeDraft)}},
		{"changes_requested only", []store.RevisionEvent{event(1, store.EventTypeDraft), changes(2)}},
		{"approved", []store.RevisionEvent{event(1, store.EventTypeDraft), approved(2)}},
		{"approved then changes_requested reopens", []store.RevisionEvent{approved(1), changes(2)}},
		{"changes_requested then approved closes", []store.RevisionEvent{changes(1), approved(2)}},
		{"approved twice stays closed", []store.RevisionEvent{approved(1), approved(2)}},
		{"closed reopened then closed again", []store.RevisionEvent{approved(1), changes(2), approved(3)}},
		{"signoff with no status reads open", []store.RevisionEvent{event(1, store.EventTypeDraft), nilStatus(2)}},
	}

	sessions := make([]store.DesignSession, 0, len(cases))
	events := make(map[uuid.UUID][]store.RevisionEvent, len(cases))
	for i, c := range cases {
		id := uuid.New()
		created := time.Date(2026, 3, 1, 8, i, 0, 0, time.UTC)
		sessions = append(sessions, store.DesignSession{
			ID: id, ProductID: productID,
			OpeningSubmission: fmt.Sprintf("submission %s", c.name),
			CreatedAt:         created,
		})
		events[id] = c.events
	}

	app := newDesignReadApp(
		fakeDesignSessions{byProduct: map[uuid.UUID][]store.DesignSession{productID: sessions}},
		fakeRevisionEvents{bySession: events},
	)
	rec := get(designReadMux(app), designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := parseRenderedSessionRows(t, rec.Body.String())
	require.Len(t, rows, len(sessions), "one row per session ListByProduct returned, no more, no fewer")

	for i, ds := range sessions {
		r := rows[i]
		where := ds.OpeningSubmission
		assert.Equal(t, ds.ID.String(), r.ID, "%s: session id", where)
		assert.Equal(t, "/design/design-sessions/"+ds.ID.String(), r.DetailPath, "%s: detail link", where)
		assert.Equal(t, ds.OpeningSubmission, r.OpeningSubmission, "%s: opening submission", where)
		assert.Equal(t, ds.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), r.CreatedAt, "%s: created", where)

		wantStatus := "open"
		if wantSignedOff(events[ds.ID]) {
			wantStatus = "signed off"
		}
		assert.Equal(t, wantStatus, r.Status, "%s: status cell vs last-signoff-wins", where)
	}
}

type renderedSessionRow struct {
	DetailPath        string
	ID                string
	OpeningSubmission string
	Status            string
	CreatedAt         string
}

var reSessionRow = regexp.MustCompile(
	`<a href="([^"]+)"><code>([^<]+)</code></a></td>\s*<td>(.*?)</td>\s*<td>(signed off|open)</td>\s*<td>(.*?)</td>`)

func parseRenderedSessionRows(t *testing.T, body string) []renderedSessionRow {
	t.Helper()
	region := pageSection(t, body, "<h2>Design sessions</h2>", "")
	var rows []renderedSessionRow
	for _, m := range reSessionRow.FindAllStringSubmatch(region, -1) {
		rows = append(rows, renderedSessionRow{
			DetailPath:        m[1],
			ID:                m[2],
			OpeningSubmission: strings.TrimSpace(m[3]),
			Status:            m[4],
			CreatedAt:         strings.TrimSpace(m[5]),
		})
	}
	return rows
}

// TestDesignSessionDetail_OpenQuestionsMatchListOpenQuestionsWire is FR
// db08d930's parity criterion: the rendered open-questions table matches
// ListOpenQuestionsResponse field by field, each question tagged blocking or
// non-blocking.
func TestDesignSessionDetail_OpenQuestionsMatchListOpenQuestionsWire(t *testing.T) {
	sessionID := uuid.New()
	productID := uuid.New()
	questions := []store.OpenQuestion{
		{QuestionID: "q-blocker", Text: "which store holds the flag?", Blocking: true, OpenedAtSeqNo: 3},
		{QuestionID: "q-wording", Text: "roll back or rollback?", Blocking: false, OpenedAtSeqNo: 1},
		{QuestionID: "q-second", Text: "is the flag durable across restarts?", Blocking: true, OpenedAtSeqNo: 7},
	}

	// list_open_questions' own unfiltered answer.
	wire := handlers.NewListOpenQuestionsResponse(questions, false)

	app := newDesignReadApp(
		fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}}},
		fakeRevisionEvents{
			bySession:     map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}},
			openQuestions: map[uuid.UUID][]store.OpenQuestion{sessionID: questions},
		},
	)
	rec := get(designReadMux(app), designSessionPath(sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := parseRenderedOpenQuestions(t, rec.Body.String())
	require.Len(t, got, len(wire.OpenQuestions), "one row per open question, no more, no fewer")
	for i, w := range wire.OpenQuestions {
		r := got[i]
		where := w.QuestionID
		assert.Equal(t, w.QuestionID, r.QuestionID, "%s: question_id", where)
		assert.Equal(t, w.Text, r.Text, "%s: text", where)
		assert.Equal(t, wantBlockingTag(w.Blocking), r.Tag, "%s: blocking tag", where)
		assert.Equal(t, w.OpenedAtSeqNo, r.OpenedAtSeqNo, "%s: opened_at_seq_no", where)
	}
}

type renderedOpenQuestion struct {
	QuestionID    string
	Text          string
	Tag           string
	OpenedAtSeqNo int
}

var reOpenQuestionRow = regexp.MustCompile(
	`<tr><td><code>([^<]+)</code> &mdash; (.*?)</td><td>(blocking|non-blocking)</td><td>#(\d+)</td></tr>`)

func parseRenderedOpenQuestions(t *testing.T, body string) []renderedOpenQuestion {
	t.Helper()
	region := pageSection(t, body, "<h3>Open questions</h3>", "<p><a href=")
	var out []renderedOpenQuestion
	for _, m := range reOpenQuestionRow.FindAllStringSubmatch(region, -1) {
		seq, err := strconv.Atoi(m[4])
		require.NoError(t, err)
		out = append(out, renderedOpenQuestion{
			QuestionID:    m[1],
			Text:          strings.TrimSpace(m[2]),
			Tag:           m[3],
			OpenedAtSeqNo: seq,
		})
	}
	return out
}

// TestDesignRead_ErrorPaths_SeparateStores covers the error paths per store
// read rather than as one all-stores-fail case: each of the four reads the
// detail view makes (GetByID, ListBySession, ListOpenQuestions) and the two
// the list view makes (ListByProduct, ListBySession) must surface the right
// status without leaking the store's error text.
func TestDesignRead_ErrorPaths_SeparateStores(t *testing.T) {
	sessionID := uuid.New()
	productID := uuid.New()
	boom := fmt.Errorf("pq: password authentication failed for user krill")

	t.Run("revision-event read error is 500", func(t *testing.T) {
		app := newDesignReadApp(
			fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}}},
			fakeRevisionEvents{err: boom},
		)
		rec := get(designReadMux(app), designSessionPath(sessionID))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
		assert.NotContains(t, rec.Body.String(), "password authentication", "store error text must not reach the browser")
	})

	t.Run("list 500 when a session's log read fails", func(t *testing.T) {
		app := newDesignReadApp(
			fakeDesignSessions{byProduct: map[uuid.UUID][]store.DesignSession{productID: {{ID: sessionID, ProductID: productID}}}},
			fakeRevisionEvents{err: boom},
		)
		rec := get(designReadMux(app), designProductSessionsPath(productID))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "password authentication")
	})

	t.Run("unknown well-formed id is 404 on both pages", func(t *testing.T) {
		unknown := uuid.NewString()
		mux := designReadMux(newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{}))
		detail := get(mux, "/design/design-sessions/"+unknown)
		assert.Equal(t, http.StatusNotFound, detail.Code)
		assert.NotEmpty(t, strings.TrimSpace(detail.Body.String()))

		// An unknown product is an empty list, not an error.
		list := get(mux, designProductSessionsPath(uuid.New()))
		assert.Equal(t, http.StatusOK, list.Code)
		assert.Contains(t, list.Body.String(), "No design sessions")
	})
}
