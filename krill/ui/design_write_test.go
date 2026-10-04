// Coverage for the design-session write surface (design_write.go): a
// Requirement Contributor's two browser writes -- opening a session from a
// plain-language opening_submission, and submitting a follow-up answer --
// driven through the real form routes and the real write client against a
// fake krill api that records what actually went over the wire.
//
// What these tests pin, beyond "it returned 303":
//
//   - the exact body each write sends, asserted against literals rather than
//     the production request structs, so drift in either wire type fails;
//   - that the open body has no entity reference of any kind (FR8) and the
//     answer body always sends an empty entity_deltas (FR 1ff1c1e9) -- the
//     UI turns a submission into a spec entity only through the mediated
//     propose_entities path an Agent drives, never from the browser;
//   - that both writes are attributed to the signed-in operator's real
//     (iss, sub), as acting and on-behalf-of, where the subject under test
//     is a uuid minted fresh per test so no hardcoded identity can satisfy
//     the assertion;
//   - that a rejected write re-renders the form in-shell with the operator's
//     text and ticks intact, and that an empty submission never reaches krill.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The two open questions the seeded session carries: one the answer under
// test closes, one it does not, so a re-rendered tick is provably per-id.
const (
	testClosedQuestion = "q-flag-store"
	testKeptQuestion   = "q-wording"
)

var (
	testClosedQuestionText = "which store holds the flag?"
	testKeptQuestionText   = "roll back or rollback?"
)

// ---------------------------------------------------------------------------
// fake read-side stores
//
// The write paths re-render a read view after a rejection, so a
// write-surface test needs the two stores design_page.go reads. Their write
// methods fail: this browser surface reaches krill only through the api, so a
// store Open/Append would mean the UI wrote a row behind api's gate.
// ---------------------------------------------------------------------------

type writeSurfaceSessions struct {
	byID      map[uuid.UUID]store.DesignSession
	byProduct map[uuid.UUID][]store.DesignSession

	// stage is the derived stage GetSummaryByID reports. It is the ONE
	// derivation the page reads for both the badge and whether a follow-up
	// form is shown, so a test drives the signed-off case by setting it
	// here rather than by constructing some second fact.
	stage store.Stage

	// summarizeErr fails only the aggregate READ the refused-open re-render
	// makes, so a test can reach the branch where the session list behind
	// the blade is unavailable and the blade has to come back anyway.
	summarizeErr error

	// summaryErr fails the SESSION read the follow-up round is re-derived
	// from, so a test can reach the branch where the re-read itself fails
	// and the operator's ticked ids have to survive as hidden inputs.
	summaryErr error
}

func (f writeSurfaceSessions) derivedStage() store.Stage {
	if f.stage == "" {
		return store.StageOpened
	}
	return f.stage
}

func (f writeSurfaceSessions) Open(context.Context, uuid.UUID, uuid.UUID, string, store.SessionID) (store.DesignSession, error) {
	return store.DesignSession{}, errors.New("the browser write path must not open a session through the store")
}

func (f writeSurfaceSessions) GetByID(_ context.Context, id uuid.UUID) (store.DesignSession, error) {
	ds, ok := f.byID[id]
	if !ok {
		return store.DesignSession{}, fmt.Errorf("%w: design_session id %s", store.ErrNotFound, id)
	}
	return ds, nil
}

func (f writeSurfaceSessions) ListByProduct(_ context.Context, productID uuid.UUID) ([]store.DesignSession, error) {
	return f.byProduct[productID], nil
}

// SummarizeByProduct answers the product-wide aggregate the rejected-open
// re-render reads the session list from, derived from byProduct the same
// way the real read is derived from design_session. It is a READ, and the
// browser write path may read: refusing it here would make the re-render's
// own list unreadable and the refusal path untestable. What this surface
// must never do is WRITE a session or a revision round, which is what the
// two methods above refuse.
func (f writeSurfaceSessions) SummarizeByProduct(_ context.Context, productID uuid.UUID) (store.ProductDesignSessionsSummary, error) {
	if f.summarizeErr != nil {
		return store.ProductDesignSessionsSummary{}, f.summarizeErr
	}
	rows := f.byProduct[productID]
	sessions := make([]store.DesignSessionSummary, 0, len(rows))
	for _, ds := range rows {
		sessions = append(sessions, store.DesignSessionSummary{DesignSession: ds, Stage: store.StageOpened})
	}
	return store.ProductDesignSessionsSummary{ProductID: productID, Sessions: sessions}, nil
}

// GetSummaryByID answers out of byID the way the follow-up round is
// re-derived after a write. Like SummarizeByProduct it is a READ, and the
// re-render genuinely needs one: it rebuilds the whole round -- log, rail,
// form -- to show the outcome in place.
func (f writeSurfaceSessions) GetSummaryByID(_ context.Context, id uuid.UUID) (store.DesignSessionSummary, error) {
	if f.summaryErr != nil {
		return store.DesignSessionSummary{}, f.summaryErr
	}
	ds, ok := f.byID[id]
	if !ok {
		return store.DesignSessionSummary{}, fmt.Errorf("%w: design_session id %s", store.ErrNotFound, id)
	}
	return store.DesignSessionSummary{DesignSession: ds, Stage: f.derivedStage()}, nil
}

// writeSurfaceEvents is the session's log and open-question set, mutable so
// a test can put them in the state they are in AFTER an accepted write --
// which is the only way the re-derived round can be asserted to have moved.
//
// Append refuses: this browser surface reaches krill only through the api,
// so a store Append would mean the UI wrote a row behind api's gate.
type writeSurfaceEvents struct {
	mu            sync.Mutex
	bySession     map[uuid.UUID][]store.RevisionEvent
	openQuestions map[uuid.UUID][]store.OpenQuestion
}

func (f *writeSurfaceEvents) Append(context.Context, store.NewRevisionEvent) (store.RevisionEvent, error) {
	return store.RevisionEvent{}, errors.New("the browser write path must not append a revision event through the store")
}

func (f *writeSurfaceEvents) ListBySession(_ context.Context, id uuid.UUID) ([]store.RevisionEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bySession[id], nil
}

func (f *writeSurfaceEvents) ListOpenQuestions(_ context.Context, id uuid.UUID) ([]store.OpenQuestion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openQuestions[id], nil
}

func (f *writeSurfaceEvents) ListLatestSignoffBySessionIDs(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]store.SignoffStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[uuid.UUID]store.SignoffStatus, len(ids))
	for _, id := range ids {
		for _, ev := range f.bySession[id] {
			if ev.EventType == store.EventTypeSignoff && ev.SignoffStatus != nil {
				out[id] = *ev.SignoffStatus
			}
		}
	}
	return out, nil
}

// applyAnswer puts the read side in the state one accepted answer round
// leaves behind: the event joins the log, and the questions the round
// resolved leave the open set.
//
// Called from the fake api's responder, on the api's goroutine, while the
// handler is mid-write on the mux's -- hence the mutex on every read.
func (f *writeSurfaceEvents) applyAnswer(id uuid.UUID, ev store.RevisionEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bySession[id] = append(f.bySession[id], ev)
	if len(ev.OpenQuestionsDelta.Resolved) == 0 {
		return
	}
	resolved := make(map[string]bool, len(ev.OpenQuestionsDelta.Resolved))
	for _, qid := range ev.OpenQuestionsDelta.Resolved {
		resolved[qid] = true
	}
	kept := make([]store.OpenQuestion, 0, len(f.openQuestions[id]))
	for _, q := range f.openQuestions[id] {
		if !resolved[q.QuestionID] {
			kept = append(kept, q)
		}
	}
	f.openQuestions[id] = kept
}

// resolveOne removes one question from the open set without appending an
// event, standing in for another operator answering it between this page
// load and this write -- the stale-tick case.
func (f *writeSurfaceEvents) resolveOne(id uuid.UUID, questionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := make([]store.OpenQuestion, 0, len(f.openQuestions[id]))
	for _, q := range f.openQuestions[id] {
		if q.QuestionID != questionID {
			kept = append(kept, q)
		}
	}
	f.openQuestions[id] = kept
}

// ---------------------------------------------------------------------------
// the environment one test drives
// ---------------------------------------------------------------------------

// designWriteEnv is one signed-in operator, one recording fake api, and the
// two form routes mounted exactly as mountShellRoutes mounts them. Sub is a
// uuid minted for this test alone, so the identity assertions cannot be
// satisfied by a hardcoded subject.
type designWriteEnv struct {
	Mux *http.ServeMux
	App *App
	API *fakeAPI
	// Sessions and Events are the read side, kept on the env so a test can
	// put them in the state a WRITE leaves behind -- the re-derived round
	// is only worth asserting if it moved.
	Sessions writeSurfaceSessions
	Events   *writeSurfaceEvents
	// Cookie is the signed-in operator's session.
	Cookie    *http.Cookie
	Iss       string
	Sub       string
	SessionID uuid.UUID
	ProductID uuid.UUID
}

