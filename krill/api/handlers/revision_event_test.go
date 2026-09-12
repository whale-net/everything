// Unit tests for AppendRevisionEventHandler (revision_event.go, issue
// #2543's Testing section). No Postgres dependency --
// fakeRevisionEventStore (fake_design_session_store_test.go) stands in for
// store.RevisionEventStore, replicating the store's FR3/FR4/entity_deltas
// validation so this package's tests can drive the handler's error mapping
// without a database (krill/store/revision_event_integration_test.go covers
// the real-Postgres chain, including the row-lock seq_no allocation).
package handlers_test

import (
	"bytes"
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

// doAppendRevisionEventRequest wraps handler in
// handlers.RequireSession(sessions) (the exact wiring routes.go uses) and
// mounts it behind a "{id}" path pattern -- exactly like routes.go's
// "POST /design-sessions/{id}/revision-events" -- so r.PathValue("id") is
// populated the same way a real request would populate it.
// sessionIDHeader == "" omits the X-Krill-Session-Id header entirely (the
// "no session id" rejection case).
func doAppendRevisionEventRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, sessionPathID, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /design-sessions/{id}/revision-events", gated)

	req := httptest.NewRequest(http.MethodPost, "/design-sessions/"+sessionPathID+"/revision-events", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAppendRevisionEventHandler_NoSessionID_Rejected proves FR3's write
// gate sits in front of this endpoint: a request with no krill session id
// never reaches RevisionEventStore.Append.
func TestAppendRevisionEventHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, "", sessionPathID,
		`{"event_type": "ruling", "entity_deltas": [], "open_questions_delta": {}}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, events.gotEvent.ScopeID, "the store must never be called without a valid session")
}

// TestAppendRevisionEventHandler_ActingDiffersFromOnBehalfOf_
// BothRecordedDistinctly proves FR2: the gating session's acting and
// on-behalf-of subjects are copied verbatim onto the new event when they
// differ -- and that appendRevisionEventRequest carries no field a caller
// could use to assert a different identity (the body below has none).
func TestAppendRevisionEventHandler_ActingDiffersFromOnBehalfOf_BothRecordedDistinctly(t *testing.T) {
	sessions := newFakeSessionStore()
	scopeID := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-caller", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "human-owner", Kind: store.SubjectKindHuman}
	sessionID := mustInitSession(t, sessions, scopeID, acting, onBehalfOf)
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionID.String(), sessionPathID,
		`{"event_type": "ruling", "entity_deltas": [], "open_questions_delta": {}}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, events.gotEvent.ScopeID, "scope_id must come from the session, never the request body")
	assert.Equal(t, acting, events.gotEvent.Acting)
	assert.Equal(t, onBehalfOf, events.gotEvent.OnBehalfOf)
	assert.NotEqual(t, events.gotEvent.Acting, events.gotEvent.OnBehalfOf, "an append must never collapse acting and on_behalf_of into one subject")
}

// TestAppendRevisionEventHandler_BodyWithIdentityField_RejectedAsUnknownField
// proves the other half of FR2/NFR2's invariant: appendRevisionEventRequest
// declares no field for acting/on_behalf_of/scope_id, so decodeStrict's
// unknown-field rejection means a caller cannot even submit a body that
// tries to assert one -- it is rejected outright, before the store is ever
// called, rather than silently ignored.
func TestAppendRevisionEventHandler_BodyWithIdentityField_RejectedAsUnknownField(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID,
		`{"event_type": "ruling", "entity_deltas": [], "open_questions_delta": {}, "acting": {"iss": "https://evil.example.com", "sub": "impersonator", "kind": "human"}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, events.gotEvent.ScopeID, "a body asserting an identity field must never reach the store")
}

// TestAppendRevisionEventHandler_VerifiedAgainstRule covers FR3's
// conditional-presence rule for each of its two affected event types.
func TestAppendRevisionEventHandler_VerifiedAgainstRule(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "draft without verified_against is rejected",
			body:       `{"event_type": "draft", "entity_deltas": [], "open_questions_delta": {}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "draft with a sha succeeds",
			body:       `{"event_type": "draft", "entity_deltas": [], "open_questions_delta": {}, "verified_against": "abc123"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "answer with verified_against is rejected",
			body:       `{"event_type": "answer", "entity_deltas": [], "open_questions_delta": {}, "verified_against": "abc123"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			events := newFakeRevisionEventStore()
			sessionPathID := uuid.New().String()

			rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID, tc.body)

			assert.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
		})
	}
}

// TestAppendRevisionEventHandler_SignoffStatusRule covers FR4's
// conditional-presence rule and its closed two-value enum, including that
// free text is rejected in place of the enum.
func TestAppendRevisionEventHandler_SignoffStatusRule(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "signoff without signoff_status is rejected",
			body:       `{"event_type": "signoff", "entity_deltas": [], "open_questions_delta": {}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "signoff with approved succeeds",
			body:       `{"event_type": "signoff", "entity_deltas": [], "open_questions_delta": {}, "signoff_status": "approved"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "signoff with changes_requested succeeds",
			body:       `{"event_type": "signoff", "entity_deltas": [], "open_questions_delta": {}, "signoff_status": "changes_requested"}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "signoff with free text is rejected",
			body:       `{"event_type": "signoff", "entity_deltas": [], "open_questions_delta": {}, "signoff_status": "looks good to me"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			events := newFakeRevisionEventStore()
			sessionPathID := uuid.New().String()

			rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID, tc.body)

			assert.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
		})
	}
}

// TestAppendRevisionEventHandler_EntityDeltaDeleted_Returns400 proves FR2's
// entity_deltas.change enum rejects "deleted" -- M1's entity model has no
// delete/retire operation.
func TestAppendRevisionEventHandler_EntityDeltaDeleted_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID,
		`{"event_type": "ruling", "entity_deltas": [{"entity_id": "`+uuid.New().String()+`", "change": "deleted", "summary_line": "oops"}], "open_questions_delta": {}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestAppendRevisionEventHandler_UnknownEventType_Returns400 proves
// ParseEventType rejects a value outside the five-value enum before the
// store is ever called.
func TestAppendRevisionEventHandler_UnknownEventType_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID,
		`{"event_type": "musing", "entity_deltas": [], "open_questions_delta": {}}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, events.gotEvent.ScopeID, "an unrecognized event_type must never reach the store")
}

// TestAppendRevisionEventHandler_UnknownSessionID_Returns404 proves an
// append against an unknown design_session id (store.ErrNotFound, mirroring
// the real store's FOR UPDATE lock miss) is a 404, not a 400 or 500.
func TestAppendRevisionEventHandler_UnknownSessionID_Returns404(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	events := newFakeRevisionEventStore()
	events.appendErr = store.ErrNotFound
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID,
		`{"event_type": "ruling", "entity_deltas": [], "open_questions_delta": {}}`)

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestAppendRevisionEventHandler_Success_ReturnsIDAndAllocatedSeqNo proves
// the response shape: a 201 body with the new event's id and its
// store-allocated seq_no.
func TestAppendRevisionEventHandler_Success_ReturnsIDAndAllocatedSeqNo(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	events := newFakeRevisionEventStore()
	sessionPathID := uuid.New().String()

	rec := doAppendRevisionEventRequest(t, handlers.AppendRevisionEventHandler(events), sessions, sessionIDStr, sessionPathID,
		`{"event_type": "ruling", "entity_deltas": [], "open_questions_delta": {}}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp struct {
		ID    string `json:"id"`
		SeqNo int    `json:"seq_no"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.ID)
	assert.Equal(t, 1, resp.SeqNo)
}
