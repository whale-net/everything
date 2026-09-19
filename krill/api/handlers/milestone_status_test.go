// Unit tests for SetMilestoneStatusHandler/GetMilestoneStatusHandler/
// GetMilestoneStatusHistoryHandler (milestone_status.go, issue #2685's
// Testing section): POST /milestones/{id}/status is gated like every
// other write endpoint (FR3) -- NFR4's "a handler call without a session
// is 401 and writes nothing" -- while the two GET endpoints are ungated
// reads. No Postgres dependency -- fakeMilestoneStatusStore
// (fake_milestone_status_store_test.go) stands in for
// store.MilestoneStatusEventStore (krill/store/
// milestone_status_integration_test.go covers the real store behavior
// against Postgres).
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// doMilestoneStatusPostRequest mounts handler behind
// handlers.RequireSession(sessions) at "{id}/status" -- exactly like
// routes.go's "POST /milestones/{id}/status" -- so r.PathValue("id") is
// populated the same way a real request would populate it.
// sessionIDHeader == "" omits the X-Krill-Session-Id header entirely (the
// "no session id" rejection case). method lets a test drive a non-POST
// verb at the same path (proving no other verb reaches this handler).
func doMilestoneStatusPostRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, method, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/status", gated)

	req := httptest.NewRequest(method, "/probe/"+id+"/status", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// doMilestoneStatusGetRequest drives one of the two ungated GET endpoints
// directly (no RequireSession wrapping, mirroring routes.go), with
// r.PathValue("id") populated via a bare mux mount. pattern/requestPath
// let a test drive either "GET /probe/{id}/status" or
// "GET /probe/{id}/status/history" against the same helper.
func doMilestoneStatusGetRequest(t *testing.T, handler http.HandlerFunc, pattern, requestPath string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc(pattern, handler)

	req := httptest.NewRequest(http.MethodGet, requestPath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestSetMilestoneStatusHandler_NoSessionID_Rejected401_NoStoreCall is
// issue #2685's Testing item 7's handler-level half (NFR4): a request with
// no X-Krill-Session-Id header is rejected with 401 by the write gate
// (FR3) before it ever reaches RecordTransition -- the LB4 subject pair
// always comes from a real session, never a caller-supplied field, and an
// unauthenticated caller writes nothing.
func TestSetMilestoneStatusHandler_NoSessionID_Rejected401_NoStoreCall(t *testing.T) {
	sessions := newFakeSessionStore()
	statuses := &fakeMilestoneStatusStore{}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, "", id.String(), http.MethodPost,
		`{"status": "planned"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, statuses.recordCalled, "an ungated request must never reach RecordTransition -- NFR4/FR3")
}

// TestSetMilestoneStatusHandler_ValidSession_RecordsTransitionWithPathIDAndSubjects
// proves the success path: a valid session lets the request through, and
// the handler passes the path's {id}, the body's status/note, and the
// session's scope/subject pair to RecordTransition unchanged.
func TestSetMilestoneStatusHandler_ValidSession_RecordsTransitionWithPathIDAndSubjects(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "planned", "note": "kicking off"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.True(t, statuses.recordCalled)
	assert.Equal(t, scopeID, statuses.gotScopeID)
	assert.Equal(t, id, statuses.gotMilestoneID)
	assert.Equal(t, store.MilestoneStatusPlanned, statuses.gotStatus)
	require.NotNil(t, statuses.gotNote)
	assert.Equal(t, "kicking off", *statuses.gotNote)

	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
}

// TestSetMilestoneStatusHandler_InvalidStatus_Returns400_NoStoreCall
// proves FR8's fixed seven-value set is enforced before RecordTransition
// is ever called -- an eighth, made-up value is a 400, not a store call
// (the DB CHECK, migration 012, is the other, store-level half of this
// same guarantee -- see krill/store/
// milestone_status_integration_test.go's TestMilestoneStatusStore_
// AllSevenStatuses_EighthRejectedByDBCheck).
func TestSetMilestoneStatusHandler_InvalidStatus_Returns400_NoStoreCall(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "bogus-status"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, statuses.recordCalled, "an invalid status must never reach RecordTransition")
}

// TestSetMilestoneStatusHandler_WrongMethod_Returns405_NoStoreCall is
// part of issue #2685's NFR2 structural proof: doMilestoneStatusPostRequest
// mounts SetMilestoneStatusHandler behind the exact single-method pattern
// routes.go uses ("POST /milestones/{id}/status", no PUT/PATCH/DELETE
// pattern registered anywhere alongside it) -- so a PUT, PATCH, or DELETE
// at this path (the verbs an update-or-delete path would use) never even
// reaches the handler, and SetMilestoneStatusHandler's own belt-and-braces
// r.Method check (milestone_status.go) backs that up a second way.
// Neither ever dispatches to RecordTransition.
func TestSetMilestoneStatusHandler_WrongMethod_Returns405_NoStoreCall(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			statuses := &fakeMilestoneStatusStore{}
			id := uuid.New()

			rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), method,
				`{"status": "planned"}`)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "milestone_status_event has no update/delete path (NFR2) -- %s must never be accepted", method)
			assert.False(t, statuses.recordCalled)
		})
	}
}

// TestGetMilestoneStatusHandler_ReturnsCurrentStatus proves the ungated
// read endpoint reports whatever CurrentStatus derives.
func TestGetMilestoneStatusHandler_ReturnsCurrentStatus(t *testing.T) {
	statuses := &fakeMilestoneStatusStore{currentStatus: store.MilestoneStatusShipped}
	id := uuid.New()

	rec := doMilestoneStatusGetRequest(t, handlers.GetMilestoneStatusHandler(statuses), "GET /probe/{id}/status", "/probe/"+id.String()+"/status")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "shipped", resp.Status)
}

// TestGetMilestoneStatusHistoryHandler_ReturnsChronologicalTransitions
// proves the ungated history endpoint (FR12) serializes ListTransitions'
// answer, in order, with each entry's subject pair and timestamp.
func TestGetMilestoneStatusHistoryHandler_ReturnsChronologicalTransitions(t *testing.T) {
	id := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "human-1", Kind: store.SubjectKindHuman}
	now := time.Now().UTC()

	statuses := &fakeMilestoneStatusStore{transitions: []store.MilestoneStatusEvent{
		{ID: uuid.New(), MilestoneID: id, Status: store.MilestoneStatusPlanned, CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf, CreatedAt: now},
		{ID: uuid.New(), MilestoneID: id, Status: store.MilestoneStatusInProgress, CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf, CreatedAt: now.Add(time.Minute)},
		{ID: uuid.New(), MilestoneID: id, Status: store.MilestoneStatusShipped, CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf, CreatedAt: now.Add(2 * time.Minute)},
	}}

	rec := doMilestoneStatusGetRequest(t, handlers.GetMilestoneStatusHistoryHandler(statuses), "GET /probe/{id}/status/history", "/probe/"+id.String()+"/status/history")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp handlers.MilestoneStatusHistoryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Transitions, 3)
	assert.Equal(t, "planned", resp.Transitions[0].Status)
	assert.Equal(t, "in progress", resp.Transitions[1].Status)
	assert.Equal(t, "shipped", resp.Transitions[2].Status)
	for _, tr := range resp.Transitions {
		assert.Equal(t, "agent-1", tr.Acting.Sub)
		assert.Equal(t, "human-1", tr.OnBehalfOf.Sub)
	}
}