func newDesignWriteEnv(t *testing.T) *designWriteEnv {
	t.Helper()

	sub := uuid.NewString()
	idp := newFakeIDP(t, sub)
	authenticator, cookie := newSignedInOperator(t, idp)
	api := newFakeAPI(t)

	sessionID, productID := uuid.New(), uuid.New()
	ds := store.DesignSession{
		ID:                sessionID,
		ProductID:         productID,
		OpeningSubmission: "operators need a rollback story",
		CreatedAt:         time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
	}
	app := newTestApp(t, authenticator, idp.server.URL, api.server.URL)
	// The scope listing carries the product under test as well as the
	// deployment's own, so a re-rendered blade can be asked which product
	// it is opening a session under.
	app.spec = scopedProductsReader{products: []store.Product{
		{ID: testScopeID, Name: "krill"},
		{ID: productID, Name: "krill"},
	}}
	sessions := writeSurfaceSessions{
		byID:      map[uuid.UUID]store.DesignSession{sessionID: ds},
		byProduct: map[uuid.UUID][]store.DesignSession{productID: {ds}},
	}
	events := &writeSurfaceEvents{
		bySession: map[uuid.UUID][]store.RevisionEvent{},
		openQuestions: map[uuid.UUID][]store.OpenQuestion{sessionID: {
			{QuestionID: testClosedQuestion, Text: testClosedQuestionText, Blocking: true, OpenedAtSeqNo: 1},
			{QuestionID: testKeptQuestion, Text: testKeptQuestionText, Blocking: false, OpenedAtSeqNo: 1},
		}},
	}
	app.designSessions = sessions
	app.revisionEvents = events

	mux := http.NewServeMux()
	mux.HandleFunc("POST /design/products/{productID}/design-sessions", app.operatorRoute(app.handleOpenDesignSessionForm))
	mux.HandleFunc("POST /design/products/{productID}/design-sessions/{id}/answers", app.operatorRoute(app.handleDesignSessionAnswerForm))
	// The GET alongside them: a signed-off session's missing form is a claim
	// about a rendered PAGE, so it has to be read off the served one.
	mux.HandleFunc("GET /design/products/{productID}/design-sessions/{id}", app.operatorRoute(app.handleDesignSessionDetail))

	return &designWriteEnv{
		Mux: mux, App: app, API: api, Cookie: cookie,
		Sessions: sessions, Events: events,
		Iss: idp.server.URL, Sub: sub,
		SessionID: sessionID, ProductID: productID,
	}
}

// submitForm posts a real browser form -- urlencoded, as an HTML <form>
// sends -- through mux with the operator's session cookie attached.
func submitForm(mux *http.ServeMux, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return postForm(mux, target, form, false, cookies...)
}

// submitFormHX is the same submit as an htmx form post: the identical
// urlencoded body plus the HX-Request header htmx sets on every request it
// issues. Both write forms are doubled (method+action and hx-post), so
// this exercises the half a no-JS browser never reaches.
func submitFormHX(mux *http.ServeMux, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return postForm(mux, target, form, true, cookies...)
}

func postForm(mux *http.ServeMux, target string, form url.Values, hx bool, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func (e *designWriteEnv) openPath() string {
	return "/design/products/" + e.ProductID.String() + "/design-sessions"
}

func (e *designWriteEnv) answerPath() string {
	return "/design/products/" + e.ProductID.String() + "/design-sessions/" + e.SessionID.String() + "/answers"
}

func (e *designWriteEnv) submitOpen(form url.Values) *httptest.ResponseRecorder {
	return submitForm(e.Mux, e.openPath(), form, e.Cookie)
}

func (e *designWriteEnv) submitAnswer(form url.Values) *httptest.ResponseRecorder {
	return submitForm(e.Mux, e.answerPath(), form, e.Cookie)
}

func (e *designWriteEnv) submitOpenHX(form url.Values) *httptest.ResponseRecorder {
	return submitFormHX(e.Mux, e.openPath(), form, e.Cookie)
}

func (e *designWriteEnv) submitAnswerHX(form url.Values) *httptest.ResponseRecorder {
	return submitFormHX(e.Mux, e.answerPath(), form, e.Cookie)
}

// ---------------------------------------------------------------------------
// body assertions
// ---------------------------------------------------------------------------

// wireAnswerRound is the appendRevisionEvent body as it arrives at api,
// declared here rather than reusing the production request struct so a
// change to the UI's own type cannot quietly reshape the expectation.
type wireAnswerRound struct {
	EventType          string            `json:"event_type"`
	EntityDeltas       []wireEntityDelta `json:"entity_deltas"`
	OpenQuestionsDelta struct {
		Opened   []wireOpenedQuestion `json:"opened"`
		Resolved []string             `json:"resolved"`
	} `json:"open_questions_delta"`
	VerifiedAgainst *string `json:"verified_against"`
	SignoffStatus   *string `json:"signoff_status"`
}

type wireEntityDelta struct {
	EntityID    string `json:"entity_id"`
	Change      string `json:"change"`
	SummaryLine string `json:"summary_line"`
}

type wireOpenedQuestion struct {
	QuestionID string `json:"question_id"`
	Blocking   bool   `json:"blocking"`
	Text       string `json:"text"`
}

func decodeAnswerRound(t *testing.T, raw []byte) wireAnswerRound {
	t.Helper()
	var parsed wireAnswerRound
	require.NoError(t, json.Unmarshal(raw, &parsed), "answer body: %s", raw)
	return parsed
}

// forbiddenWriteKeys are field names no browser write may carry at any
// depth: a spec-entity reference, a proposal payload, or an identity /
// scope / session field -- those come only from the gating krill session,
// never from the request.
var forbiddenWriteKeys = []string{
	"acting", "on_behalf_of", "iss", "sub", "kind",
	"scope_id", "session_id", "krill_session_id", "x_krill_session_id",
	"entity_id", "entity_ids", "summary_line", "proposals", "proposed_entities",
}

// assertNoForbiddenKeys walks a decoded write body and fails on any key that
// names a spec entity, a proposal, or an identity. The walk is recursive so
// a nested addition is caught, not just a new top-level field.
func assertNoForbiddenKeys(t *testing.T, where string, raw []byte) {
	t.Helper()

	var decoded any
	require.NoError(t, json.Unmarshal(raw, &decoded), "%s body: %s", where, raw)
	forbidden := make(map[string]bool, len(forbiddenWriteKeys))
	for _, k := range forbiddenWriteKeys {
		forbidden[k] = true
	}
	var found []string
	var walk func(v any, path string)
	walk = func(v any, path string) {
		switch typed := v.(type) {
		case map[string]any:
			for k, child := range typed {
				if forbidden[k] {
					found = append(found, path+"."+k)
				}
				walk(child, path+"."+k)
			}
		case []any:
			for i, child := range typed {
				walk(child, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walk(decoded, where)
	assert.Empty(t, found, "%s must carry no entity reference, proposal payload, or identity field", where)
}

// assertJSONKeysExactly pins a write body's whole key set: a field added on
// either side of the wire fails here even when its value is benign.
func assertJSONKeysExactly(t *testing.T, where string, raw []byte, keys ...string) {
	t.Helper()

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "%s body: %s", where, raw)
	got := make([]string, 0, len(fields))
	for k := range fields {
		got = append(got, k)
	}
	sort.Strings(got)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	assert.Equal(t, want, got, "%s must carry exactly these fields", where)
}

// assertEntityDeltasAlwaysEmpty is FR 1ff1c1e9's central prohibition as one
// named assertion: entity_deltas must be present, and an empty array -- not
// a populated one, not null, not absent. A browser that proposed or amended
// a Feature/Requirement would fail exactly here.
func assertEntityDeltasAlwaysEmpty(t *testing.T, where string, raw []byte) {
	t.Helper()

	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields), "%s body: %s", where, raw)
	value, present := fields["entity_deltas"]
	require.True(t, present, "%s must send entity_deltas explicitly, as an empty array", where)
	assert.JSONEq(t, `[]`, string(value),
		"%s: the UI must never propose or amend a Feature/Requirement entity", where)
	var deltas []wireEntityDelta
	require.NoError(t, json.Unmarshal(value, &deltas), "%s entity_deltas: %s", where, value)
	assert.Empty(t, deltas, "%s: entity_deltas must stay empty", where)
}

// assertInShell fails unless body is a full shell page (not a bare status
// page) -- a rejected write must land the operator back in the UI. The
// landmarks are matched on the tag prefix because the shared nav/main
// elements carry attributes (aria-label, class) now that they are templ
// components rather than hand-written tags.
func assertInShell(t *testing.T, body string) {
	t.Helper()
	assert.Contains(t, body, "<!DOCTYPE html>", "the re-render must be in-shell, not a bare status page")
	assert.Contains(t, body, "<nav")
	assert.Contains(t, body, "<main")
}

// ---------------------------------------------------------------------------
// FR 84c18cd6 -- open a session from a plain-language submission
// ---------------------------------------------------------------------------

// TestDesignWrite_OpenSession_RoundTrip is FR 84c18cd6's round trip: the
// operator types a plain-language idea into the product's form, the UI opens
// a session through api's POST /design-sessions carrying exactly the two
// fields the handler accepts, and the browser is redirected (POST/Redirect/
// Get) to the new session's own detail page. The write rides a krill session
// minted under this test's freshly signed-in operator as both acting and
// on-behalf-of.
func TestDesignWrite_OpenSession_RoundTrip(t *testing.T) {
	env := newDesignWriteEnv(t)
	const submission = "Operators need to bulk-export their incident list as CSV."

	rec := env.submitOpen(url.Values{"opening_submission": {submission}})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, designSessionPath(env.ProductID, uuid.MustParse(env.API.createdSessionID)), rec.Header().Get("Location"),
		"the redirect must land on the new session's detail page")

	// Attribution: minted under the operator who just signed in, as both
	// subjects, and presented on the write itself.
	assertFreshOperatorAttribution(t, env.API, env.Iss, env.Sub)
	write := env.API.writeRequest(t)
	assert.Equal(t, http.MethodPost, write.Method)
	assert.Equal(t, "/design-sessions", write.Path)
	assert.Equal(t, env.API.sessionID, write.Header.Get(sessionHeader),
		"the write must carry the krill session init minted under the operator's identity")

	// The body, asserted against literals: exactly product_id and
	// opening_submission, no identity, no entity reference (FR8).
	want := fmt.Sprintf(`{"product_id":%q,"opening_submission":%q}`, env.ProductID.String(), submission)
	assert.JSONEq(t, want, string(write.Body))
	assertJSONKeysExactly(t, "open-session body", write.Body, "product_id", "opening_submission")
	assertNoForbiddenKeys(t, "open-session body", write.Body)
}

// ---------------------------------------------------------------------------
// FR 1ff1c1e9 -- a follow-up answer is part of the session's record
// ---------------------------------------------------------------------------

// TestDesignWrite_Answer_RoundTrip is FR 1ff1c1e9's round trip: the
// operator answers an open question and adds context, and the UI appends one
// `answer` revision round through api carrying the follow-up text and the
// ticked resolve ids -- with entity_deltas empty, so the answer becomes part
// of the session's durable record without proposing or amending any spec
// entity.
func TestDesignWrite_Answer_RoundTrip(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if req.Path == "/design-sessions/"+env.SessionID.String()+"/revision-events" {
			return http.StatusCreated, `{"id":"` + uuid.NewString() + `","seq_no":4}`
		}
		return 0, ""
	})
	const followUp = "It should read the postgres flag table, not the env file."

	rec := env.submitAnswer(url.Values{
		"follow_up": {followUp},
		"resolve":   {testClosedQuestion},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, designSessionPath(env.ProductID, env.SessionID), rec.Header().Get("Location"),
		"the redirect must land back on the session's detail page")

	assertFreshOperatorAttribution(t, env.API, env.Iss, env.Sub)
	write := env.API.writeRequest(t)
	assert.Equal(t, http.MethodPost, write.Method)
	assert.Equal(t, "/design-sessions/"+env.SessionID.String()+"/revision-events", write.Path)
	assert.Equal(t, env.API.sessionID, write.Header.Get(sessionHeader))

	// The full delta the answer round carries.
	round := decodeAnswerRound(t, write.Body)
	assert.Equal(t, "answer", round.EventType, "an answer round is event_type answer")
	require.Len(t, round.OpenQuestionsDelta.Opened, 1)
	opened := round.OpenQuestionsDelta.Opened[0]
	assert.Equal(t, followUp, opened.Text, "the contributor's follow-up text is the round's durable record")
	assert.False(t, opened.Blocking, "a contributor's own follow-up is not a blocking question")
	assert.NotEmpty(t, opened.QuestionID, "the opened question must carry a minted id")
	assert.Equal(t, []string{testClosedQuestion}, round.OpenQuestionsDelta.Resolved,
		"only the ticked resolve box is resolved")
	assert.Nil(t, round.VerifiedAgainst, "an answer round carries no verified_against")
	assert.Nil(t, round.SignoffStatus, "an answer round carries no signoff_status")

	// FR8 / FR 1ff1c1e9: the body's field set, and the empty entity_deltas.
	assertJSONKeysExactly(t, "answer body", write.Body,
		"event_type", "entity_deltas", "open_questions_delta", "verified_against", "signoff_status")
	assertEntityDeltasAlwaysEmpty(t, "answer body", write.Body)
	assertNoForbiddenKeys(t, "answer body", write.Body)
}

