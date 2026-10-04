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
	"net/url"
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
	"github.com/whale-net/everything/krill/ui/components"
)

// ---------------------------------------------------------------------------
// fake stores
// ---------------------------------------------------------------------------

// fakeDesignSessions is an in-memory DesignSessionStore, optionally failing
// every call with err so the handlers' error paths are reachable.
type fakeDesignSessions struct {
	byID      map[uuid.UUID]store.DesignSession
	byProduct map[uuid.UUID][]store.DesignSession
	// summaries is what SummarizeByProduct answers: the product-wide
	// aggregate the sessions list now reads (FR d0a63ffb), supplied
	// whole so a list test states each session's derived stage and open
	// blocking-question count directly rather than re-deriving them the
	// way the real SQL does -- which is store's own test's job, not this
	// view's.
	summaries map[uuid.UUID]store.ProductDesignSessionsSummary
	// summaryErr, when set, is what GetSummaryByID returns. Separate from
	// err because the detail page's three reads fail independently
	// (TestDesignRead_ErrorPaths_SeparateStores), and one shared err
	// could not say which read broke.
	summaryErr error
	err        error
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

func (f fakeDesignSessions) SummarizeByProduct(_ context.Context, productID uuid.UUID) (store.ProductDesignSessionsSummary, error) {
	if f.err != nil {
		return store.ProductDesignSessionsSummary{}, f.err
	}
	summary, ok := f.summaries[productID]
	if !ok {
		// The real read distinguishes "no such product" from "no sessions
		// yet"; the empty answer here is the second of those, which is
		// the one an empty-list test wants.
		return store.ProductDesignSessionsSummary{ProductID: productID, Sessions: []store.DesignSessionSummary{}}, nil
	}
	return summary, nil
}

// GetSummaryByID is what the detail page reads for its stage and opening
// identity. It answers out of the SAME summaries map SummarizeByProduct
// does, looked up by the session's own row, so a fake cannot hand the list
// one stage and the detail another -- which is exactly the disagreement
// the real store's one-derivation rule exists to prevent, and a fake that
// kept two sources could hide.
//
// A session present in byID but absent from any seeded summary still reads
// back: the stage is StageOpened, the same answer the real SQL gives a
// session with no revision events, so a detail test that is not about the
// stage does not have to seed one.
func (f fakeDesignSessions) GetSummaryByID(_ context.Context, id uuid.UUID) (store.DesignSessionSummary, error) {
	if f.summaryErr != nil {
		return store.DesignSessionSummary{}, f.summaryErr
	}
	if f.err != nil {
		return store.DesignSessionSummary{}, f.err
	}
	for _, product := range f.summaries {
		for _, row := range product.Sessions {
			if row.ID == id {
				return row, nil
			}
		}
	}
	ds, ok := f.byID[id]
	if !ok {
		return store.DesignSessionSummary{}, fmt.Errorf("%w: design_session id %s", store.ErrNotFound, id)
	}
	return store.DesignSessionSummary{DesignSession: ds, Stage: store.StageOpened}, nil
}

// designSummaries is the aggregate a sessions-list test states directly:
// one summary per session, in the order the read returns them.
func designSummaries(productID uuid.UUID, sessions ...store.DesignSessionSummary) map[uuid.UUID]store.ProductDesignSessionsSummary {
	byProduct := make(map[uuid.UUID]store.ProductDesignSessionsSummary, 1)
	byProduct[productID] = store.ProductDesignSessionsSummary{ProductID: productID, Sessions: sessions}
	return byProduct
}

// designSummary is one session's row of that aggregate, carrying the
// opening submission's first line in the field the view shows it from.
func designSummary(id, productID uuid.UUID, opening string, stage store.Stage, created time.Time, blocking int) store.DesignSessionSummary {
	return store.DesignSessionSummary{
		DesignSession: store.DesignSession{
			ID: id, ProductID: productID, OpeningSubmission: opening, CreatedAt: created,
		},
		Stage:                 stage,
		OpenBlockingQuestions: blocking,
	}
}

// fakeRevisionEvents is an in-memory RevisionEventStore returning canned,
// already-ordered logs and open questions.
//
// logErr and questionsErr fail the two accessors INDEPENDENTLY, which is
// the only way to reach FR e5ad1a5b's two degraded pages: with one shared
// err, a "the log read failed" case necessarily also breaks the question
// read, and a test cannot tell "the rail survives a log failure" apart from
// "the rail fails too". err still fails both, for the older whole-store
// cases.
type fakeRevisionEvents struct {
	bySession     map[uuid.UUID][]store.RevisionEvent
	openQuestions map[uuid.UUID][]store.OpenQuestion
	logErr        error
	questionsErr  error
	err           error
}

func (f fakeRevisionEvents) Append(context.Context, store.NewRevisionEvent) (store.RevisionEvent, error) {
	return store.RevisionEvent{}, f.err
}

func (f fakeRevisionEvents) ListBySession(_ context.Context, sessionID uuid.UUID) ([]store.RevisionEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.logErr != nil {
		return nil, f.logErr
	}
	return f.bySession[sessionID], nil
}

func (f fakeRevisionEvents) ListOpenQuestions(_ context.Context, sessionID uuid.UUID) ([]store.OpenQuestion, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.questionsErr != nil {
		return nil, f.questionsErr
	}
	return f.openQuestions[sessionID], nil
}

// ListLatestSignoffBySessionIDs folds each requested session's canned log
// the same last-signoff-wins way the real store's SQL derives it, so tests
// built against bySession need no separate signoff fixture.
func (f fakeRevisionEvents) ListLatestSignoffBySessionIDs(_ context.Context, sessionIDs []uuid.UUID) (map[uuid.UUID]store.SignoffStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[uuid.UUID]store.SignoffStatus, len(sessionIDs))
	for _, id := range sessionIDs {
		for _, ev := range f.bySession[id] {
			if ev.EventType == store.EventTypeSignoff && ev.SignoffStatus != nil {
				out[id] = *ev.SignoffStatus
			}
		}
	}
	return out, nil
}

