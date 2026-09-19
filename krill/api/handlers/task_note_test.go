// Unit tests for RecordNoteHandler/ListTaskNotesHandler (task_note.go,
// issue #2727's Testing section): POST /notes is gated behind
// handlers.RequireSession (NFR6) like every other write endpoint in this
// package -- scope_id and both subjects come from the session, never the
// request body -- while GET /tasks/{id}/notes is an ungated read that
// resolves scope from the task itself, mirroring
// task_dependency_test.go's own gated-write/ungated-read pair for the
// same TaskStore-widening shape. fakeTaskStore (fake_task_store_test.go)
// stands in for store.TaskStore, so none of this needs Postgres;
// krill/store/task_note_integration_test.go covers the real store-level
// branch coverage, including the note round trip that actually appears in
// a real assembled GET /tasks/{id} payload.
package handlers_test

import (
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

// doTaskNotesGetRequest drives the ungated GET endpoint directly, with
// r.PathValue("id") populated via a bare mux mount, mirroring
// doTaskDependenciesGetRequest's precedent.
func doTaskNotesGetRequest(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/{id}/notes", handler)

	req := httptest.NewRequest(http.MethodGet, "/probe/"+id+"/notes", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestRecordNoteHandler_Success_TaskTarget proves FR11/NFR6: a
// well-formed task-targeted note passes task_id/kind/body through
// unchanged, and writes scope_id and both LB4 subjects from the session,
// never the request body.
func TestRecordNoteHandler_Success_TaskTarget(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	noteID := uuid.New()

	tasks := &fakeTaskStore{recordNoteResult: store.Note{ID: noteID, TaskID: &taskID}}
	rec := doGatedRequest(t, handlers.RecordNoteHandler(tasks), sessions, sessionIDStr,
		`{"task_id": "`+taskID.String()+`", "kind": "comment", "body": "hello"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, noteID.String(), resp.ID)

	assert.Equal(t, scopeID, tasks.gotRecordNoteParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	require.NotNil(t, tasks.gotRecordNoteParams.TaskID)
	assert.Equal(t, taskID, *tasks.gotRecordNoteParams.TaskID)
	assert.Nil(t, tasks.gotRecordNoteParams.EntityKind, "a task-targeted note must never also carry an entity target")
	assert.Equal(t, store.NoteKind("comment"), tasks.gotRecordNoteParams.Kind)
	assert.Equal(t, "hello", tasks.gotRecordNoteParams.Body)
	assert.NotEmpty(t, tasks.gotRecordNoteParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotRecordNoteParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestRecordNoteHandler_Success_EntityTarget proves the entity-targeting
// half of the same request shape: entity_kind/entity_id pass through
// unchanged and task_id stays nil.
func TestRecordNoteHandler_Success_EntityTarget(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	entityID := uuid.New()
	noteID := uuid.New()

	tasks := &fakeTaskStore{recordNoteResult: store.Note{ID: noteID}}
	rec := doGatedRequest(t, handlers.RecordNoteHandler(tasks), sessions, sessionIDStr,
		`{"entity_kind": "requirement", "entity_id": "`+entityID.String()+`", "kind": "scope-note", "body": "noticed drift"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotRecordNoteParams.ScopeID)
	assert.Nil(t, tasks.gotRecordNoteParams.TaskID, "an entity-targeted note must never also carry a task_id")
	require.NotNil(t, tasks.gotRecordNoteParams.EntityKind)
	assert.Equal(t, store.NoteEntityKind("requirement"), *tasks.gotRecordNoteParams.EntityKind)
	require.NotNil(t, tasks.gotRecordNoteParams.EntityID)
	assert.Equal(t, entityID, *tasks.gotRecordNoteParams.EntityID)
	assert.Equal(t, store.NoteKind("scope-note"), tasks.gotRecordNoteParams.Kind)
}

// TestRecordNoteHandler_StoreRejection_Returns400 proves writeNoteStoreError
// maps RecordNote's named FR11/FR12 rejections -- both/neither target,
// empty body, unknown note kind, unknown entity kind -- and store.ErrNotFound
// (NFR1's cross-scope/unknown-target rejection) onto a 400.
func TestRecordNoteHandler_StoreRejection_Returns400(t *testing.T) {
	for name, err := range map[string]error{
		"invalid target (both/neither)": store.ErrInvalidNoteTarget,
		"empty body":                    store.ErrEmptyNoteBody,
		"unknown note kind":             store.ErrUnknownNoteKind,
		"unknown entity kind":           store.ErrUnknownNoteEntityKind,
		"unknown or cross-scope target": store.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{recordNoteErr: err}

			rec := doGatedRequest(t, handlers.RecordNoteHandler(tasks), sessions, sessionIDStr,
				`{"task_id": "`+uuid.New().String()+`", "kind": "comment", "body": "x"}`)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// TestRecordNoteHandler_NoSessionHeader_Rejected proves NFR6's write gate:
// POST /notes with no X-Krill-Session-Id header is rejected before the
// body is ever parsed or the store is ever called.
func TestRecordNoteHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doGatedRequest(t, handlers.RecordNoteHandler(tasks), sessions, "",
		`{"task_id": "`+uuid.New().String()+`", "kind": "comment", "body": "x"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotRecordNoteParams.ScopeID, "a request with no session header must never reach the store")
}

// TestListTaskNotesHandler_Success proves the ungated read resolves
// {id}'s own scope via GetTaskByID before calling ListNotesForTask, and
// passes the recorded notes through unchanged.
func TestListTaskNotesHandler_Success(t *testing.T) {
	taskID := uuid.New()
	scopeID := uuid.New()
	noteID := uuid.New()

	tasks := &fakeTaskStore{
		getTaskByIDResult: store.Task{ID: taskID, ScopeID: scopeID},
		notesForTask: []store.Note{
			{ID: noteID, TaskID: &taskID, Kind: store.NoteKindComment, Body: "hello"},
		},
	}

	rec := doTaskNotesGetRequest(t, handlers.ListTaskNotesHandler(tasks), taskID.String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Notes []struct {
			ID     string `json:"id"`
			TaskID string `json:"task_id"`
			Kind   string `json:"kind"`
			Body   string `json:"body"`
		} `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Notes, 1)
	assert.Equal(t, taskID.String(), resp.Notes[0].TaskID)
	assert.Equal(t, "comment", resp.Notes[0].Kind)
	assert.Equal(t, "hello", resp.Notes[0].Body)
}

// TestListTaskNotesHandler_TaskNotFound_Returns404 proves an unknown task
// id is reported as 404, never a 500 or an empty 200.
func TestListTaskNotesHandler_TaskNotFound_Returns404(t *testing.T) {
	tasks := &fakeTaskStore{getTaskByIDErr: store.ErrNotFound}

	rec := doTaskNotesGetRequest(t, handlers.ListTaskNotesHandler(tasks), uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