// TestDesignWrite_Answer_EntityDeltasAlwaysEmpty pins the empty
// entity_deltas invariant across every shape of answer a contributor can
// submit -- text only, a ticked box only, and both -- so no input the form
// accepts can make the UI write a spec entity.
func TestDesignWrite_Answer_EntityDeltasAlwaysEmpty(t *testing.T) {
	cases := []struct {
		name string
		form url.Values
	}{
		{"follow-up text only", url.Values{"follow_up": {"the flag lives in postgres"}}},
		{"ticked resolve box only", url.Values{"resolve": {testClosedQuestion}}},
		{"text and a ticked box", url.Values{"follow_up": {"use the flag table"}, "resolve": {testClosedQuestion, testKeptQuestion}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			rec := env.submitAnswer(c.form)
			require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

			write := env.API.writeRequest(t)
			round := decodeAnswerRound(t, write.Body)
			assert.Equal(t, "answer", round.EventType)
			assertEntityDeltasAlwaysEmpty(t, "answer body ("+c.name+")", write.Body)
		})
	}
}

// TestDesignWrite_ResolveOnlyAnswer_SendsNoOpenedQuestion covers the
// resolve-only round: an operator who only closes a question sends that
// resolve id and nothing else -- no opened question standing in for absent
// text, and no entity delta either.
func TestDesignWrite_ResolveOnlyAnswer_SendsNoOpenedQuestion(t *testing.T) {
	env := newDesignWriteEnv(t)

	rec := env.submitAnswer(url.Values{"follow_up": {"   "}, "resolve": {testClosedQuestion}})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	write := env.API.writeRequest(t)
	round := decodeAnswerRound(t, write.Body)
	assert.Equal(t, "answer", round.EventType)
	assert.Empty(t, round.OpenQuestionsDelta.Opened,
		"a resolve-only round must not open a question")
	assert.Equal(t, []string{testClosedQuestion}, round.OpenQuestionsDelta.Resolved)
	assertJSONKeysExactly(t, "resolve-only answer body", write.Body,
		"event_type", "entity_deltas", "open_questions_delta", "verified_against", "signoff_status")
	assertEntityDeltasAlwaysEmpty(t, "resolve-only answer body", write.Body)
}

// TestDesignWrite_AnswerIsNeverAProposal is FR 1ff1c1e9's boundary as a
// named check across every write this surface can make: none of them targets
// a mediated-proposal endpoint, and none carries a proposal-shaped payload.
// Turning a submission into a Feature/Requirement stays the mediated
// propose_entities path an Agent drives, never a browser write.
func TestDesignWrite_AnswerIsNeverAProposal(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		env := newDesignWriteEnv(t)
		require.Equal(t, http.StatusSeeOther, env.submitOpen(url.Values{
			"opening_submission": {"operators want an export button"},
		}).Code)

		write := env.API.writeRequest(t)
		assertNotAProposal(t, write)
	})
	for _, c := range []struct {
		name string
		form url.Values
	}{
		{"answer with text", url.Values{"follow_up": {"here is the missing detail"}}},
		{"resolve-only answer", url.Values{"resolve": {testKeptQuestion}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			require.Equal(t, http.StatusSeeOther, env.submitAnswer(c.form).Code)

			write := env.API.writeRequest(t)
			assertNotAProposal(t, write)
			assertEntityDeltasAlwaysEmpty(t, c.name, write.Body)
		})
	}
}

// assertNotAProposal fails if a write targets a proposal endpoint or carries
// any entity/proposal field at any depth.
func assertNotAProposal(t *testing.T, write recordedRequest) {
	t.Helper()
	assert.NotContains(t, strings.ToLower(write.Path), "propos",
		"a browser write must not target a mediated-proposal endpoint")
	assertNoForbiddenKeys(t, "write to "+write.Path, write.Body)
}

// ---------------------------------------------------------------------------
// rejection: re-render in-shell, keep what the operator typed
// ---------------------------------------------------------------------------

// TestDesignWrite_RejectedOpen_RerendersFormInShell covers a rejected
// "open a session" write: the operator gets the page back inside the shell
// with api's own message as readable inline text -- not a bare status page,
// and not raw JSON -- and their opening text still in the textarea.
func TestDesignWrite_RejectedOpen_RerendersFormInShell(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if req.Path == "/design-sessions" {
			return http.StatusUnprocessableEntity, `{"error":"product is in another scope"}`
		}
		return 0, ""
	})
	const submission = "Operators need to bulk-export their incident list as CSV."

	rec := env.submitOpen(url.Values{"opening_submission": {submission}})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, "a rejected write re-renders the page, not a status code: %s", body)
	assertInShell(t, body)

	assert.Contains(t, body, `class="alert alert-error"`, "the message must render as an inline alert")
	assert.Contains(t, body, "422: product is in another scope", "api's status and named message are shown")
	assert.NotContains(t, body, `{"error":`, "the raw JSON rejection must not leak into the page")
	assert.NotContains(t, body, `"error":`)

	// The operator's text survives the rejection.
	assert.Contains(t, body, ">"+submission+"</textarea>", "the submitted text must be preserved in the form")
	assert.Contains(t, body, `action="`+env.openPath()+`"`, "the form still posts to its own action")
}

