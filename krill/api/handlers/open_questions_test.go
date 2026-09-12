// Unit tests for ListOpenQuestionsHandler (open_questions.go, issue
// #2545's Testing section, handler cases 7-10). No Postgres dependency --
// fakeDesignSessionStore/fakeRevisionEventStore
// (fake_design_session_store_test.go) stand in for the store interfaces
// (krill/store/open_questions_integration_test.go covers the real-Postgres
// derivation).
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// doListOpenQuestionsRequest mounts ListOpenQuestionsHandler behind a
// "GET /design-sessions/{id}/open-questions" path pattern -- exactly like
// routes.go -- so r.PathValue("id") is populated the same way a real
// request would populate it. Never wrapped in RequireSession (routes.go's
// doc comment: this is one of the ungated read routes).
func doListOpenQuestionsRequest(t *testing.T, designSessions store.DesignSessionStore, events store.RevisionEventStore, id, rawQuery string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle("GET /design-sessions/{id}/open-questions", handlers.ListOpenQuestionsHandler(designSessions, events))

	url := "/design-sessions/" + id + "/open-questions"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestListOpenQuestionsHandler_UnknownSession_Returns404 is Testing case 7's
// first half.
func TestListOpenQuestionsHandler_UnknownSession_Returns404(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()

	rec := doListOpenQuestionsRequest(t, designSessions, events, uuid.New().String(), "")

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestListOpenQuestionsHandler_NoEvents_ReturnsEmptyList is Testing case
// 7's second half: a session with no events is a 200 with an empty list,
// not a 404 -- "no events" and "unknown session" must not collapse into
// the same response.
func TestListOpenQuestionsHandler_NoEvents_ReturnsEmptyList(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()
	ds := store.DesignSession{ID: uuid.New()}
	designSessions.put(ds)

	rec := doListOpenQuestionsRequest(t, designSessions, events, ds.ID.String(), "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		OpenQuestions []struct {
			QuestionID string `json:"question_id"`
		} `json:"open_questions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.OpenQuestions)
}

// TestListOpenQuestionsHandler_BlockingFilter is Testing case 8:
// ?blocking=true returns only blocking questions.
func TestListOpenQuestionsHandler_BlockingFilter(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()
	ds := store.DesignSession{ID: uuid.New()}
	designSessions.put(ds)
	self := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}

	_, err := events.Append(t.Context(), store.NewRevisionEvent{
		SessionID: ds.ID, Acting: self, OnBehalfOf: self, EventType: store.EventTypeRuling,
		OpenQuestionsDelta: store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
			{QuestionID: "q-blocking", Blocking: true, Text: "must resolve before ship"},
			{QuestionID: "q-nonblocking", Blocking: false, Text: "nice to know"},
		}},
	})
	require.NoError(t, err)

	rec := doListOpenQuestionsRequest(t, designSessions, events, ds.ID.String(), "blocking=true")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		OpenQuestions []struct {
			QuestionID string `json:"question_id"`
			Blocking   bool   `json:"blocking"`
		} `json:"open_questions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.OpenQuestions, 1)
	assert.Equal(t, "q-blocking", resp.OpenQuestions[0].QuestionID)
	assert.True(t, resp.OpenQuestions[0].Blocking)
}

// TestFR7_ResolutionIsARevisionEventCarryingBothIdentities is Testing case
// 9: append an `answer` event (through AppendRevisionEventHandler)
// resolving a question, with acting != on-behalf-of on the gating
// krill_session, then assert (a) the question disappears from
// ListOpenQuestionsHandler's output and (b) GetDesignSessionHandler shows
// that resolution as a revision_event carrying both identity triples
// distinctly. This is the single test that proves FR7: "the resolution and
// who made it -- and on whose behalf -- are part of the same append-only
// record", with no mutable question row involved anywhere.
func TestFR7_ResolutionIsARevisionEventCarryingBothIdentities(t *testing.T) {
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-resolver", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "human-owner", Kind: store.SubjectKindHuman}
	sessions := newFakeSessionStore()
	scopeID := uuid.New()
	sessionIDStr := mustInitSession(t, sessions, scopeID, acting, onBehalfOf).String()

	designSessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()
	ds := store.DesignSession{ID: uuid.New(), ScopeID: scopeID}
	designSessions.put(ds)

	// Open the question first (a draft round, self-acting is fine for the
	// open half -- FR7's identity claim is about the resolution).
	self := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-drafter", Kind: store.SubjectKindService}
	verified := "abc123"
	_, err := events.Append(t.Context(), store.NewRevisionEvent{
		ScopeID: scopeID, SessionID: ds.ID, Acting: self, OnBehalfOf: self,
		EventType: store.EventTypeDraft, VerifiedAgainst: &verified,
		OpenQuestionsDelta: store.OpenQuestionsDelta{Opened: []store.OpenQuestionOpened{
			{QuestionID: "q1", Blocking: true, Text: "what storage backend?"},
		}},
	})
	require.NoError(t, err)

	// Resolve it through the real gated append endpoint, acting != on_behalf_of.
	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, ds.ID.String(),
		`{"event_type": "answer", "entity_deltas": [], "open_questions_delta": {"resolved": ["q1"]}}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// (a) the question disappears from the derived open-question view.
	oqRec := doListOpenQuestionsRequest(t, designSessions, events, ds.ID.String(), "")
	require.Equal(t, http.StatusOK, oqRec.Code, oqRec.Body.String())
	var oqResp struct {
		OpenQuestions []struct {
			QuestionID string `json:"question_id"`
		} `json:"open_questions"`
	}
	require.NoError(t, json.Unmarshal(oqRec.Body.Bytes(), &oqResp))
	assert.Empty(t, oqResp.OpenQuestions, "a resolved question must not appear in the open-question view")

	// (b) GetDesignSessionHandler shows the resolution as a revision_event
	// carrying both identity triples distinctly -- proving the resolution
	// and its resolver identity live in the same append-only record, not a
	// mutable question row.
	dsRec := doGetDesignSessionRequest(t, designSessions, events, ds.ID.String())
	require.Equal(t, http.StatusOK, dsRec.Code, dsRec.Body.String())
	var dsResp struct {
		RevisionEvents []struct {
			EventType  string `json:"event_type"`
			Acting     struct{ Sub string } `json:"acting"`
			OnBehalfOf struct{ Sub string } `json:"on_behalf_of"`
			OpenQuestionsDelta struct {
				Resolved []string `json:"resolved"`
			} `json:"open_questions_delta"`
		} `json:"revision_events"`
	}
	require.NoError(t, json.Unmarshal(dsRec.Body.Bytes(), &dsResp))
	require.Len(t, dsResp.RevisionEvents, 2)
	resolution := dsResp.RevisionEvents[1]
	assert.Equal(t, "answer", resolution.EventType)
	assert.Equal(t, []string{"q1"}, resolution.OpenQuestionsDelta.Resolved)
	assert.Equal(t, "agent-resolver", resolution.Acting.Sub)
	assert.Equal(t, "human-owner", resolution.OnBehalfOf.Sub)
	assert.NotEqual(t, resolution.Acting.Sub, resolution.OnBehalfOf.Sub, "acting and on-behalf-of must round-trip distinctly")
}

// TestAppendRevisionEventHandler_ResolvedNamesUnopenedQuestion_Returns400 is
// Testing case 10: a `resolved` entry naming a question never opened in
// the session is a 400 at the append endpoint, not a silently-ignored
// no-op.
func TestAppendRevisionEventHandler_ResolvedNamesUnopenedQuestion_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID,
		`{"event_type": "answer", "entity_deltas": [], "open_questions_delta": {"resolved": ["never-opened"]}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestAppendRevisionEventHandler_ReopenPreviouslyResolvedQuestion_Succeeds
// covers the store's Testing case 3 at the handler level: re-opening a
// previously resolved question is allowed (last-event-wins), not rejected
// as if it were a "resolved names unopened" violation.
func TestAppendRevisionEventHandler_ReopenPreviouslyResolvedQuestion_Succeeds(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID.String(),
		`{"event_type": "draft", "entity_deltas": [], "open_questions_delta": {"opened": [{"question_id": "q1", "blocking": true, "text": "what storage backend?"}]}, "verified_against": "abc123"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID.String(),
		`{"event_type": "answer", "entity_deltas": [], "open_questions_delta": {"resolved": ["q1"]}}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID.String(),
		`{"event_type": "draft", "entity_deltas": [], "open_questions_delta": {"opened": [{"question_id": "q1", "blocking": false, "text": "reopened, lower priority now"}]}, "verified_against": "abc123"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	designSessions := newFakeDesignSessionStore()
	designSessions.put(store.DesignSession{ID: sessionPathID})
	oqRec := doListOpenQuestionsRequest(t, designSessions, events, sessionPathID.String(), "")
	require.Equal(t, http.StatusOK, oqRec.Code, oqRec.Body.String())

	var resp struct {
		OpenQuestions []struct {
			QuestionID string `json:"question_id"`
			Blocking   bool   `json:"blocking"`
			Text       string `json:"text"`
		} `json:"open_questions"`
	}
	require.NoError(t, json.Unmarshal(oqRec.Body.Bytes(), &resp))
	require.Len(t, resp.OpenQuestions, 1)
	assert.Equal(t, "q1", resp.OpenQuestions[0].QuestionID)
	assert.False(t, resp.OpenQuestions[0].Blocking, "the re-opened event's blocking flag must win, not the original")
	assert.Equal(t, "reopened, lower priority now", resp.OpenQuestions[0].Text)
}