// newDesignReadApp builds an App whose only wired stores are the two the
// read surface touches. The read routes are mounted directly (not behind
// RequireAuthFunc) -- renderShell tolerates an absent user, and the FRs
// under test are about the rendered view, not the sign-in gate.
//
// spec is a fakeSpecReader so the un-prefixed session-detail page can
// resolve a product for the last-viewed cookie; it lists none, which
// leaves every page here rendering exactly as it did before.
func newDesignReadApp(ds store.DesignSessionStore, re store.RevisionEventStore) *App {
	// scopes and tasks are what the chrome reads for its Needs-attention
	// badge on every page it renders; without them a nil-panic in the
	// sidebar would be this test's failure rather than a page defect.
	return &App{
		designSessions: ds,
		revisionEvents: re,
		spec:           emptyScopeSpecReader{},
		scopes:         chromeScopes{},
		tasks:          chromeTaskCounter{},
	}
}

func designReadMux(app *App) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /design/products/{productID}/design-sessions", app.handleDesignSessionList)
	mux.HandleFunc("GET /design/products/{productID}/design-sessions/{id}", app.handleDesignSessionDetail)
	// The new-session blade, a literal segment beside that {id} wildcard.
	mux.HandleFunc("GET /design/products/{productID}/design-sessions/new", app.handleDesignSessionNew)
	// The un-prefixed design root, which resolves a product and serves
	// that product's list: one page at two URLs.
	mux.HandleFunc("GET "+designPath, app.handleDesign)
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

	rec := get(designReadMux(app), designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()

	// Every event type appears, as its own round badge.
	for _, et := range []store.EventType{store.EventTypeDraft, store.EventTypeAnswer, store.EventTypeSignoff, store.EventTypeRuling} {
		assert.Contains(t, body, string(et), "event type %q must be rendered", et)
	}

	// Log order is the store's: the timeline renders each round's own seq_no,
	// and the entries come out in ascending order, which is the exact list
	// get_design_session returns (FR a1b955e4).
	got := parseRenderedEvents(t, body)
	require.Len(t, got, len(log), "one timeline entry per wire event")
	for i, w := range got {
		assert.Equal(t, log[i].SeqNo, w.SeqNo, "round %d renders the store's own seq_no", i)
		if i > 0 {
			assert.Less(t, got[i-1].SeqNo, w.SeqNo, "the timeline is in ascending seq_no order")
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
		byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
		summaries: designSummaries(productID, designSummary(
			sessionID, productID, "design the rollback story", store.StageInDraft, time.Now(), 0)),
	}
	re := fakeRevisionEvents{bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}}}
	app := newDesignReadApp(ds, re)
	mux := designReadMux(app)

	// The list's DetailPath is exactly the URL the detail handler serves.
	detailURL := designSessionPath(productID, sessionID)
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