// TestDesignWrite_RejectedAnswer_RerendersFormInShell is the answer form's
// half: the detail page comes back in-shell with the message inline and
// exactly the boxes the operator ticked still ticked.
func TestDesignWrite_RejectedAnswer_RerendersFormInShell(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if strings.HasSuffix(req.Path, "/revision-events") {
			return http.StatusConflict, `{"error":"question q-flag-store was never opened"}`
		}
		return 0, ""
	})
	const followUp = "It should read the postgres flag table, not the env file."

	rec := env.submitAnswer(url.Values{
		"follow_up": {followUp},
		"resolve":   {testClosedQuestion},
	})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, "a rejected write re-renders the page, not a status code: %s", body)
	assertInShell(t, body)

	assert.Contains(t, body, `class="alert alert-error"`)
	assert.Contains(t, body, "409: question q-flag-store was never opened")
	assert.NotContains(t, body, `{"error":`, "the raw JSON rejection must not leak into the page")
	assert.NotContains(t, body, `"error":`)

	// Text and ticks survive; only the ticked box comes back checked. The
	// boxes live in the rail and are checked on the input's own tag rather
	// than by string-matching an attribute order that is not a contract.
	assert.Contains(t, body, ">"+followUp+"</textarea>", "the submitted follow-up must be preserved")
	assert.True(t, resolveBoxChecked(t, body, testClosedQuestion),
		"the ticked resolve box must stay ticked")
	assert.False(t, resolveBoxChecked(t, body, testKeptQuestion),
		"an unticked box must not come back ticked")
}

// resolveBoxChecked reports whether the resolve box for questionID is
// rendered with the checked attribute. It parses the input carrying that
// question's own value rather than looking for `value="X" checked`, because
// the box now also carries the form it belongs to and attribute
// serialisation order is not a contract (krill/ui/README.md, Testing
// conventions).
func resolveBoxChecked(t *testing.T, body, questionID string) bool {
	t.Helper()
	re := regexp.MustCompile(`<input[^>]*value="` + regexp.QuoteMeta(questionID) + `"[^>]*>`)
	input := re.FindString(body)
	require.NotEmpty(t, input, "no resolve box for %q in the rendered page", questionID)
	return strings.Contains(input, " checked")
}

// formAttr is one attribute of the follow-up form, read off the SERVED
// element rather than off the template source, so a template that stops
// emitting it fails here. The form is found by its data-krill hook, which
// is what makes this a lookup rather than a scan for a substring that might
// also appear inside the operator's own typed text.
func formAttr(t *testing.T, body, attr string) string {
	t.Helper()
	re := regexp.MustCompile(`<form[^>]*data-krill="design-session-follow-up-form"[^>]*>`)
	tag := re.FindString(body)
	require.NotEmpty(t, tag, "the served page must carry the follow-up form")
	m := regexp.MustCompile(regexp.QuoteMeta(attr) + `="([^"]*)"`).FindStringSubmatch(tag)
	require.NotNil(t, m, "the follow-up form must carry %s", attr)
	return m[1]
}

// mustTakeFlash decodes the message a flash cookie carries, failing rather
// than returning "" so a missing or unreadable confirmation is a test
// failure rather than a silent pass.
func mustTakeFlash(t *testing.T, c *http.Cookie) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(c.Value)
	require.NoError(t, err, "the flash cookie must carry a readable message")
	return string(decoded)
}

// getDetail renders one session's detail page through a mounted GET route,
// so the tests below assert against SERVED markup rather than against a
// component rendered directly -- a page a route does not actually serve is
// not a page an operator ever sees.
func getDetail(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	mux.ServeHTTP(rec, req)
	return rec
}

// getDetailOf is getDetail through this env's operator session, which the
// detail route's auth gate requires.
func (e *designWriteEnv) getDetail(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(e.Cookie)
	e.Mux.ServeHTTP(rec, req)
	return rec
}

// betweenTags slices body from the marker to the next occurrence of end,
// so an assertion about what one element carries stays off its neighbours.
func betweenTags(t *testing.T, body, marker, end string) string {
	t.Helper()
	i := strings.Index(body, marker)
	require.NotEqual(t, -1, i, "body must contain %q", marker)
	rest := body[i:]
	j := strings.Index(rest, end)
	require.NotEqual(t, -1, j, "body must contain %q after %q", end, marker)
	return rest[:j+len(end)]
}

// The session detail's stable region ids, spelled as the markup the served
// page contains so a renamed region fails here rather than silently. Slicing
// on these rather than on a heading's class list is what lets a card be
// restyled without breaking a section-scoped assertion (htmxui ARCHITECTURE
// §14).
const (
	regionRevisionLog  = `id="revision-events"`
	regionOpenQuestion = `id="open-questions"`
	regionSessionProps = `id="session-properties"`
)

// pageSectionOf slices body between two region ids, so parsing one section
// never picks up a neighbouring section's markup.
func pageSectionOf(t *testing.T, body, from, to string) string {
	t.Helper()
	i := strings.LastIndex(body, from)
	require.NotEqual(t, -1, i, "body must contain section start %q", from)
	rest := body[i+len(from):]
	j := strings.LastIndex(rest, to)
	require.NotEqual(t, -1, j, "body must contain section end %q", to)
	return rest[:j]
}

// ---------------------------------------------------------------------------
// validation: nothing empty ever reaches krill
// ---------------------------------------------------------------------------

// TestDesignWrite_RejectsEmptySubmissionBeforeApi covers the client-side
// rules the forms enforce: a whitespace-only opening submission, and an
// answer with no text and nothing ticked, are both refused in-shell with
// the operator's own words, and the fake api records nothing at all -- not
// even a session init.
func TestDesignWrite_RejectsEmptySubmissionBeforeApi(t *testing.T) {
	blank := []string{"", "   ", "\t\n  "}

	for _, opening := range blank {
		t.Run("empty opening_submission "+fmt.Sprintf("%q", opening), func(t *testing.T) {
			env := newDesignWriteEnv(t)
			rec := env.submitOpen(url.Values{"opening_submission": {opening}})
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code, "validation must re-render in-shell, not answer a bare 400: %s", body)
			assertInShell(t, body)
			assert.Contains(t, body, `class="alert alert-error"`)
			assert.Contains(t, body, "Describe your idea in plain language before opening the session.")
			assert.Empty(t, env.API.recorded(), "an empty submission must never reach krill")
		})
	}

	for _, followUp := range blank {
		t.Run("empty answer "+fmt.Sprintf("%q", followUp), func(t *testing.T) {
			env := newDesignWriteEnv(t)
			rec := env.submitAnswer(url.Values{"follow_up": {followUp}})
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code, "validation must re-render in-shell, not answer a bare 400: %s", body)
			assertInShell(t, body)
			assert.Contains(t, body, `class="alert alert-error"`)
			assert.Contains(t, body, "Write a follow-up, or tick an open question your answer closes.")
			assert.Empty(t, env.API.recorded(), "an empty answer must never reach krill")
		})
	}
}

// ---------------------------------------------------------------------------
// NFR 6f872832 -- identity comes from the signed-in session, nothing else
// ---------------------------------------------------------------------------

