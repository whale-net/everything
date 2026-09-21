// Unit tests for CancelTaskHandler (task_cancel.go, issue #2873's Testing
// section, FR7): POST /tasks/{id}/cancel is gated like every other write
// endpoint (NFR6) -- session-derived scope_id/subject-pair pass-through,
// the path's {id} carried through as TaskID, the body's optional reason
// carried through, method enforcement, and writeCancelStoreError's
// mapping of CancelTask's own named rejections (store.ErrTaskAlreadyCancelled,
// store.ErrNotFound). No Postgres dependency -- fakeTaskStore
// (fake_task_store_test.go) stands in for store.TaskStore.
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

// doCancelTaskRequest mounts handler behind handlers.RequireSession(sessions)
// at "POST /probe/{id}/cancel" -- mirroring doAbandonTaskRequest's own
// precedent (task_abandon_test.go).
func doCancelTaskRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/cancel", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/cancel", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestCancelTaskHandler_NoSessionHeader_Rejected proves NFR6's write gate:
// POST /tasks/{id}/cancel with no X-Krill-Session-Id header is rejected
// before the store is ever called.
func TestCancelTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doCancelTaskRequest(t, handlers.CancelTaskHandler(tasks, assembler), sessions, "", uuid.New().String(), `{}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotCancelParams.ScopeID, "a request with no session header must never reach the store")
}

// TestCancelTaskHandler_InvalidID_Returns400 proves a malformed {id} is
// rejected before CancelTask is ever called.
func TestCancelTaskHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doCancelTaskRequest(t, handlers.CancelTaskHandler(tasks, assembler), sessions, sessionIDStr, "not-a-uuid", `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotCancelParams.ScopeID, "a validation failure must never reach the store")
}

// TestCancelTaskHandler_WrongMethod_Returns405 proves GET (or any verb
// other than POST) against this handler is rejected.
func TestCancelTaskHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/cancel", nil)
	rec := httptest.NewRecorder()
	handlers.CancelTaskHandler(tasks, assembler)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestCancelTaskHandler_ParamsPassThroughFromSessionAndBody proves NFR6/
// NFR3: scope_id and both LB4 subjects come from the session, TaskID from
// the path's {id}, and Reason from the request body.
func TestCancelTaskHandler_ParamsPassThroughFromSessionAndBody(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doCancelTaskRequest(t, handlers.CancelTaskHandler(tasks, assembler), sessions, sessionIDStr, taskID.String(),
		`{"reason":"superseded by a different task"}`)

	assert.Equal(t, scopeID, tasks.gotCancelParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotCancelParams.TaskID, "TaskID must come from the path's {id}")
	gotReason := tasks.gotCancelParams.Reason
	if assert.NotNil(t, gotReason) {
		assert.Equal(t, "superseded by a different task", *gotReason)
	}
	assert.NotEmpty(t, tasks.gotCancelParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotCancelParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestCancelTaskHandler_NoReason_Optional proves Reason is optional: an
// absent "reason" field passes through as nil, not a validation failure --
// the store is still reached.
func TestCancelTaskHandler_NoReason_Optional(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doCancelTaskRequest(t, handlers.CancelTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

	assert.Equal(t, scopeID, tasks.gotCancelParams.ScopeID, "the store must be reached with an absent (optional) reason")
	assert.Nil(t, tasks.gotCancelParams.Reason, "an absent reason must pass through as nil")
}

// TestCancelTaskHandler_UnknownField_Rejected proves decodeStrict's
// unknown-field check applies to this body too, mirroring every other
// write endpoint's strict decode.
func TestCancelTaskHandler_UnknownField_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doCancelTaskRequest(t, handlers.CancelTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
		`{"verdict":"pass"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotCancelParams.ScopeID, "an unknown field must never reach the store")
}

// TestCancelTaskHandler_StoreRejection_MappedToStatus proves
// writeCancelStoreError maps CancelTask's own named rejections onto the
// right status: ErrTaskAlreadyCancelled to 409, ErrNotFound to 400 --
// never a 500 for either.
func TestCancelTaskHandler_StoreRejection_MappedToStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"already cancelled": {store.ErrTaskAlreadyCancelled, http.StatusConflict},
		"not found":         {store.ErrNotFound, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{cancelErr: tc.err}
			assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

			rec := doCancelTaskRequest(t, handlers.CancelTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