// TestDesignSessionDetail_OpenQuestionsTagged is FR db08d930: the rail's
// card shows every currently-open question, each tagged blocking or
// non-blocking through the one mapper that vocabulary has, matching
// list_open_questions.
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

	rec := get(designReadMux(app), designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := parseRenderedOpenQuestions(t, rec.Body.String())
	require.Len(t, got, 2, "one entry per open question")
	for i, q := range []store.OpenQuestion{
		{QuestionID: "blocker", Text: "needs a decision", Blocking: true},
		{QuestionID: "nit", Text: "wording tweak", Blocking: false},
	} {
		assert.Equal(t, q.QuestionID, got[i].QuestionID)
		assert.Equal(t, q.Text, got[i].Text)
		// The tag is the shared label mapper's own answer, not a class
		// literal: the claim under test is that the vocabulary resolves
		// through one owner (FR dcecb049), and asserting against the mapper
		// keeps that true across a restyle of either side.
		assert.Equal(t, components.QuestionBlockingLabel(wantBlockingTag(q.Blocking)), got[i].Tag,
			"%s: the badge must carry the shared label for this tag", q.QuestionID)
	}
}

// ---------------------------------------------------------------------------
// error paths: 400 / 404 / 500
// ---------------------------------------------------------------------------

func TestDesignSessionDetail_ErrorPaths(t *testing.T) {
	sessionID := uuid.New()
	productID := uuid.New()
	okDS := fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}}}
	okRE := fakeRevisionEvents{}

	t.Run("malformed id is 400", func(t *testing.T) {
		rec := get(designReadMux(newDesignReadApp(okDS, okRE)),
			"/design/products/"+uuid.NewString()+"/design-sessions/not-a-uuid")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()), "a 400 must carry a message, not a blank page")
	})

	t.Run("malformed product id is 400", func(t *testing.T) {
		rec := get(designReadMux(newDesignReadApp(okDS, okRE)), "/design/products/not-a-uuid/design-sessions/"+uuid.NewString())
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()), "a 400 must carry a message, not a blank page")
	})

	t.Run("store error is 500 with a message", func(t *testing.T) {
		boom := fmt.Errorf("db is down")
		app := newDesignReadApp(fakeDesignSessions{err: boom}, fakeRevisionEvents{err: boom})
		rec := get(designReadMux(app), designSessionPath(productID, sessionID))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
		assert.NotContains(t, rec.Body.String(), "db is down", "a store error must not leak its text to the browser")
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
	AtExact         string
	AtTitle         string
	AtRelative      string
	CreatedAt       string
	Summary         string
	VerifiedAgainst string
	SignoffStatus   string
	Deltas          []renderedDelta
	Opened          []renderedOpened
	Resolved        []string
}

// The rendered-log parsers below read the timeline's markup. Each one names
// a stable data-krill hook or the element's own semantic tag rather than a
// class or a heading's layout, so a restyle of the timeline cannot break
// them (htmxui ARCHITECTURE §14: assert the claim, not the byte layout).
var (
	// reEventSeq reads the store's own seq_no off the entry, which is what
	// "the list is in log order" is actually about: the number is the
	// store's, and the entry renders it rather than a template's own count.
	reEventSeq  = regexp.MustCompile(`data-krill="revision-event" data-krill-seq-no="(\d+)"`)
	reEventType = regexp.MustCompile(`data-krill="revision-event"[^>]*data-krill-event-type="([^"]+)"`)
	// reEventBadge reads the round's LABELLED badge -- the spine node is an
	// unlabelled badge in the same colour and must not be mistaken for it.
	reEventBadge = regexp.MustCompile(`<span[^>]*data-krill="design-session-event-type">([^<]*)</span>`)
	reSignoff    = regexp.MustCompile(`<dd data-krill="revision-event-signoff">([^<]*)</dd>`)
	reActing     = regexp.MustCompile(`<span[^>]*data-krill="revision-event-acting">([^<]*)</span>`)
	reOnBehalfOf = regexp.MustCompile(`<span[^>]*data-krill="revision-event-on-behalf-of">on behalf of ([^<]*)</span>`)
	reAt         = regexp.MustCompile(`<time[^>]*data-krill="revision-event-at"[^>]*datetime="([^"]+)"[^>]*title="([^"]+)"[^>]*>([^<]*)</time>`)
	reEventID    = regexp.MustCompile(`<code data-krill="revision-event-id">([^<]*)</code>`)
	reVerified   = regexp.MustCompile(`<dd data-krill="revision-event-verified-against">([^<]*)</dd>`)
	reSummary    = regexp.MustCompile(`<p class="mt-1 text-sm" data-krill="revision-event-summary">([^<]*)</p>`)
	reDeltaLI    = regexp.MustCompile(`(?s)<li[^>]*>\s*(created|updated) <code>([^<]+)</code> — (.*?)</li>`)
	reOpenedLI   = regexp.MustCompile(`(?s)<li[^>]*><code>([^<]+)</code> \((blocking|non-blocking)\): (.*?)</li>`)
	// reResolvedSec scopes the resolved ids to their own paragraph, which
	// is the only place a bare <code> is a question id rather than an
	// entity id.
	reResolvedSec = regexp.MustCompile(`(?s)Resolved questions</p><p>(.*?)</p>`)
	reCodeTag     = regexp.MustCompile(`<code>([^<]*)</code>`)
)

// The design page's regions carry stable ids (pages/design.templ), and
// every section-scoped parse slices on those. Slicing on a heading's
// literal markup instead would break the moment a heading gained a class
// -- htmxui ARCHITECTURE §14: assert the claim, not the byte layout.
const (
	regionSessions     = `id="design-sessions"`
	// regionSessionsEnd bounds that region: the table lives in a
	// <section>, and slicing to the next shell landmark keeps an
	// assertion about this page off the chrome around it.
	regionSessionsEnd = "</section>"
	regionRevisionLog  = `id="revision-events"`
	regionOpenQuestion = `id="open-questions"`
	regionSessionNav   = `id="session-nav"`
)

// pageSection returns the slice of body between the two given region ids,
// so parsing one section never picks up the shell's or a neighbouring
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
// own event's block. It matches on the "<li" prefix, not "<li>", so a list
// item carrying daisyUI classes still counts.
func topLevelLIs(region string) []string {
	var out []string
	depth, start := 0, -1
	for i := 0; i < len(region); {
		switch {
		case strings.HasPrefix(region[i:], "<li"):
			if depth == 0 {
				start = i
			}
			depth++
			i += len("<li")
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

// parseRenderedEvents reads the timeline back out of the served page: one
// renderedEvent per <li data-krill="revision-event">, which is the element
// the timeline itself marks rather than one this test guesses at.
func parseRenderedEvents(t *testing.T, body string) []renderedEvent {
	t.Helper()
	region := pageSection(t, body, regionRevisionLog, regionOpenQuestion)
	blocks := topLevelLIs(region)
	events := make([]renderedEvent, 0, len(blocks))
	for _, b := range blocks {
		seq := firstSubmatch(reEventSeq, b)
		require.NotEmpty(t, seq, "every event block must carry its own seq_no: %q", b)
		n, err := strconv.Atoi(seq)
		require.NoError(t, err)

		at := reAt.FindStringSubmatch(b)
		require.NotNil(t, at, "every event block must render its own instant: %q", b)

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
			SeqNo:           n,
			EventType:       firstSubmatch(reEventType, b),
			EventID:         firstSubmatch(reEventID, b),
			Acting:          firstSubmatch(reActing, b),
			OnBehalfOf:      firstSubmatch(reOnBehalfOf, b),
			AtExact:         at[1],
			AtTitle:         at[2],
			AtRelative:      at[3],
			CreatedAt:       recordedInstant(b),
			Summary:         firstSubmatch(reSummary, b),
			VerifiedAgainst: firstSubmatch(reVerified, b),
			SignoffStatus:   firstSubmatch(reSignoff, b),
			Deltas:          deltas,
			Opened:          opened,
			Resolved:        resolved,
		})
	}
	return events
}

// recordedInstant is the round's UTC instant as the entry's own detail line
// records it. It is the human form; AtExact is the machine one, and the two
// are asserted separately because an operator hovers for the second and a
// reader auditing a round reads the first.
func recordedInstant(block string) string {
	i := strings.Index(block, `data-krill="revision-event-recorded-at"`)
	if i < 0 {
		return ""
	}
	rest := block[i:]
	j := strings.Index(rest, "</dd>")
	if j < 0 {
		return ""
	}
	rest = rest[strings.Index(rest, ">")+1 : j]
	return strings.TrimSpace(rest)
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

// wantRenderedOnBehalfOf is what the timeline should show for a round's
// on_behalf_of: the wire's own triple when the store recorded a SECOND,
// different actor, and nothing at all otherwise.
//
// Written out here rather than read off the view so a change to the view's
// rule has to change this too. The rule is about what an operator reads, not
// about the schema: "on behalf of yourself" repeated down every round of a
// timeline reads as a second, different actor, and a round the store
// recorded with no on-behalf-of subject at all must not acquire an empty
// one.
func wantRenderedOnBehalfOf(w handlers.RevisionEventWire) string {
	if w.OnBehalfOf.Sub == "" {
		return ""
	}
	if w.OnBehalfOf == w.Acting {
		return ""
	}
	return strings.TrimSpace(wantSubject(w.OnBehalfOf))
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
	rec := get(designReadMux(app), designSessionPath(productID, sessionID))
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
		assert.Equal(t, wantRenderedOnBehalfOf(w), r.OnBehalfOf, "%s: on_behalf_of identity triple", where)
		assert.Equal(t, w.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"), r.CreatedAt, "%s: created_at", where)
		// The instant is also machine-readable on the entry's own <time>,
		// which is what makes it hoverable and what the head script's
		// data-krill-updated-at hook reads.
		assert.Equal(t, w.CreatedAt.UTC().Format(time.RFC3339), r.AtExact, "%s: the exact instant on the time element", where)
		assert.Equal(t, w.CreatedAt.UTC().Format(time.RFC3339), r.AtTitle, "%s: and its title, so hovering answers exactly when", where)
		assert.NotEmpty(t, r.AtRelative, "%s: the time element renders a readable age server-side", where)
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

// TestDesignSessionDetail_OpenQuestionsMatchListOpenQuestionsWire is FR
// db08d930's parity criterion: the rail's card matches
// ListOpenQuestionsResponse field by field, each question tagged blocking or
// non-blocking and carrying a box whose value is its own id.
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
	rec := get(designReadMux(app), designSessionPath(productID, sessionID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := parseRenderedOpenQuestions(t, rec.Body.String())
	require.Len(t, got, len(wire.OpenQuestions), "one entry per open question, no more, no fewer")
	for i, w := range wire.OpenQuestions {
		r := got[i]
		where := w.QuestionID
		assert.Equal(t, w.QuestionID, r.QuestionID, "%s: question_id", where)
		assert.Equal(t, w.Text, r.Text, "%s: text", where)
		assert.Equal(t, wantBlockingTag(w.Blocking), r.Tag, "%s: blocking tag", where)
		assert.Equal(t, w.QuestionID, r.ResolveValue,
			"%s: the box's value is the question id the answer posts", where)
	}
}

// renderedOpenQuestion is one rail entry read back off the served page. It
// is keyed on the data-krill hook the card marks rather than on its layout,
// so the entry can restyle without this parse noticing.
type renderedOpenQuestion struct {
	QuestionID   string
	Text         string
	Tag          string
	ResolveValue string
	// Form is the id of the form the entry's box belongs to by reference,
	// and Checked whether it rendered ticked -- both read off the input's
	// own tag, because attribute serialisation order is not a contract.
	Form    string
	Checked bool
}

var (
	reOpenQuestionEntry = regexp.MustCompile(`<label[^>]*data-krill="open-question-row" data-krill-question-id="([^"]+)"(.*?)</label>`)
	reResolveInput      = regexp.MustCompile(`<input[^>]*data-krill="open-question-resolve"[^>]*>`)
	reResolveValue      = regexp.MustCompile(`value="([^"]*)"`)
	reResolveForm       = regexp.MustCompile(`form="([^"]*)"`)
	reBlockingTag       = regexp.MustCompile(`<span[^>]*data-krill="question-blocking">([^<]*)</span>`)
	reQuestionText      = regexp.MustCompile(`<span class="block text-sm">([^<]*)</span>`)
)

func parseRenderedOpenQuestions(t *testing.T, body string) []renderedOpenQuestion {
	t.Helper()
	region := pageSection(t, body, regionOpenQuestion, regionSessionNav)
	var out []renderedOpenQuestion
	for _, m := range reOpenQuestionEntry.FindAllStringSubmatch(region, -1) {
		inner := m[2]
		entry := renderedOpenQuestion{
			QuestionID: m[1],
			Tag:        firstSubmatch(reBlockingTag, inner),
			Text:       firstSubmatch(reQuestionText, inner),
		}
		if input := reResolveInput.FindString(inner); input != "" {
			entry.ResolveValue = firstSubmatch(reResolveValue, input)
			entry.Form = firstSubmatch(reResolveForm, input)
			entry.Checked = strings.Contains(input, " checked")
		}
		out = append(out, entry)
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

	t.Run("revision-event read error degrades the timeline, not the page", func(t *testing.T) {
		// FR e5ad1a5b: a log that cannot be read costs the operator the
		// timeline and nothing else, so this is a 200 with an alert where
		// the timeline was -- not a 500 that takes the session's opening
		// statement and rail with it.
		app := newDesignReadApp(
			fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}}},
			fakeRevisionEvents{err: boom},
		)
		rec := get(designReadMux(app), designSessionPath(productID, sessionID))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := rec.Body.String()
		assert.Contains(t, body, "could not be loaded", "the failing read says so inline")
		assert.NotContains(t, body, "password authentication", "store error text must not reach the browser")
		// And a failed read is never rendered as an empty list (NFR ca90dc03).
		assert.NotContains(t, body, "No revision events yet.",
			"a read that failed must not be rendered as a log with nothing in it")
	})

	t.Run("aggregate read error is 500 on the list", func(t *testing.T) {
		app := newDesignReadApp(fakeDesignSessions{err: boom}, fakeRevisionEvents{})
		rec := get(designReadMux(app), designProductSessionsPath(productID))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotContains(t, rec.Body.String(), "password authentication")
	})

	t.Run("unknown well-formed id is 404 on both pages", func(t *testing.T) {
		unknown := uuid.NewString()
		mux := designReadMux(newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{}))
		detail := get(mux, designSessionPath(uuid.New(), uuid.MustParse(unknown)))
		assert.Equal(t, http.StatusNotFound, detail.Code)
		assert.NotEmpty(t, strings.TrimSpace(detail.Body.String()))

		// An unknown product is an empty list, not an error.
		list := get(mux, designProductSessionsPath(uuid.New()))
		assert.Equal(t, http.StatusOK, list.Code)
		assert.Contains(t, list.Body.String(), "No design sessions")
	})
}

// the canonical detail route's own 404s: an unknown session, and a session
// that belongs to another product. Both must be in-shell pages, not bare
// http.Error text -- an operator who followed a stale link lands somewhere
// they can navigate out of.
func TestDesignSessionDetail_NotUnderTheProductIsInShell404(t *testing.T) {
	productID, otherID, sessionID := uuid.New(), uuid.New(), uuid.New()
	app := newDesignReadApp(
		fakeDesignSessions{byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: otherID}}},
		fakeRevisionEvents{},
	)

	t.Run("foreign product is 404 in the shell", func(t *testing.T) {
		rec := get(designReadMux(app), designSessionPath(productID, sessionID))
		require.Equal(t, http.StatusNotFound, rec.Code)
		body := rec.Body.String()
		assert.Contains(t, body, "<!DOCTYPE html>", "a 404 must render inside the shell, not as a bare error")
		assert.Contains(t, body, "<main")
		assert.Contains(t, body, "Design session not found")
		assert.NotContains(t, body, sessionID.String(),
			"a 404 must not leak another product's session into the page")
	})

	t.Run("an unknown session is 404 in the shell too", func(t *testing.T) {
		empty := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})
		rec := get(designReadMux(empty), designSessionPath(productID, uuid.New()))
		require.Equal(t, http.StatusNotFound, rec.Code)
		assert.Contains(t, rec.Body.String(), "<main",
			"an unknown id must land in the shell, not on a bare http.Error")
	})
}

