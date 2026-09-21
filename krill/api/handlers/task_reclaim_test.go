// Unit tests for ReclaimExpiredHandler (task_reclaim.go, issue #2724's
// Testing section): POST /tasks/reclaim is gated like every other write
// endpoint (FR7, NFR6) -- session-derived scope_id/subject-pair
// pass-through, an empty body sweeping the whole scope (TaskID nil),
// a body-named task_id passed through, method enforcement, request-body
// validation, and writeStoreError's mapping of ReclaimExpired's own
// named rejection (store.ErrNotFound) onto 400. No Postgres dependency --
// fakeTaskStore (fake_task_store_test.go) stands in for store.TaskStore;
// krill/store/task_reclaim_integration_test.go covers the real
// store-level branch coverage.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// doReclaimRequest mounts handler behind handlers.RequireSession(sessions)
// at "POST /probe/reclaim" -- exactly like routes.go's own wiring
// ("POST /tasks/reclaim"), mirroring doHeartbeatRequest's precedent.
func doReclaimRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/reclaim", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/reclaim", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestReclaimExpiredHandler_NoSessionHeader_Rejected proves NFR6's write
// gate: POST /tasks/reclaim with no X-Krill-Session-Id header is rejected
// before the store is ever called.
func TestReclaimExpiredHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doReclaimRequest(t, handlers.ReclaimExpiredHandler(tasks), sessions, "", "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotReclaimParams.ScopeID, "a request with no session header must never reach the store")
}

// TestReclaimExpiredHandler_InvalidTaskID_Returns400 proves a malformed
// task_id in the request body is rejected before ReclaimExpired is ever
// called.
func TestReclaimExpiredHandler_InvalidTaskID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doReclaimRequest(t, handlers.ReclaimExpiredHandler(tasks), sessions, sessionIDStr, `{"task_id":"not-a-uuid"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotReclaimParams.ScopeID, "a validation failure must never reach the store")
}

// TestReclaimExpiredHandler_WrongMethod_Returns405 proves GET (or any verb
// other than POST) against this handler is rejected.
func TestReclaimExpiredHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/probe/reclaim", nil)
	rec := httptest.NewRecorder()
	handlers.ReclaimExpiredHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestReclaimExpiredHandler_EmptyBody_SweepsWholeScope proves an
// empty/absent request body sweeps the caller's whole scope: TaskID is
// passed through to the store as nil, and scope_id/both subjects come
// from the session (NFR6).
func TestReclaimExpiredHandler_EmptyBody_SweepsWholeScope(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doReclaimRequest(t, handlers.ReclaimExpiredHandler(tasks), sessions, sessionIDStr, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotReclaimParams.ScopeID, "NFR6: scope_id must come from the session")
	assert.Nil(t, tasks.gotReclaimParams.TaskID, "an empty body must sweep the whole scope -- TaskID must be nil")
	assert.NotEmpty(t, tasks.gotReclaimParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotReclaimParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestReclaimExpiredHandler_TaskIDInBody_PassedThroughAndNamedInResponse
// proves a task_id in the request body is passed through as
// ReclaimParams.TaskID, and that the response names both a reclaimed task
// and a cap-exhausted one distinctly -- including, for the cap-exhausted
// one, the escalation_id/escalation_reason issue #2871 (FR3) adds
// additively to ReclaimedTaskResponse.
func TestReclaimExpiredHandler_TaskIDInBody_PassedThroughAndNamedInResponse(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	reclaimedID := uuid.New()
	capExhaustedID := uuid.New()
	escalationID := uuid.New()
	escalationReason := store.EscalationReasonAttemptCap
	tasks := &fakeTaskStore{reclaimResult: store.ReclaimResult{
		Reclaimed: []store.ReclaimedTask{
			{TaskID: reclaimedID, CapExhausted: false},
			{TaskID: capExhaustedID, CapExhausted: true, EscalationID: &escalationID, EscalationReason: &escalationReason},
		},
	}}

	rec := doReclaimRequest(t, handlers.ReclaimExpiredHandler(tasks), sessions, sessionIDStr, `{"task_id":"`+taskID.String()+`"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, tasks.gotReclaimParams.TaskID)
	assert.Equal(t, taskID, *tasks.gotReclaimParams.TaskID, "task_id in the body must be passed through to the store")

	body := rec.Body.String()
	assert.Contains(t, body, reclaimedID.String())
	assert.Contains(t, body, capExhaustedID.String())
	assert.Contains(t, body, `"cap_exhausted":false`)
	assert.Contains(t, body, `"cap_exhausted":true`)
	assert.Contains(t, body, escalationID.String(), "the cap-exhausted task's response must name the escalation event it wrote")
	assert.Contains(t, body, `"escalation_reason":"attempt-cap"`)
}

// TestReclaimExpiredHandler_UnknownTaskID_Returns400 proves writeStoreError
// maps ReclaimExpired's own named rejection (store.ErrNotFound, for an
// unknown or cross-scope task_id) onto a 400.
func TestReclaimExpiredHandler_UnknownTaskID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{reclaimErr: store.ErrNotFound}

	rec := doReclaimRequest(t, handlers.ReclaimExpiredHandler(tasks), sessions, sessionIDStr, `{"task_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestReclaimExpiredHandler_TaskEscalated_Returns409 proves writeStoreError
// maps store.ErrTaskEscalated -- the error recordEscalationTx (via this
// same reclaim) returns when the task already carries an active
// escalation (issue #2871's own same-transaction rollback proof) -- onto
// 409, not the default 500.
func TestReclaimExpiredHandler_TaskEscalated_Returns409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{reclaimErr: store.ErrTaskEscalated}

	rec := doReclaimRequest(t, handlers.ReclaimExpiredHandler(tasks), sessions, sessionIDStr, "")

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}