// TestDesignWrite_IdentityComesOnlyFromTheSignedInSession is the NFR's
// negative case for this surface: identity-shaped fields planted in both
// form fields and the query string are ignored entirely. The krill session
// is still minted under the operator who actually signed in, and the write
// body still carries none of them.
func TestDesignWrite_IdentityComesOnlyFromTheSignedInSession(t *testing.T) {
	spoof := url.Values{
		"acting":             {`{"iss":"https://evil.example","sub":"attacker","kind":"service"}`},
		"on_behalf_of":       {`{"iss":"https://evil.example","sub":"attacker","kind":"service"}`},
		"iss":                {"https://evil.example"},
		"sub":                {"attacker"},
		"scope_id":           {uuid.NewString()},
		"session_id":         {uuid.NewString()},
		"entity_id":          {uuid.NewString()},
		"proposals":          {`[{"entity_id":"` + uuid.NewString() + `"}]`},
		"opening_submission": {"operators want an export button"},
		"follow_up":          {"here is the missing detail"},
		"resolve":            {testClosedQuestion},
	}
	query := "?acting=" + url.QueryEscape(`{"sub":"attacker"}`) +
		"&iss=https%3A%2F%2Fevil.example&sub=attacker" +
		"&scope_id=" + uuid.NewString() + "&session_id=" + uuid.NewString()

	t.Run("open", func(t *testing.T) {
		env := newDesignWriteEnv(t)
		rec := submitForm(env.Mux, env.openPath()+query, spoof, env.Cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

		assertFreshOperatorAttribution(t, env.API, env.Iss, env.Sub)
		write := env.API.writeRequest(t)
		assertJSONKeysExactly(t, "open-session body", write.Body, "product_id", "opening_submission")
		assertNoForbiddenKeys(t, "open-session body", write.Body)
	})

	t.Run("answer", func(t *testing.T) {
		env := newDesignWriteEnv(t)
		rec := submitForm(env.Mux, env.answerPath()+query, spoof, env.Cookie)
		require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

		assertFreshOperatorAttribution(t, env.API, env.Iss, env.Sub)
		write := env.API.writeRequest(t)
		assertJSONKeysExactly(t, "answer body", write.Body,
			"event_type", "entity_deltas", "open_questions_delta", "verified_against", "signoff_status")
		assertNoForbiddenKeys(t, "answer body", write.Body)
	})
}

// ---------------------------------------------------------------------------
// the doubled form: the htmx half of each write
//
// Both write forms carry method+action AND hx-post+hx-target+hx-swap, so
// one route serves a no-JS browser (303 + Location) and an htmx one
// (200 + HX-Redirect, or 200 with the form fragment carrying the error
// inline). The no-HX half is pinned by every test above; these cover the
// other half.
// ---------------------------------------------------------------------------

// TestDesignWrite_OpenSession_HXSuccessRedirects is the open form's htmx
// success: a 200 with HX-Redirect to the new session's detail page. The
// write itself is byte-identical to the no-HX round trip -- one route, one
// handler, one body.
func TestDesignWrite_OpenSession_HXSuccessRedirects(t *testing.T) {
	env := newDesignWriteEnv(t)
	const submission = "Operators need to bulk-export their incident list as CSV."

	rec := env.submitOpenHX(url.Values{"opening_submission": {submission}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, designSessionPath(env.ProductID, uuid.MustParse(env.API.createdSessionID)), rec.Header().Get("HX-Redirect"),
		"the htmx success must navigate to the new session's detail page")
	assert.Empty(t, rec.Header().Get("Location"), "an htmx write redirects with HX-Redirect, not Location")

	write := env.API.writeRequest(t)
	assert.JSONEq(t, fmt.Sprintf(`{"product_id":%q,"opening_submission":%q}`, env.ProductID.String(), submission), string(write.Body))
	assertFreshOperatorAttribution(t, env.API, env.Iss, env.Sub)
}

// TestDesignWrite_RejectedOpen_HXReRendersFormInline is the open form's
// htmx refusal: 200 with the bare form fragment (no shell chrome), the
// api's status and named message inline, and the operator's typed text
// preserved. A swap target's HTTP status is not surfaced to the operator,
// so the error has to ride inside the fragment.
func TestDesignWrite_RejectedOpen_HXReRendersFormInline(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if req.Path == "/design-sessions" {
			return http.StatusUnprocessableEntity, `{"error":"product is in another scope"}`
		}
		return 0, ""
	})
	const submission = "Operators need to bulk-export their incident list as CSV."

	rec := env.submitOpenHX(url.Values{"opening_submission": {submission}})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, "a refusal is 200, never the rejection's status: %s", body)
	assert.Empty(t, rec.Header().Get("HX-Redirect"), "a refusal must not navigate away")
	assert.NotContains(t, body, "<main", "the htmx half answers the fragment alone, with no shell chrome")
	assert.Contains(t, body, `class="alert alert-error"`, "the error rides inline in the swapped fragment")
	assert.Contains(t, body, "422: product is in another scope")
	assert.NotContains(t, body, `"error":`, "the raw JSON rejection must not leak into the fragment")

	// The operator's text and the form's own wiring both survive.
	assert.Contains(t, body, ">"+submission+"</textarea>", "the submitted text must be preserved")
	assert.Contains(t, body, `action="`+env.openPath()+`"`, "the form still posts to its own action")
	assert.Contains(t, body, `hx-post="`+env.openPath()+`"`, "the doubled form keeps its htmx wiring across a re-render")
}

// TestDesignWrite_RejectedAnswer_HXReRendersTheRoundRegion is the answer
// form's htmx refusal, and the gap task f12d1042 recorded as its own: the
// refusal used to swap the FORM alone, and the ticked resolve boxes live in
// the rail, so an htmx refusal dropped the operator's own ticks while the
// no-JS refusal preserved them. Both halves now answer the same round
// region, so a tick survives either way.
//
// What comes back is 200, the error inline, the typed text in the textarea,
// the ticked box still ticked in the rail, and the region's own id --
// because that id is the form's hx-target, and a fragment without it makes
// htmx delete the element it was meant to replace.
func TestDesignWrite_RejectedAnswer_HXReRendersTheRoundRegion(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if strings.HasSuffix(req.Path, "/revision-events") {
			return http.StatusConflict, `{"error":"question q-flag-store was never opened"}`
		}
		return 0, ""
	})
	const followUp = "It should read the postgres flag table, not the env file."

	rec := env.submitAnswerHX(url.Values{
		"follow_up": {followUp},
		"resolve":   {testClosedQuestion},
	})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, "a refusal is 200, never the rejection's status: %s", body)
	assert.Empty(t, rec.Header().Get("HX-Redirect"), "a refusal must not navigate away")
	assert.NotContains(t, body, "<main", "the htmx half answers the fragment alone, with no shell chrome")
	assert.Contains(t, body, `class="alert alert-error"`)
	assert.Contains(t, body, "409: question q-flag-store was never opened")
	assert.NotContains(t, body, `"error":`)

	// The whole round comes back, so the box keeps its tick. This is the
	// claim the previous test could not make.
	assert.Contains(t, body, `id="`+pages.DesignSessionRoundAnchor+`"`,
		"the refusal answers the region the form's hx-target names")
	assert.True(t, resolveBoxChecked(t, body, testClosedQuestion),
		"FR 1942d934: the ticked question must still be ticked after an htmx refusal")
	assert.False(t, resolveBoxChecked(t, body, testKeptQuestion),
		"an unticked box must not come back ticked")

	// The text and the form's wiring survive, and the boxes still appear
	// exactly once across the fragment -- in the rail, not inside the form.
	assert.Contains(t, body, ">"+followUp+"</textarea>", "the submitted follow-up must be preserved")
	assert.Equal(t, 2, strings.Count(body, `data-krill="open-question-resolve"`),
		"one box per open question, and the rail still owns them")
	assert.Contains(t, body, `hx-post="`+env.answerPath()+`"`, "the doubled form keeps its htmx wiring across a re-render")
	assert.Equal(t, "#"+pages.DesignSessionRoundAnchor, formAttr(t, body, "hx-target"),
		"the re-rendered form still points its swap at the round region, not at itself")
}

// TestDesignWrite_HXRejectsEmptySubmissionBeforeApi is the htmx half of
// the empty-submission rule: the server refusal is answered inline in the
// swapped fragment, and nothing at all reaches krill.
func TestDesignWrite_HXRejectsEmptySubmissionBeforeApi(t *testing.T) {
	env := newDesignWriteEnv(t)
	rec := env.submitOpenHX(url.Values{"opening_submission": {"   "}})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, "validation is 200 inline, not a bare 400: %s", body)
	assert.Contains(t, body, `class="alert alert-error"`)
	assert.Contains(t, body, "Describe your idea in plain language before opening the session.")
	assert.Empty(t, env.API.recorded(), "an empty submission must never reach krill")
}