// ---------------------------------------------------------------------------
// the JS-free browse target: GET /design/go
// ---------------------------------------------------------------------------

// TestHandleDesignGo covers the design root's product-id browse, which
// replaced an inline <script> that assigned window.location. The rule now
// lives in one place -- here -- and the only user-controlled path segment
// is a UUID this handler validates before it builds the redirect target,
// so a malformed or hostile value is a 400 rather than an open redirect.
func TestHandleDesignGo(t *testing.T) {
	app := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+designGoPath, app.handleDesignGo)

	t.Run("a valid id 302s to that product's session list", func(t *testing.T) {
		productID := uuid.New()
		rec := get(mux, designGoPath+"?product_id="+productID.String())
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Equal(t, designProductSessionsPath(productID), rec.Header().Get("Location"))
	})

	t.Run("a bad id is 400 with a message", func(t *testing.T) {
		for _, bad := range []string{
			"",
			"not-a-uuid",
			"../../etc/passwd",
			"//evil.example",
			"https://evil.example/x",
		} {
			rec := get(mux, designGoPath+"?product_id="+url.QueryEscape(bad))
			assert.Equal(t, http.StatusBadRequest, rec.Code, "product_id=%q must be refused", bad)
			assert.Empty(t, rec.Header().Get("Location"),
				"a refused product_id must never produce a redirect target: %q", bad)
			assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()), "a 400 must carry a message")
		}
	})

	t.Run("surrounding whitespace is tolerated", func(t *testing.T) {
		productID := uuid.New()
		rec := get(mux, designGoPath+"?product_id="+url.QueryEscape("  "+productID.String()+"  "))
		assert.Equal(t, http.StatusFound, rec.Code)
		assert.Equal(t, designProductSessionsPath(productID), rec.Header().Get("Location"))
	})
}

