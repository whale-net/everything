// Unit tests for RequeueTaskHandler (task_requeue.go, issue #2876's
// Testing section, FR6): POST /tasks/{id}/requeue is gated like every
// other write endpoint (NFR6) -- session-derived scope_id/subject-pair
// pass-through, the path's {id} carried through as TaskID, the body's
// optional reason carried through, method enforcement, and
// writeRequeueStoreError's mapping of RequeueTask's own named rejections
// (store.ErrTaskNotEscalated, store.ErrTaskCancelled, store.ErrNotFound).
// No Postgres dependency -- fakeTaskStore (fake_task_store_test.go) stands
// in for store.TaskStore. Mirrors task_cancel_test.go's own shape for the
// sibling FR7 endpoint.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// doRequeueTaskRequest mounts handler behind
// handlers.RequireSession(sessions) at "POST /probe/{id}/requeue" --
// mirroring doCancelTaskRequest's own precedent (task_cancel_test.go).
func doRequeueTaskRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/requeue", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/requeue", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestRequeueTaskHandler_NoSessionHeader_Rejected proves NFR6's write
// gate: POST /tasks/{id}/requeue with no X-Krill-Session-Id header is
// rejected before the store is ever called.
func TestRequeueTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doRequeueTaskRequest(t, handlers.RequeueTaskHandler(tasks, assembler), sessions, "", uuid.New().String(), `{}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotRequeueParams.ScopeID, "a request with no session header must never reach the store")
}

// TestRequeueTaskHandler_InvalidID_Returns400 proves a malformed {id} is
// rejected before RequeueTask is ever called.
func TestRequeueTaskHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doRequeueTaskRequest(t, handlers.RequeueTaskHandler(tasks, assembler), sessions, sessionIDStr, "not-a-uuid", `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotRequeueParams.ScopeID, "a validation failure must never reach the store")
}

// TestRequeueTaskHandler_WrongMethod_Returns405 proves GET (or any verb
// other than POST) against this handler is rejected.
func TestRequeueTaskHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/requeue", nil)
	rec := httptest.NewRecorder()
	handlers.RequeueTaskHandler(tasks, assembler)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestRequeueTaskHandler_ParamsPassThroughFromSessionAndBody proves NFR6/
// NFR3: scope_id and both LB4 subjects come from the session, TaskID from
// the path's {id}, and Reason from the request body.
func TestRequeueTaskHandler_ParamsPassThroughFromSessionAndBody(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doRequeueTaskRequest(t, handlers.RequeueTaskHandler(tasks, assembler), sessions, sessionIDStr, taskID.String(),
		`{"reason":"investigated, safe to resume"}`)

	assert.Equal(t, scopeID, tasks.gotRequeueParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotRequeueParams.TaskID, "TaskID must come from the path's {id}")
	gotReason := tasks.gotRequeueParams.Reason
	if assert.NotNil(t, gotReason) {
		assert.Equal(t, "investigated, safe to resume", *gotReason)
	}
	assert.NotEmpty(t, tasks.gotRequeueParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotRequeueParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestRequeueTaskHandler_NoReason_Optional proves Reason is optional: an
// absent "reason" field passes through as nil, not a validation failure --
// the store is still reached.
func TestRequeueTaskHandler_NoReason_Optional(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doRequeueTaskRequest(t, handlers.RequeueTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

	assert.Equal(t, scopeID, tasks.gotRequeueParams.ScopeID, "the store must be reached with an absent (optional) reason")
	assert.Nil(t, tasks.gotRequeueParams.Reason, "an absent reason must pass through as nil")
}

// TestRequeueTaskHandler_UnknownField_Rejected proves decodeStrict's
// unknown-field check applies to this body too, mirroring every other
// write endpoint's strict decode.
func TestRequeueTaskHandler_UnknownField_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doRequeueTaskRequest(t, handlers.RequeueTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
		`{"verdict":"pass"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotRequeueParams.ScopeID, "an unknown field must never reach the store")
}

// TestRequeueTaskHandler_StoreRejection_MappedToStatus proves
// writeRequeueStoreError maps RequeueTask's own named rejections onto the
// right status: ErrTaskNotEscalated and ErrTaskCancelled to 409,
// ErrNotFound to 400 -- never a 500 for any of them.
func TestRequeueTaskHandler_StoreRejection_MappedToStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"not escalated": {store.ErrTaskNotEscalated, http.StatusConflict},
		"cancelled":     {store.ErrTaskCancelled, http.StatusConflict},
		"not found":     {store.ErrNotFound, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{requeueErr: tc.err}
			assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

			rec := doRequeueTaskRequest(t, handlers.RequeueTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