// TestDesignWrite_Answer_HXSuccessSwapsTheRoundInPlace is FR d81d2283's
// htmx half: a submitted follow-up answers 200 and swaps the ONE region a
// round changes -- the timeline gains the event, the rail loses the
// questions the round closed, the textarea empties -- with one
// out-of-band toast and no navigation at all.
//
// The HX-Redirect this replaces could not express any of that: a redirect
// is a full page load, so the operator's whole viewport was thrown away to
// say what a partial update says better.
func TestDesignWrite_Answer_HXSuccessSwapsTheRoundInPlace(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if strings.HasSuffix(req.Path, "/revision-events") {
			// The write landed; the read side sees it too, so the
			// re-derived region is the state AFTER the answer rather than
			// the state before it.
			env.Events.applyAnswer(env.SessionID, store.RevisionEvent{
				ID:                 uuid.New(),
				SeqNo:              4,
				EventType:          store.EventTypeAnswer,
				OpenQuestionsDelta: store.OpenQuestionsDelta{Resolved: []string{testClosedQuestion}},
			})
			return http.StatusCreated, `{"id":"` + uuid.NewString() + `","seq_no":4}`
		}
		return 0, ""
	})
	const followUp = "here is the missing detail"

	rec := env.submitAnswerHX(url.Values{
		"follow_up": {followUp},
		"resolve":   {testClosedQuestion},
	})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// No navigation, in either spelling: the page does not reload.
	assert.Empty(t, rec.Header().Get("HX-Redirect"),
		"an answer updates the page it is on; a redirect would throw the whole viewport away")
	assert.Empty(t, rec.Header().Get("Location"))
	assert.NotContains(t, body, "<main", "the htmx half answers the fragment alone, with no shell chrome")
	assert.NotContains(t, body, "<!DOCTYPE html>", "the page must not reload")

	// The fragment's ROOT is the swap target, or htmx deletes the region
	// it was meant to replace and the next answer never reaches the server
	// (htmxui ARCHITECTURE, the swap-target rule).
	root := strings.TrimLeft(body, " \t\r\n")
	assert.True(t, strings.HasPrefix(root, `<section id="`+pages.DesignSessionRoundAnchor+`"`) ||
		strings.HasPrefix(root, `<section id="`+pages.DesignSessionRoundAnchor+`" `),
		"the served fragment's ROOT must be the round region the form's hx-target names: %s", root)

	// Exactly one toast, and it is out-of-band into the shell's host.
	assert.Equal(t, 1, strings.Count(body, components.ToastHostID),
		"a success confirms with exactly one toast")
	assert.Contains(t, body, "hx-swap-oob", "the toast rides out-of-band into the shell's host, which this fragment is not")
	assert.Contains(t, body, `data-krill="toast"`)
	assert.Contains(t, body, answerSuccessToast)

	// The three things a round changed, asserted through region ids.
	log := pageSectionOf(t, body, regionRevisionLog, regionOpenQuestion)
	assert.Contains(t, log, `data-krill-seq-no="4"`, "the timeline gained the appended round")
	assert.Contains(t, log, `data-krill-event-type="answer"`)

	rail := pageSectionOf(t, body, regionOpenQuestion, regionSessionProps)
	assert.NotContains(t, rail, testClosedQuestion,
		"the question the round closed has left the rail")
	assert.Contains(t, rail, testKeptQuestion, "the question it did not close is still offered")

	assert.Contains(t, body, "<textarea id=\"follow-up\"", "the form is still there")
	assert.NotContains(t, body, ">"+followUp+"</textarea>",
		"the textarea is empty: the text is now the round's record, not a draft")

	// The write itself is unchanged by any of this: no entity reference, no
	// identity, no scope (FR 1ff1c1e9, LB4).
	write := env.API.writeRequest(t)
	assertEntityDeltasAlwaysEmpty(t, "htmx answer body", write.Body)
	assertNoForbiddenKeys(t, "htmx answer body", write.Body)
	assertJSONKeysExactly(t, "htmx answer body", write.Body,
		"event_type", "entity_deltas", "open_questions_delta", "verified_against", "signoff_status")
	assertFreshOperatorAttribution(t, env.API, env.Iss, env.Sub)
}

// TestDesignWrite_Answer_HXSuccessStaysOnThePage pins the one thing the
// swap has to NOT be: a navigation. The no-JS half still redirects, and the
// two halves are allowed to differ exactly there.
func TestDesignWrite_Answer_HXSuccessStaysOnThePage(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if strings.HasSuffix(req.Path, "/revision-events") {
			return http.StatusCreated, `{"id":"` + uuid.NewString() + `","seq_no":4}`
		}
		return 0, ""
	})

	rec := env.submitAnswerHX(url.Values{"follow_up": {"here is the missing detail"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Header().Get("HX-Redirect"))
	assert.Empty(t, rec.Header().Get("Location"))
}

// TestDesignWrite_Answer_NoJSSuccessStillRedirects is the other half of the
// same FR: with JavaScript unavailable the form is an ordinary POST, so it
// still answers 303 + Location back to the session's canonical detail URL
// and carries its confirmation on the one-shot flash cookie. That half is
// unchanged by the in-place work, and it has to stay unchanged -- it is
// what stops a refresh from replaying the write.
func TestDesignWrite_Answer_NoJSSuccessStillRedirects(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if strings.HasSuffix(req.Path, "/revision-events") {
			return http.StatusCreated, `{"id":"` + uuid.NewString() + `","seq_no":4}`
		}
		return 0, ""
	})

	rec := env.submitAnswer(url.Values{"follow_up": {"here is the missing detail"}})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, designSessionPath(env.ProductID, env.SessionID), rec.Header().Get("Location"),
		"the no-JS success navigates back to this session's canonical detail URL")

	// The confirmation rides the flash cookie, because a 303 has no body
	// to carry a message in.
	var flashed *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == toastCookieName {
			flashed = c
		}
	}
	require.NotNil(t, flashed, "the success must arm the one-shot flash cookie")
	assert.Contains(t, mustTakeFlash(t, flashed), answerSuccessToast)
}

// ---------------------------------------------------------------------------
// FR 4304fe60 -- the blade is what a refusal hands back
//
// Every case below is a refusal, and every one of them must answer 200
// with the new-session BLADE -- not a bare status page, and never the
// rejection's own status, which htmx does not swap on. What they share is
// the load the operator's work must not lose.
// ---------------------------------------------------------------------------

// bladeRegion is the region id the blade URL's fragment carries and the
// list's action swaps into. It is spelled as the raw marker the served
// HTML contains so a renamed region fails here rather than silently.
const bladeRegion = `id="design-session-new-blade"`

// assertBladeSays fails unless body carries the blade, the alert variant a
// refusal renders, the reason the operator is owed, and their own text
// still in the textarea. The last of those is the whole point of the rule:
// a refusal is not allowed to cost someone a paragraph.
func assertBladeSays(t *testing.T, body, reason, typed string) {
	t.Helper()
	assert.Contains(t, body, bladeRegion, "a refusal re-renders the blade in its own region")
	assert.Contains(t, body, `class="alert alert-error"`, "the reason rides inline through htmxui.Alert")
	assert.Contains(t, body, reason, "the reason the operator is owed is shown")
	assert.Contains(t, body, ">"+typed+"</textarea>",
		"the operator's typed text survives a refusal")
}

// TestDesignWrite_RejectedOpen_HXReRendersTheBladeRegion is the htmx half
// of FR 4304fe60: a refused open answers 200 with the bare blade region,
// whose ROOT is the swap target the form's hx-target names. Without that
// root id htmx's outerHTML deletes the region and the operator is left
// with a page that cannot be typed into.
func TestDesignWrite_RejectedOpen_HXReRendersTheBladeRegion(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if req.Path == "/design-sessions" {
			return http.StatusUnprocessableEntity, `{"error":"product is in another scope"}`
		}
		return 0, ""
	})
	const submission = "Operators need to bulk-export their incident list as CSV."

	rec := env.submitOpenHX(url.Values{"opening_submission": {submission}})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, "a refusal is 200, never the rejection's status: %s", body)
	assert.Empty(t, rec.Header().Get("HX-Redirect"), "a refusal must not navigate away")
	assert.NotContains(t, body, "<main", "the htmx half answers the region alone, with no shell chrome")

	root := strings.TrimLeft(body, " \t\r\n")
	require.True(t, strings.HasPrefix(root, "<div "+bladeRegion) ||
		strings.HasPrefix(root, "<div id=\"design-session-new-blade\""),
		"the served fragment's ROOT must be the blade region, so outerHTML replaces it rather than deleting it: %s", body)
	assertBladeSays(t, body, "422: product is in another scope", submission)
	assert.Contains(t, body, `hx-post="`+env.openPath()+`"`, "the doubled form keeps its htmx wiring across a re-render")
	assert.NotContains(t, body, `"error":`, "the raw JSON rejection must not leak into the fragment")
}

// TestDesignWrite_RejectedOpen_BladeKeepsTheProductAndTheTypedText is the
// no-JS half: the same refusal, rendered inside the shell, with the blade
// open over the list and the product it is opening a session under named
// read-only.
func TestDesignWrite_RejectedOpen_BladeKeepsTheProductAndTheTypedText(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.API.onRequest(func(req recordedRequest) (int, string) {
		if req.Path == "/design-sessions" {
			return http.StatusUnprocessableEntity, `{"error":"product is in another scope"}`
		}
		return 0, ""
	})
	const submission = "Operators need to bulk-export their incident list as CSV."

	rec := env.submitOpen(url.Values{"opening_submission": {submission}})
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, body)
	assertInShell(t, body)
	assertBladeSays(t, body, "422: product is in another scope", submission)

	assert.Contains(t, body, `data-krill="design-session-new-product"`,
		"the blade names the product it opens a session under")
	assert.Contains(t, body, ">krill<", "and the name is the one the URL resolved")
	assert.NotContains(t, bladeSectionOf(t, body), "<input",
		"the blade carries no product field: the product came from the URL")
	assert.Contains(t, body, `action="`+env.openPath()+`"`, "the form still posts to its own action")
}

// TestDesignWrite_TransportFailure_ReRendersTheBladeWithoutStoreText is
// the case the 200-re-render rule is really about: api is unreachable, so
// the write never reached krill at all.
//
// A 502 is the honest status and the useless one -- the operator gets a
// plain-text page instead of the form they just filled in, and the cause
// text (a URL, a driver message) is exactly what must not be rendered.
// Both modes answer 200 with the blade, the operator-facing half of the
// message, and their text.
func TestDesignWrite_TransportFailure_ReRendersTheBladeWithoutStoreText(t *testing.T) {
	const submission = "Operators need to bulk-export their incident list as CSV."

	for _, hx := range []bool{false, true} {
		name := "no-JS"
		if hx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			// Take the whole api away: the session mint fails first, which
			// is one of the two ways this failure happens in production.
			env.API.server.Close()

			rec := postForm(env.Mux, env.openPath(), url.Values{"opening_submission": {submission}}, hx, env.Cookie)
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code,
				"an unreachable krill still answers 200 with the form, never a status page: %s", body)
			assertBladeSays(t, body, "Could not reach krill: the request did not complete.", submission)
			assert.NotContains(t, body, "connection refused",
				"a transport failure's cause is logged, never rendered")
			if hx {
				assert.NotContains(t, body, "<main")
			} else {
				assertInShell(t, body)
			}
		})
	}
}

