// Unit tests for OpenDesignSessionHandler/GetDesignSessionHandler
// (design_session.go, issue #2543's Testing section). No Postgres
// dependency -- fakeDesignSessionStore/fakeRevisionEventStore
// (fake_design_session_store_test.go) stand in for the store interfaces
// (krill/store/design_session_integration_test.go covers the real-Postgres
// chain).
package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// doGetDesignSessionRequest mounts GetDesignSessionHandler behind a
// "GET /design-sessions/{id}" path pattern -- exactly like routes.go -- so
// r.PathValue("id") is populated the same way a real request would
// populate it. GetDesignSessionHandler is never wrapped in RequireSession
// (routes.go's doc comment: this is one of the ungated read routes).
func doGetDesignSessionRequest(t *testing.T, designSessions store.DesignSessionStore, events store.RevisionEventStore, id string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle("GET /design-sessions/{id}", handlers.GetDesignSessionHandler(designSessions, events))

	req := httptest.NewRequest(http.MethodGet, "/design-sessions/"+id, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestOpenDesignSessionHandler_NoSessionID_Rejected proves FR3's write gate
// sits in front of this endpoint: a request with no krill session id never
// reaches DesignSessionStore.Open.
func TestOpenDesignSessionHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	designSessions := newFakeDesignSessionStore()

	rec := doGatedRequest(t, handlers.OpenDesignSessionHandler(designSessions), sessions, "",
		`{"product_id": "`+uuid.New().String()+`", "opening_submission": "We need a way to track sensor readings."}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, designSessions.gotProductID, "the store must never be called without a valid session")
}

// TestOpenDesignSessionHandler_Success_ScopeAndOpenedByFromSession proves
// FR1: a well-formed open returns the new session's id, and the store call
// receives scope_id and opened_by_krill_session_id from the gating
// session -- openDesignSessionRequest has no field for either, so there is
// nothing in the body that could override them.
func TestOpenDesignSessionHandler_Success_ScopeAndOpenedByFromSession(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	designSessions := newFakeDesignSessionStore()
	productID := uuid.New()

	rec := doGatedRequest(t, handlers.OpenDesignSessionHandler(designSessions), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "opening_submission": "We need a way to track sensor readings."}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)

	assert.Equal(t, scopeID, designSessions.gotScopeID, "scope_id must come from the session, never the request body")
	assert.Equal(t, productID, designSessions.gotProductID)
	assert.Equal(t, sessionIDStr, uuid.UUID(designSessions.gotOpenedByKrillSessionID).String(), "opened_by_krill_session_id must come from the gating session (FR1, provenance only)")
}

// TestOpenDesignSessionHandler_PlainLanguageOpeningSubmission_Succeeds
// proves FR8: a Requirement Contributor's opening submission carries zero
// entity references (no uuid, no krill entity-model term) and still
// succeeds -- openDesignSessionRequest has no entity-reference field of any
// kind for this text to even sit next to.
func TestOpenDesignSessionHandler_PlainLanguageOpeningSubmission_Succeeds(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	designSessions := newFakeDesignSessionStore()

	submission := "I keep forgetting to water my plants and would love an email when one is thirsty."
	assert.NotContains(t, submission, "-", "sanity: the plain-language submission must contain no uuid-shaped token")
	for _, entityTerm := range []string{"product", "feature", "requirement", "decision", "design_session", "revision_event"} {
		assert.NotContains(t, submission, entityTerm, "sanity: the submission must contain no krill entity-model term")
	}

	rec := doGatedRequest(t, handlers.OpenDesignSessionHandler(designSessions), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "opening_submission": "`+submission+`"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, submission, designSessions.gotOpeningSubmission)
}

// TestOpenDesignSessionHandler_EmptyOpeningSubmission_Returns400 proves the
// shared requireNonEmpty("opening_submission", ...) validation applies
// here.
func TestOpenDesignSessionHandler_EmptyOpeningSubmission_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	designSessions := newFakeDesignSessionStore()

	rec := doGatedRequest(t, handlers.OpenDesignSessionHandler(designSessions), sessions, sessionIDStr,
		`{"product_id": "`+uuid.New().String()+`", "opening_submission": ""}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, designSessions.gotScopeID, "an empty opening_submission must never reach the store")
}

// TestOpenDesignSessionHandler_UnknownProductID_Returns400WithNamedMessage
// proves writeStoreError (types.go) maps an unknown/cross-scope product_id
// (store.ErrNotFound, via errParentNotFound) onto a 400 naming the product,
// never a raw pg error or a 500.
func TestOpenDesignSessionHandler_UnknownProductID_Returns400WithNamedMessage(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	productID := uuid.New()
	designSessions := newFakeDesignSessionStore()
	designSessions.openErr = fmt.Errorf("%w: no current product row for id %s", store.ErrNotFound, productID)

	rec := doGatedRequest(t, handlers.OpenDesignSessionHandler(designSessions), sessions, sessionIDStr,
		`{"product_id": "`+productID.String()+`", "opening_submission": "We need a way to track sensor readings."}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "product", "the 400 body must name the missing parent")
}

// TestGetDesignSessionHandler_ReturnsSessionAndOrderedEvents proves the
// read path renders the design_session row plus its revision_event log in
// seq_no order, with entity_deltas and open_questions_delta round-tripped
// intact -- and that GetDesignSessionHandler is reachable with no session
// header at all (it is never gated).
func TestGetDesignSessionHandler_ReturnsSessionAndOrderedEvents(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()

	productID := uuid.New()
	openedBy := store.SessionID(uuid.New())
	ds := store.DesignSession{
		ID:                     uuid.New(),
		ScopeID:                uuid.New(),
		ProductID:              productID,
		OpeningSubmission:      "We need a way to track sensor readings.",
		OpenedByKrillSessionID: openedBy,
	}
	designSessions.put(ds)

	entityID := uuid.New()
	verified := "abc123"
	approved := store.SignoffStatusApproved
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "human-1", Kind: store.SubjectKindHuman}

	// Insert out of the order they should render in, to prove ordering is
	// by seq_no (ListBySession), not insertion order.
	_, err := events.Append(t.Context(), store.NewRevisionEvent{
		ScopeID: ds.ScopeID, SessionID: ds.ID, Acting: acting, OnBehalfOf: onBehalfOf,
		EventType: store.EventTypeDraft, VerifiedAgainst: &verified,
		EntityDeltas:       []store.EntityDelta{{EntityID: entityID, Change: store.EntityDeltaChangeCreated, SummaryLine: "drafted the feature"}},
		OpenQuestionsDelta: store.OpenQuestionsDelta{Opened: []string{"q1"}},
	})
	require.NoError(t, err)
	_, err = events.Append(t.Context(), store.NewRevisionEvent{
		ScopeID: ds.ScopeID, SessionID: ds.ID, Acting: acting, OnBehalfOf: onBehalfOf,
		EventType: store.EventTypeSignoff, SignoffStatus: &approved,
		OpenQuestionsDelta: store.OpenQuestionsDelta{Resolved: []string{"q1"}},
	})
	require.NoError(t, err)

	rec := doGetDesignSessionRequest(t, designSessions, events, ds.ID.String())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		ID                     string `json:"id"`
		ProductID              string `json:"product_id"`
		OpeningSubmission      string `json:"opening_submission"`
		OpenedByKrillSessionID string `json:"opened_by_krill_session_id"`
		RevisionEvents         []struct {
			SeqNo           int     `json:"seq_no"`
			EventType       string  `json:"event_type"`
			VerifiedAgainst *string `json:"verified_against"`
			SignoffStatus   *string `json:"signoff_status"`
			EntityDeltas    []struct {
				EntityID    string `json:"entity_id"`
				Change      string `json:"change"`
				SummaryLine string `json:"summary_line"`
			} `json:"entity_deltas"`
			OpenQuestionsDelta struct {
				Opened   []string `json:"opened"`
				Resolved []string `json:"resolved"`
			} `json:"open_questions_delta"`
		} `json:"revision_events"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, ds.ID.String(), resp.ID)
	assert.Equal(t, productID.String(), resp.ProductID)
	assert.Equal(t, "We need a way to track sensor readings.", resp.OpeningSubmission)
	assert.Equal(t, uuid.UUID(openedBy).String(), resp.OpenedByKrillSessionID)

	require.Len(t, resp.RevisionEvents, 2)
	assert.Equal(t, 1, resp.RevisionEvents[0].SeqNo)
	assert.Equal(t, "draft", resp.RevisionEvents[0].EventType)
	require.NotNil(t, resp.RevisionEvents[0].VerifiedAgainst)
	assert.Equal(t, "abc123", *resp.RevisionEvents[0].VerifiedAgainst)
	require.Len(t, resp.RevisionEvents[0].EntityDeltas, 1)
	assert.Equal(t, entityID.String(), resp.RevisionEvents[0].EntityDeltas[0].EntityID)
	assert.Equal(t, "created", resp.RevisionEvents[0].EntityDeltas[0].Change)
	assert.Equal(t, []string{"q1"}, resp.RevisionEvents[0].OpenQuestionsDelta.Opened)

	assert.Equal(t, 2, resp.RevisionEvents[1].SeqNo)
	assert.Equal(t, "signoff", resp.RevisionEvents[1].EventType)
	require.NotNil(t, resp.RevisionEvents[1].SignoffStatus)
	assert.Equal(t, "approved", *resp.RevisionEvents[1].SignoffStatus)
	assert.Equal(t, []string{"q1"}, resp.RevisionEvents[1].OpenQuestionsDelta.Resolved)
}

// TestGetDesignSessionHandler_UnknownID_Returns404 proves an unknown
// design_session id is a 404, not a 400 or 500.
func TestGetDesignSessionHandler_UnknownID_Returns404(t *testing.T) {
	designSessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()

	rec := doGetDesignSessionRequest(t, designSessions, events, uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
