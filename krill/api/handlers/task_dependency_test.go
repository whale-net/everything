// Unit tests for DeclareTaskDependenciesHandler/ListTaskDependenciesHandler
// (task_dependency.go, issue #2720's Testing section): POST
// /tasks/{id}/dependencies is gated like every other write endpoint (FR2,
// NFR6) -- session-derived scope_id/subject-pair pass-through, the path's
// {id} unchanged as TaskID, field validation, and DeclareDependency's
// named FR2 rejections mapped to 400 -- while GET /tasks/{id}/dependencies
// is an ungated read that resolves scope from the task itself. No
// Postgres dependency -- fakeTaskStore (fake_task_store_test.go) stands in
// for store.TaskStore; krill/store/task_dependency_integration_test.go
// covers the real store-level branch coverage.
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

// doTaskDependenciesPostRequest mounts handler behind
// handlers.RequireSession(sessions) at "POST /probe/{id}/dependencies" --
// exactly like routes.go's own wiring -- so r.PathValue("id") is populated
// the same way a real request would populate it, mirroring
// milestone_status_test.go's doMilestoneStatusPostRequest precedent.
func doTaskDependenciesPostRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/dependencies", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/dependencies", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// doTaskDependenciesGetRequest drives the ungated GET endpoint directly,
// with r.PathValue("id") populated via a bare mux mount, mirroring
// milestone_status_test.go's doMilestoneStatusGetRequest precedent.
func doTaskDependenciesGetRequest(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/{id}/dependencies", handler)

	req := httptest.NewRequest(http.MethodGet, "/probe/"+id+"/dependencies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestDeclareTaskDependenciesHandler_Success proves FR2/NFR6: a
// well-formed declare passes the path's {id} through as TaskID and the
// body's depends_on_task_ids through unchanged, and writes scope_id and
// both LB4 subjects from the session, never the request body.
func TestDeclareTaskDependenciesHandler_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	dependsOnA := uuid.New()
	dependsOnB := uuid.New()

	tasks := &fakeTaskStore{}
	rec := doTaskDependenciesPostRequest(t, handlers.DeclareTaskDependenciesHandler(tasks), sessions, sessionIDStr, taskID.String(),
		`{"depends_on_task_ids": ["`+dependsOnA.String()+`", "`+dependsOnB.String()+`"]}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, taskID.String(), resp.ID)

	assert.Equal(t, scopeID, tasks.gotDeclareParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotDeclareParams.TaskID, "TaskID must come from the path's {id}")
	assert.Equal(t, []uuid.UUID{dependsOnA, dependsOnB}, tasks.gotDeclareParams.DependsOnTaskIDs)
	assert.NotEmpty(t, tasks.gotDeclareParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotDeclareParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestDeclareTaskDependenciesHandler_MalformedDependsOnTaskID_Rejected
// proves a malformed depends_on_task_ids entry is rejected before the
// store is ever called.
func TestDeclareTaskDependenciesHandler_MalformedDependsOnTaskID_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doTaskDependenciesPostRequest(t, handlers.DeclareTaskDependenciesHandler(tasks), sessions, sessionIDStr, uuid.New().String(),
		`{"depends_on_task_ids": ["not-a-uuid"]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotDeclareParams.ScopeID, "a validation failure must never reach the store")
}

// TestDeclareTaskDependenciesHandler_StoreRejection_Returns400 proves
// writeStoreError maps DeclareDependency's named FR2 rejections
// (store.ErrSelfDependency, store.ErrDependencyCycle) and store.ErrNotFound
// (NFR1's cross-scope rejection) onto a 400.
func TestDeclareTaskDependenciesHandler_StoreRejection_Returns400(t *testing.T) {
	for name, err := range map[string]error{
		"unknown or cross-scope task id": store.ErrNotFound,
		"self-dependency":                store.ErrSelfDependency,
		"dependency cycle":               store.ErrDependencyCycle,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{declareErr: err}

			rec := doTaskDependenciesPostRequest(t, handlers.DeclareTaskDependenciesHandler(tasks), sessions, sessionIDStr, uuid.New().String(),
				`{"depends_on_task_ids": ["`+uuid.New().String()+`"]}`)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// TestDeclareTaskDependenciesHandler_NoSessionHeader_Rejected proves
// NFR6's write gate: POST /tasks/{id}/dependencies with no
// X-Krill-Session-Id header is rejected before the body is ever parsed or
// the store is ever called.
func TestDeclareTaskDependenciesHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}

	rec := doTaskDependenciesPostRequest(t, handlers.DeclareTaskDependenciesHandler(tasks), sessions, "", uuid.New().String(),
		`{"depends_on_task_ids": ["`+uuid.New().String()+`"]}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotDeclareParams.ScopeID, "a request with no session header must never reach the store")
}

// TestListTaskDependenciesHandler_Success proves the ungated read
// resolves {id}'s own scope via GetTaskByID before calling
// ListDependencies, and passes the declared dependencies through
// unchanged.
func TestListTaskDependenciesHandler_Success(t *testing.T) {
	taskID := uuid.New()
	scopeID := uuid.New()
	dependsOn := uuid.New()

	tasks := &fakeTaskStore{
		getTaskByIDResult: store.Task{ID: taskID, ScopeID: scopeID},
		dependencies: []store.TaskDependency{
			{ID: uuid.New(), ScopeID: scopeID, TaskID: taskID, DependsOnTaskID: dependsOn},
		},
	}

	rec := doTaskDependenciesGetRequest(t, handlers.ListTaskDependenciesHandler(tasks), taskID.String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Dependencies []struct {
			ID              string `json:"id"`
			TaskID          string `json:"task_id"`
			DependsOnTaskID string `json:"depends_on_task_id"`
		} `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Dependencies, 1)
	assert.Equal(t, taskID.String(), resp.Dependencies[0].TaskID)
	assert.Equal(t, dependsOn.String(), resp.Dependencies[0].DependsOnTaskID)
}

// TestListTaskDependenciesHandler_TaskNotFound_Returns404 proves an
// unknown task id is reported as 404, never a 500 or an empty 200.
func TestListTaskDependenciesHandler_TaskNotFound_Returns404(t *testing.T) {
	tasks := &fakeTaskStore{getTaskByIDErr: store.ErrNotFound}

	rec := doTaskDependenciesGetRequest(t, handlers.ListTaskDependenciesHandler(tasks), uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