// TestDesignWrite_RejectedOpen_UnreadableListStillReturnsTheBlade covers
// the read behind the refusal failing too. The list is a nicety; the
// operator's paragraph is not. The blade comes back with the rejection
// stated AND the failed re-read said out loud, so an empty table is never
// mistaken for the whole answer.
func TestDesignWrite_RejectedOpen_UnreadableListStillReturnsTheBlade(t *testing.T) {
	for _, hx := range []bool{false, true} {
		name := "no-JS"
		if hx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			env.API.onRequest(func(req recordedRequest) (int, string) {
				if req.Path == "/design-sessions" {
					return http.StatusUnprocessableEntity, `{"error":"product is in another scope"}`
				}
				return 0, ""
			})
			env.App.designSessions = writeSurfaceSessions{
				summarizeErr: fmt.Errorf("pq: password authentication failed for user krill"),
			}
			const submission = "Operators need to bulk-export their incident list as CSV."

			rec := postForm(env.Mux, env.openPath(), url.Values{"opening_submission": {submission}}, hx, env.Cookie)
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code, body)
			assertBladeSays(t, body,
				"422: product is in another scope (the session list could not be reloaded)", submission)
			assert.NotContains(t, body, "password authentication", "store text must not reach the browser")
			if hx {
				assert.NotContains(t, body, "<main")
			} else {
				assertInShell(t, body)
			}
		})
	}
}

// bladeSectionOf slices the open blade out of a whole in-shell page, so an
// assertion about "what the blade carries" stays off the list and the
// chrome around it.
func bladeSectionOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, bladeRegion)
	require.NotEqual(t, -1, i, "body must carry the blade region")
	rest := body[i:]
	j := strings.Index(rest, "</article>")
	require.NotEqual(t, -1, j, "an open blade must contain its card")
	return rest[:j]
}

// ---------------------------------------------------------------------------
// FR 1942d934 -- a refused follow-up keeps the operator's text AND their ticks
//
// Four refusals, one shape: 200, the reason inline, the typed text and the
// ticked ids intact, the rail re-derived from a fresh read. What differs is
// which read is available to re-derive from -- and the degraded case, where
// none is, is the one that must not cost the operator anything.
// ---------------------------------------------------------------------------

// TestDesignWrite_EmptyRound_RefusedAt200 is the empty round: no
// follow-up text and nothing ticked records nothing meaningful, so it is
// refused before krill is asked at all -- the fake api records nothing, not
// even a session init.
//
// The refusal is still 200 and still carries the form, because an operator
// who pressed submit on a form this page gave them must get the form back
// rather than a bare 400 that tells them nothing about what to change.
func TestDesignWrite_EmptyRound_RefusedAt200(t *testing.T) {
	for _, hx := range []bool{false, true} {
		name := "no-JS"
		if hx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			// Whitespace-only text slips past the browser's `required`
			// check, which is why the rule is server-side.
			rec := postForm(env.Mux, env.answerPath(), url.Values{"follow_up": {"   "}}, hx, env.Cookie)
			body := rec.Body.String()

			require.Equal(t, http.StatusOK, rec.Code, "a refusal is 200, never a bare 400: %s", body)
			assert.Contains(t, body, `class="alert alert-error"`)
			assert.Contains(t, body, "Write a follow-up, or tick an open question your answer closes.")
			assert.Contains(t, body, `id="`+pages.FollowUpFormAnchor+`"`,
				"the operator gets the form back, not a dead end")
			assert.Empty(t, env.API.recorded(), "an empty round must never reach krill")
			if hx {
				assert.NotContains(t, body, "<main")
				assert.Contains(t, body, `id="`+pages.DesignSessionRoundAnchor+`"`)
			} else {
				assertInShell(t, body)
			}
		})
	}
}

// TestDesignWrite_RefusedAnswer_KeepsTextAndTicks is the same refusal, but
// with the operator's own work on the form: typed text and a ticked box,
// against a krill that refuses. Both must come back.
//
// The two halves used to disagree here -- the whole-page refusal preserved
// the tick through the rail, the htmx one did not, because it swapped the
// form alone. Both now answer the round region, so a tick survives either
// mode.
func TestDesignWrite_RefusedAnswer_KeepsTextAndTicks(t *testing.T) {
	for _, hx := range []bool{false, true} {
		name := "no-JS"
		if hx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			env.API.onRequest(func(req recordedRequest) (int, string) {
				if strings.HasSuffix(req.Path, "/revision-events") {
					return http.StatusConflict, `{"error":"question ` + testClosedQuestion + ` is not open"}`
				}
				return 0, ""
			})
			const followUp = "half a thought I had not finished"

			rec := postForm(env.Mux, env.answerPath(), url.Values{
				"follow_up": {followUp},
				"resolve":   {testClosedQuestion},
			}, hx, env.Cookie)
			body := rec.Body.String()

			require.Equal(t, http.StatusOK, rec.Code, "a refusal is 200, never the rejection's status: %s", body)
			assert.Contains(t, body, ">"+followUp+"</textarea>",
				"FR 1942d934: the typed follow-up survives the refusal in both modes")
			assert.True(t, resolveBoxChecked(t, body, testClosedQuestion),
				"FR 1942d934: the ticked question survives the refusal in BOTH modes")
			assert.False(t, resolveBoxChecked(t, body, testKeptQuestion),
				"an unticked box must not come back ticked")
			if hx {
				assert.Contains(t, body, `id="`+pages.DesignSessionRoundAnchor+`"`)
			} else {
				assertInShell(t, body)
			}
		})
	}
}

// TestDesignWrite_StaleAnswer_RefusedWithAFreshlyReadRail is the stale-tick
// case: the operator ticked a question that some other round has resolved
// since the page was rendered, so krill refuses the write. The rail in the
// refusal must come from a FRESH read, which means the resolved question is
// not offered as a box to tick again -- while the operator's own text
// survives, because that is still theirs.
func TestDesignWrite_StaleAnswer_RefusedWithAFreshlyReadRail(t *testing.T) {
	for _, hx := range []bool{false, true} {
		name := "no-JS"
		if hx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			env.API.onRequest(func(req recordedRequest) (int, string) {
				if strings.HasSuffix(req.Path, "/revision-events") {
					return http.StatusConflict, `{"error":"question ` + testClosedQuestion + ` is not open"}`
				}
				return 0, ""
			})
			// Resolved by another round after the page was rendered.
			env.Events.resolveOne(env.SessionID, testClosedQuestion)
			const followUp = "It should read the postgres flag table."

			rec := postForm(env.Mux, env.answerPath(), url.Values{
				"follow_up": {followUp},
				"resolve":   {testClosedQuestion},
			}, hx, env.Cookie)
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code, body)
			assert.Contains(t, body, `class="alert alert-error"`)
			assert.Contains(t, body, "409: question "+testClosedQuestion+" is not open")

			// The rail is freshly read: a question krill no longer has
			// open is not offered for answering again.
			rail := pageSectionOf(t, body, regionOpenQuestion, regionSessionProps)
			assert.NotContains(t, rail, testClosedQuestion,
				"a question resolved since the page load is not offered as a box again")
			assert.Contains(t, rail, testKeptQuestion, "the question still open is still offered")

			assert.Contains(t, body, ">"+followUp+"</textarea>",
				"the operator's text survives a stale-tick refusal")
			if hx {
				assert.Contains(t, body, `id="`+pages.DesignSessionRoundAnchor+`"`)
			}
		})
	}
}

// TestDesignWrite_Answer_TransportFailureKeepsTheWork is the case the
// 200-re-render rule is really about: api is unreachable, so the write
// never reached krill at all.
//
// Both modes answer 200 with the round region, the operator-facing half of
// the message, and their text -- and the cause (a URL, a driver message) is
// never rendered. The rail is still re-read where it can be: the read rides
// a different client from the write, so it can succeed when the write could
// not, and it is only the failure of BOTH that degrades the region.
func TestDesignWrite_Answer_TransportFailureKeepsTheWork(t *testing.T) {
	const followUp = "It should read the postgres flag table."

	for _, hx := range []bool{false, true} {
		name := "no-JS"
		if hx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			env.API.server.Close() // the whole api is gone

			rec := postForm(env.Mux, env.answerPath(), url.Values{
				"follow_up": {followUp},
				"resolve":   {testClosedQuestion},
			}, hx, env.Cookie)
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code,
				"an unreachable krill still answers 200 with the round, never a status page: %s", body)
			assert.Contains(t, body, "Could not reach krill: the request did not complete.")
			assert.NotContains(t, body, "connection refused",
				"a transport failure's cause is logged, never rendered")
			assert.Contains(t, body, ">"+followUp+"</textarea>", "the operator's text survives")

			// The read side still works, so the rail is real and the tick
			// is where the operator left it.
			assert.True(t, resolveBoxChecked(t, body, testClosedQuestion),
				"a transport failure is not a data-loss event: the tick is still ticked")
			assert.Contains(t, body, testKeptQuestion)
			if hx {
				assert.NotContains(t, body, "<main")
				assert.Contains(t, body, `id="`+pages.DesignSessionRoundAnchor+`"`)
			} else {
				assertInShell(t, body)
			}
		})
	}
}

