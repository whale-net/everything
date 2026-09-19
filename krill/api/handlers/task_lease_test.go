// Unit tests for HeartbeatHandler (task_lease.go, issue #2723's Testing
// section): POST /tasks/{id}/heartbeat is gated like every other write
// endpoint (FR6, NFR6) -- session-derived scope_id/subject-pair
// pass-through, the request body's claim_id carried through as ClaimID,
// method enforcement, request-body validation, and writeStoreError's
// mapping of Heartbeat's own named FR6 rejection (store.ErrClaimNotCurrent)
// onto 409. No Postgres dependency -- fakeTaskStore (fake_task_store_test.go)
// stands in for store.TaskStore; krill/store/task_lease_integration_test.go
// covers the real store-level branch coverage.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// doHeartbeatRequest mounts handler behind
// handlers.RequireSession(sessions) at "POST /probe/{id}/heartbeat" --
// exactly like routes.go's own wiring -- mirroring doClaimTaskRequest's
// precedent.
func doHeartbeatRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/heartbeat", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/heartbeat", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestHeartbeatHandler_NoSessionHeader_Rejected proves NFR6's write gate:
// POST /tasks/{id}/heartbeat with no X-Krill-Session-Id header is rejected
// before the store is ever called.
func TestHeartbeatHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doHeartbeatRequest(t, handlers.HeartbeatHandler(tasks), sessions, "", uuid.New().String(), `{"claim_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotHeartbeatParams.ScopeID, "a request with no session header must never reach the store")
}

// TestHeartbeatHandler_InvalidID_Returns400 proves a malformed {id} is
// rejected before Heartbeat is ever called.
func TestHeartbeatHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doHeartbeatRequest(t, handlers.HeartbeatHandler(tasks), sessions, sessionIDStr, "not-a-uuid", `{"claim_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotHeartbeatParams.ScopeID, "a validation failure must never reach the store")
}

// TestHeartbeatHandler_InvalidClaimID_Returns400 proves a malformed or
// missing claim_id in the request body is rejected before Heartbeat is
// ever called.
func TestHeartbeatHandler_InvalidClaimID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doHeartbeatRequest(t, handlers.HeartbeatHandler(tasks), sessions, sessionIDStr, uuid.New().String(), `{"claim_id":"not-a-uuid"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotHeartbeatParams.ScopeID, "a validation failure must never reach the store")
}

// TestHeartbeatHandler_WrongMethod_Returns405 proves GET (or any verb
// other than POST) against this handler is rejected.
func TestHeartbeatHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/heartbeat", nil)
	rec := httptest.NewRecorder()
	handlers.HeartbeatHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestHeartbeatHandler_ParamsPassThroughFromSessionPathAndBody proves
// NFR6/NFR3: scope_id and both LB4 subjects come from the session,
// TaskID from the path's {id}, and ClaimID from the request body -- and
// that a successful Heartbeat returns 200 with the lease's wire shape.
func TestHeartbeatHandler_ParamsPassThroughFromSessionPathAndBody(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	claimID := uuid.New()
	extendedTo := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	tasks := &fakeTaskStore{heartbeatResult: store.LeaseState{TaskID: taskID, ClaimID: claimID, ExtendedTo: extendedTo}}

	rec := doHeartbeatRequest(t, handlers.HeartbeatHandler(tasks), sessions, sessionIDStr, taskID.String(), `{"claim_id":"`+claimID.String()+`"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotHeartbeatParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotHeartbeatParams.TaskID, "TaskID must come from the path's {id}")
	assert.Equal(t, claimID, tasks.gotHeartbeatParams.ClaimID, "ClaimID must come from the request body")
	assert.NotEmpty(t, tasks.gotHeartbeatParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotHeartbeatParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
	assert.Contains(t, rec.Body.String(), claimID.String())
	assert.Contains(t, rec.Body.String(), taskID.String())
}

// TestHeartbeatHandler_ClaimNotCurrent_Returns409 proves writeStoreError
// maps Heartbeat's own named FR6 rejection (store.ErrClaimNotCurrent) onto
// a 409 -- distinct from both a 400 (bad request shape) and a 500 (a
// genuine store failure), so a caller can tell "your claim is gone" from
// "krill is broken".
func TestHeartbeatHandler_ClaimNotCurrent_Returns409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{heartbeatErr: store.ErrClaimNotCurrent}

	rec := doHeartbeatRequest(t, handlers.HeartbeatHandler(tasks), sessions, sessionIDStr, uuid.New().String(), `{"claim_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}
