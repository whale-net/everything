// Unit tests for EscalateTaskHandler (task_escalate.go, issue #2872's
// Testing section, FR9): POST /tasks/{id}/escalate is gated like every
// other write endpoint (NFR6) -- session-derived scope_id/subject-pair
// pass-through, the path's {id} carried through as TaskID, the body's
// optional reason carried through, method enforcement, and
// writeStoreError's mapping of EscalateTask's own named rejections
// (store.ErrTaskEscalated, store.ErrTaskCancelled, store.ErrNotFound). No
// Postgres dependency -- fakeTaskStore (fake_task_store_test.go) stands in
// for store.TaskStore. Mirrors task_release_test.go's own shape.
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

// doEscalateTaskRequest mounts handler behind
// handlers.RequireSession(sessions) at "POST /probe/{id}/escalate" --
// mirroring doReleaseTaskRequest's own precedent (task_release_test.go).
func doEscalateTaskRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/escalate", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/escalate", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestEscalateTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doEscalateTaskRequest(t, handlers.EscalateTaskHandler(tasks, assembler), sessions, "", uuid.New().String(), `{}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotEscalateParams.ScopeID, "a request with no session header must never reach the store")
}

func TestEscalateTaskHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doEscalateTaskRequest(t, handlers.EscalateTaskHandler(tasks, assembler), sessions, sessionIDStr, "not-a-uuid", `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotEscalateParams.ScopeID, "a validation failure must never reach the store")
}

func TestEscalateTaskHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/escalate", nil)
	rec := httptest.NewRecorder()
	handlers.EscalateTaskHandler(tasks, assembler)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

func TestEscalateTaskHandler_ParamsPassThroughFromSessionAndBody(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doEscalateTaskRequest(t, handlers.EscalateTaskHandler(tasks, assembler), sessions, sessionIDStr, taskID.String(),
		`{"reason":"looks stuck, flagging for review"}`)

	assert.Equal(t, scopeID, tasks.gotEscalateParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotEscalateParams.TaskID, "TaskID must come from the path's {id}")
	gotReason := tasks.gotEscalateParams.Reason
	if assert.NotNil(t, gotReason) {
		assert.Equal(t, "looks stuck, flagging for review", *gotReason)
	}
	assert.NotEmpty(t, tasks.gotEscalateParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotEscalateParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

func TestEscalateTaskHandler_NoReason_Optional(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doEscalateTaskRequest(t, handlers.EscalateTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

	assert.Equal(t, scopeID, tasks.gotEscalateParams.ScopeID, "the store must be reached with an absent (optional) reason")
	assert.Nil(t, tasks.gotEscalateParams.Reason, "an absent reason must pass through as nil")
}

func TestEscalateTaskHandler_UnknownField_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doEscalateTaskRequest(t, handlers.EscalateTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
		`{"verdict":"pass"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotEscalateParams.ScopeID, "an unknown field must never reach the store")
}

// TestEscalateTaskHandler_StoreRejection_MappedToStatus proves
// writeStoreError maps EscalateTask's own named rejections onto the right
// status: ErrTaskEscalated (this task's own choice for an already-
// escalated task) and ErrTaskCancelled to 409, ErrNotFound to 400 -- never
// a 500 for any of them.
func TestEscalateTaskHandler_StoreRejection_MappedToStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"already escalated": {store.ErrTaskEscalated, http.StatusConflict},
		"cancelled":          {store.ErrTaskCancelled, http.StatusConflict},
		"not found":          {store.ErrNotFound, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{escalateErr: tc.err}
			assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

			rec := doEscalateTaskRequest(t, handlers.EscalateTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