// TestDesignWrite_Answer_FailedReReadStillCarriesTheTicks is the last
// fallback: krill refused the write AND the re-read behind the refusal
// failed. There is no rail to render a box in, so the ticked ids ride as
// HIDDEN inputs -- the operator is not being asked to re-approve anything,
// we are preserving what they already chose, and a resubmit must not
// silently drop it.
func TestDesignWrite_Answer_FailedReReadStillCarriesTheTicks(t *testing.T) {
	for _, hx := range []bool{false, true} {
		name := "no-JS"
		if hx {
			name = "htmx"
		}
		t.Run(name, func(t *testing.T) {
			env := newDesignWriteEnv(t)
			env.API.onRequest(func(req recordedRequest) (int, string) {
				if strings.HasSuffix(req.Path, "/revision-events") {
					return http.StatusConflict, `{"error":"question ` + testClosedQuestion + ` is not open"}`
				}
				return 0, ""
			})
			// And now the read behind the refusal breaks too.
			env.App.designSessions = writeSurfaceSessions{
				summaryErr: fmt.Errorf("pq: could not connect to the krill database"),
			}
			const followUp = "It should read the postgres flag table."

			rec := postForm(env.Mux, env.answerPath(), url.Values{
				"follow_up": {followUp},
				"resolve":   {testClosedQuestion},
			}, hx, env.Cookie)
			body := rec.Body.String()
			require.Equal(t, http.StatusOK, rec.Code, body)

			assert.Contains(t, body, `data-krill="degraded-resolve"`,
				"with no readable rail, the ticked ids ride as hidden inputs")
			assert.Contains(t, body, `name="resolve" value="`+testClosedQuestion+`"`,
				"FR 1942d934: the ticked id survives even the failed re-read")
			assert.Contains(t, body, ">"+followUp+"</textarea>")
			assert.Contains(t, body, `class="alert alert-error"`)

			// The unread regions SAY SO rather than rendering as empty:
			// "this timeline is unavailable" and "this session has no
			// rounds yet" are different facts (NFR ca90dc03).
			assert.Contains(t, body, `data-krill="revision-events-error"`)
			assert.Contains(t, body, `data-krill="open-questions-error"`)
			assert.NotContains(t, body, `data-krill="open-questions-empty"`,
				"an unread question list is not a session with nothing waiting on it")

			assert.NotContains(t, body, "could not connect to the krill database",
				"store text must not reach the browser")
			assert.NotContains(t, body, "password", "nor any other driver text")
			if hx {
				assert.Contains(t, body, `id="`+pages.DesignSessionRoundAnchor+`"`,
					"the degraded region still carries the id the form's hx-target names")
			} else {
				assertInShell(t, body)
			}
		})
	}
}

// TestDesignWrite_SignedOffSessionRendersNoFollowUpForm is FR d81d2283's
// terminal case: a session whose latest signoff is approved takes no
// further rounds, so the page offers no way to send one.
//
// Driven through the ONE derivation the page reads -- the session's derived
// stage, which is also what the badge shows -- rather than through some
// separate fact about the log. A second rule is how the badge and the
// form's presence would come to disagree about the same session.
func TestDesignWrite_SignedOffSessionRendersNoFollowUpForm(t *testing.T) {
	env := newDesignWriteEnv(t)
	env.Sessions.stage = store.StageApproved
	env.App.designSessions = env.Sessions

	body := env.getDetail(t, designSessionPath(env.ProductID, env.SessionID))
	require.Equal(t, http.StatusOK, body.Code, body.Body.String())
	html := body.Body.String()

	assert.NotContains(t, html, `id="`+pages.FollowUpFormAnchor+`"`,
		"a signed-off session shows no follow-up form at all")
	assert.NotContains(t, html, `name="follow_up"`,
		"so there is no textarea to type a round into")
	assert.NotContains(t, html, "Submit follow-up")
	assert.Contains(t, html, `data-krill="design-session-signed-off"`,
		"and it says why, rather than leaving the operator wondering where the form went")

	// The rest of the page is untouched: the round region's id is still
	// there (it is the swap target either way), and the rail still reads.
	assert.Contains(t, html, `id="`+pages.DesignSessionRoundAnchor+`"`)
	assert.Contains(t, html, regionOpenQuestion)

	// And the badge is the same derivation, so the two cannot disagree.
	assert.Contains(t, html, components.DesignSessionStageLabel(string(store.StageApproved)),
		"the badge shows the approved stage the no-form decision was made from")
}

// TestDesignWrite_OpenSessionRendersTheFollowUpForm is the control for the
// test above: every other stage keeps the form, so "signed off hides it" is
// a statement about this stage and not about the page losing its form.
func TestDesignWrite_OpenSessionRendersTheFollowUpForm(t *testing.T) {
	for _, stage := range []store.Stage{
		store.StageOpened, store.StageInDraft, store.StageAnswered,
		store.StageChangesRequested, store.StageArchitectReview, store.StageRuled,
	} {
		t.Run(string(stage), func(t *testing.T) {
			env := newDesignWriteEnv(t)
			env.Sessions.stage = stage
			env.App.designSessions = env.Sessions

			html := env.getDetail(t, designSessionPath(env.ProductID, env.SessionID)).Body.String()
			assert.Contains(t, html, `id="`+pages.FollowUpFormAnchor+`"`,
				"an open session keeps its follow-up form")
			assert.NotContains(t, html, `data-krill="design-session-signed-off"`)
		})
	}
}

// TestDesignWrite_Answer_FormCarriesNoIdentityField is FR 6d8c70b2 on the
// form itself rather than on the write body: the form's own fields are the
// round's arguments and nothing else. Identity reaches krill through the
// gating session (withKrillSession), so an operator identity, a scope id or
// a session id has nowhere to be typed.
func TestDesignWrite_Answer_FormCarriesNoIdentityField(t *testing.T) {
	env := newDesignWriteEnv(t)
	html := env.getDetail(t, designSessionPath(env.ProductID, env.SessionID)).Body.String()

	form := betweenTags(t, html, `id="`+pages.FollowUpFormAnchor+`"`, "</form>")
	for _, forbidden := range []string{`name="acting"`, `name="on_behalf_of"`, `name="iss"`,
		`name="sub"`, `name="scope_id"`, `name="session_id"`, `name="krill_session_id"`, `name="entity_id"`} {
		assert.NotContains(t, form, forbidden,
			"the form must offer no input for %s: identity reaches krill only through the gating session", forbidden)
	}
	// What it does carry is the round's own arguments, and the resolve
	// boxes live in the rail.
	assert.Contains(t, form, `name="follow_up"`)
	assert.NotContains(t, form, `name="resolve"`,
		"the resolve boxes are in the rail and join this form by the form attribute")
	// The boxes themselves are OUTSIDE the form -- that is the whole point
	// of the association -- so this is asserted on the page, not the form.
	assert.Equal(t, 2, strings.Count(html, `name="resolve"`),
		"one resolve input per open question, wherever it renders")
	assert.Equal(t, 2, strings.Count(html, `form="`+pages.FollowUpFormAnchor+`"`),
		"every resolve box names this form by id, which is how it posts with JavaScript disabled")
}

// TestDesignWrite_Answer_FormIsDoubled is the one-route-two-modes rule on
// this form: the no-JS half is method+action to the answers path, the htmx
// half is hx-post plus a swap target that resolves. Both are asserted off
// the served tag, so a form that loses its doubling fails here rather than
// silently working only for whichever browser this run happened to be.
func TestDesignWrite_Answer_FormIsDoubled(t *testing.T) {
	env := newDesignWriteEnv(t)
	html := env.getDetail(t, designSessionPath(env.ProductID, env.SessionID)).Body.String()

	assert.Equal(t, env.answerPath(), formAttr(t, html, "action"),
		"the no-JS half posts to the answers action")
	assert.Equal(t, "post", formAttr(t, html, "method"))
	assert.Equal(t, env.answerPath(), formAttr(t, html, "hx-post"),
		"the htmx half posts to the same route")
	assert.Equal(t, "#"+pages.DesignSessionRoundAnchor, formAttr(t, html, "hx-target"),
		"and swaps the round region, because a round changes the timeline and the rail too")
	assert.Equal(t, "outerHTML", formAttr(t, html, "hx-swap"))

	// The swap target exists on the page the form is served into: an
	// hx-target that resolves to nothing makes htmx return before it even
	// issues the request, so the form would look frozen.
	assert.Contains(t, html, `id="`+pages.DesignSessionRoundAnchor+`"`,
		"the form's hx-target must resolve on the page it is served into")
}
