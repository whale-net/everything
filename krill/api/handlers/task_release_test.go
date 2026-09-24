// Unit tests for ReleaseTaskHandler (task_release.go, issue #2872's
// Testing section, FR8): POST /tasks/{id}/release is gated like every
// other write endpoint (NFR6) -- session-derived scope_id/subject-pair
// pass-through, the path's {id} carried through as TaskID, the body's
// optional reason carried through, method enforcement, and
// writeStoreError's mapping of ReleaseLease's own named rejections
// (store.ErrTaskNotClaimed, store.ErrTaskCancelled, store.ErrNotFound).
// No Postgres dependency -- fakeTaskStore (fake_task_store_test.go) stands
// in for store.TaskStore. Mirrors task_cancel_test.go's own shape.
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

// doReleaseTaskRequest mounts handler behind
// handlers.RequireSession(sessions) at "POST /probe/{id}/release" --
// mirroring doCancelTaskRequest's own precedent (task_cancel_test.go).
func doReleaseTaskRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/release", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/release", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestReleaseTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(tasks, assembler), sessions, "", uuid.New().String(), `{}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotReleaseParams.ScopeID, "a request with no session header must never reach the store")
}

func TestReleaseTaskHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(tasks, assembler), sessions, sessionIDStr, "not-a-uuid", `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotReleaseParams.ScopeID, "a validation failure must never reach the store")
}

func TestReleaseTaskHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/release", nil)
	rec := httptest.NewRecorder()
	handlers.ReleaseTaskHandler(tasks, assembler)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

func TestReleaseTaskHandler_ParamsPassThroughFromSessionAndBody(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(tasks, assembler), sessions, sessionIDStr, taskID.String(),
		`{"reason":"the claimant went dark"}`)

	assert.Equal(t, scopeID, tasks.gotReleaseParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotReleaseParams.TaskID, "TaskID must come from the path's {id}")
	gotReason := tasks.gotReleaseParams.Reason
	if assert.NotNil(t, gotReason) {
		assert.Equal(t, "the claimant went dark", *gotReason)
	}
	assert.NotEmpty(t, tasks.gotReleaseParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotReleaseParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

func TestReleaseTaskHandler_NoReason_Optional(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

	assert.Equal(t, scopeID, tasks.gotReleaseParams.ScopeID, "the store must be reached with an absent (optional) reason")
	assert.Nil(t, tasks.gotReleaseParams.Reason, "an absent reason must pass through as nil")
}

func TestReleaseTaskHandler_UnknownField_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
		`{"verdict":"pass"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotReleaseParams.ScopeID, "an unknown field must never reach the store")
}

// TestReleaseTaskHandler_StoreRejection_MappedToStatus proves
// writeStoreError maps ReleaseLease's own named rejections onto the right
// status: ErrTaskNotClaimed and ErrTaskCancelled to 409, ErrNotFound to
// 400 -- never a 500 for any of them.
func TestReleaseTaskHandler_StoreRejection_MappedToStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"not claimed": {store.ErrTaskNotClaimed, http.StatusConflict},
		"cancelled":   {store.ErrTaskCancelled, http.StatusConflict},
		"not found":   {store.ErrNotFound, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{releaseErr: tc.err}
			assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

			rec := doReleaseTaskRequest(t, handlers.ReleaseTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(), `{}`)

			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