// ---------------------------------------------------------------------------
// one route, two modes: the HX-Request read fragments
// ---------------------------------------------------------------------------

// hxGet issues the same GET an htmx control would, with the HX-Request
// header set.
func hxGet(mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestDesignReads_HXRequestRendersBareFragment proves each read view is
// one route with two modes: with HX-Request the page body comes back as a
// bare fragment at 200 (no chrome, so hx-swap can drop it in place),
// without it the same body comes back inside the shell.
func TestDesignReads_HXRequestRendersBareFragment(t *testing.T) {
	productID := uuid.New()
	sessionID := uuid.New()
	ds := fakeDesignSessions{
		byID: map[uuid.UUID]store.DesignSession{sessionID: {ID: sessionID, ProductID: productID}},
		summaries: designSummaries(productID, designSummary(
			sessionID, productID, "design the rollback story", store.StageInDraft, time.Now(), 0)),
	}
	re := fakeRevisionEvents{
		bySession: map[uuid.UUID][]store.RevisionEvent{sessionID: {event(1, store.EventTypeDraft)}},
	}
	mux := designReadMux(newDesignReadApp(ds, re))

	t.Run("session list", func(t *testing.T) {
		rec := hxGet(mux, designProductSessionsPath(productID))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := rec.Body.String()
		assert.Contains(t, body, sessionID.String(), "the fragment carries the same data the full page does")
		assert.NotContains(t, body, "<!DOCTYPE html>", "the fragment must carry no document chrome")
		assert.NotContains(t, body, "<main", "the fragment must carry no shell chrome")
	})

	t.Run("session detail", func(t *testing.T) {
		rec := hxGet(mux, designSessionPath(productID, sessionID))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		body := rec.Body.String()
		assert.Contains(t, body, string(store.EventTypeDraft), "the fragment carries the same log the full page does")
		assert.NotContains(t, body, "<!DOCTYPE html>", "the fragment must carry no document chrome")
		assert.NotContains(t, body, "<main", "the fragment must carry no shell chrome")
	})

	t.Run("a plain GET still gets the shell", func(t *testing.T) {
		rec := get(mux, designSessionPath(productID, sessionID))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "<main", "the no-JS half must keep the full chrome")
	})
}
