// Unit tests for TransitionNoteLifecycleHandler (task_note_lifecycle.go,
// issue #2874's Testing section, FR11): POST /notes/{id}/lifecycle is
// gated behind handlers.RequireSession (NFR6) like every other write
// endpoint in this package -- scope_id and both subjects come from the
// session, never the request body -- mirroring task_cancel_test.go's own
// gated-write pattern for the sibling M5 shape. fakeTaskStore
// (fake_task_store_test.go) stands in for store.TaskStore, so none of this
// needs Postgres; krill/store/task_note_lifecycle_integration_test.go
// covers the real store-level branch coverage.
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

// doTransitionNoteLifecycleRequest mounts handler behind
// handlers.RequireSession(sessions) at "POST /probe/{id}/lifecycle" --
// mirroring doCancelTaskRequest's own precedent (task_cancel_test.go).
func doTransitionNoteLifecycleRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/lifecycle", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/lifecycle", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestTransitionNoteLifecycleHandler_NoSessionHeader_Rejected proves
// NFR6's write gate: POST /notes/{id}/lifecycle with no
// X-Krill-Session-Id header is rejected before the store is ever called --
// this handler is open to any resolved persona once gated, never an
// operator-only endpoint, but the session gate itself still applies.
func TestTransitionNoteLifecycleHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doTransitionNoteLifecycleRequest(t, handlers.TransitionNoteLifecycleHandler(tasks), sessions, "", uuid.New().String(), `{"status":"deferred"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotTransitionNoteLifecycleParams.ScopeID, "a request with no session header must never reach the store")
}

// TestTransitionNoteLifecycleHandler_InvalidID_Returns400 proves a
// malformed {id} is rejected before TransitionNoteLifecycle is ever
// called.
func TestTransitionNoteLifecycleHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doTransitionNoteLifecycleRequest(t, handlers.TransitionNoteLifecycleHandler(tasks), sessions, sessionIDStr, "not-a-uuid", `{"status":"deferred"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotTransitionNoteLifecycleParams.ScopeID, "a validation failure must never reach the store")
}

// TestTransitionNoteLifecycleHandler_WrongMethod_Returns405 proves GET (or
// any verb other than POST) against this handler is rejected.
func TestTransitionNoteLifecycleHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/lifecycle", nil)
	rec := httptest.NewRecorder()
	handlers.TransitionNoteLifecycleHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestTransitionNoteLifecycleHandler_ParamsPassThrough proves NFR6/NFR3:
// scope_id and both LB4 subjects come from the session, NoteID from the
// path's {id}, and Status from the request body.
func TestTransitionNoteLifecycleHandler_ParamsPassThrough(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	noteID := uuid.New()
	eventID := uuid.New()
	tasks := &fakeTaskStore{transitionNoteLifecycleResult: store.NoteLifecycleEvent{ID: eventID, NoteID: noteID, Status: store.NoteLifecycleStatusDeferred}}

	rec := doTransitionNoteLifecycleRequest(t, handlers.TransitionNoteLifecycleHandler(tasks), sessions, sessionIDStr, noteID.String(), `{"status":"deferred"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotTransitionNoteLifecycleParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, noteID, tasks.gotTransitionNoteLifecycleParams.NoteID, "NoteID must come from the path's {id}")
	assert.Equal(t, store.NoteLifecycleStatusDeferred, tasks.gotTransitionNoteLifecycleParams.Status)
	assert.NotEmpty(t, tasks.gotTransitionNoteLifecycleParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotTransitionNoteLifecycleParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestTransitionNoteLifecycleHandler_UnknownField_Rejected proves
// decodeStrict's unknown-field check applies to this body too, mirroring
// every other write endpoint's strict decode.
func TestTransitionNoteLifecycleHandler_UnknownField_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doTransitionNoteLifecycleRequest(t, handlers.TransitionNoteLifecycleHandler(tasks), sessions, sessionIDStr, uuid.New().String(), `{"status":"deferred","body":"nope"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotTransitionNoteLifecycleParams.ScopeID, "an unknown field must never reach the store")
}

// TestTransitionNoteLifecycleHandler_StoreRejection_MappedToStatus proves
// writeNoteLifecycleStoreError maps TransitionNoteLifecycle's own named
// rejections onto 400 -- never a 500 for either.
func TestTransitionNoteLifecycleHandler_StoreRejection_MappedToStatus(t *testing.T) {
	for name, err := range map[string]error{
		"unknown status":                store.ErrUnknownNoteLifecycleStatus,
		"unknown or cross-scope note id": store.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{transitionNoteLifecycleErr: err}

			rec := doTransitionNoteLifecycleRequest(t, handlers.TransitionNoteLifecycleHandler(tasks), sessions, sessionIDStr, uuid.New().String(), `{"status":"deferred"}`)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}
