// Unit tests for ListTasksHandler (task_list.go, issue #2941): GET
// /milestones/{id}/tasks is ungated, rejects a non-UUID path id, passes the
// milestone id through unchanged, and maps every store.TaskSummary field
// onto the wire. No Postgres dependency -- fakeTaskStore
// (fake_task_store_test.go) stands in for store.TaskStore; the real query
// is covered by krill/store/task_integration_test.go.
package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// serveListTasks routes through a real ServeMux so {id} is populated the
// same way routes.go's mount populates it.
func serveListTasks(t *testing.T, tasks *fakeTaskStore, milestoneID string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /milestones/{id}/tasks", handlers.ListTasksHandler(tasks))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/milestones/"+milestoneID+"/tasks", nil))
	return rec
}

func TestListTasksHandler_InvalidID_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := serveListTasks(t, tasks, "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotListTasksMilestoneID, "an invalid id must never reach the store")
}

// TestListTasksHandler_Success proves the path id passes through and every
// summary field (lane, attempt count, live-claim flag) reaches the wire in
// store order.
func TestListTasksHandler_Success(t *testing.T) {
	milestoneID := uuid.New()
	first, second := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{tasksByMilestone: map[uuid.UUID][]store.TaskSummary{
		milestoneID: {
			{ID: first, Title: "first", CurrentLane: store.LaneImplementation, AttemptCount: 2, HasLiveClaim: true},
			{ID: second, Title: "second", CurrentLane: store.LaneDone},
		},
	}}

	rec := serveListTasks(t, tasks, milestoneID.String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, milestoneID, tasks.gotListTasksMilestoneID)
	var resp handlers.ListTasksResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, []handlers.TaskSummaryWire{
		{ID: first.String(), Title: "first", CurrentLane: string(store.LaneImplementation), AttemptCount: 2, HasLiveClaim: true},
		{ID: second.String(), Title: "second", CurrentLane: string(store.LaneDone)},
	}, resp.Tasks)
}

// TestListTasksHandler_NoTasks_ReturnsEmptyArray proves a milestone with no
// tasks lists as `[]`, never `null` or a 404.
func TestListTasksHandler_NoTasks_ReturnsEmptyArray(t *testing.T) {
	rec := serveListTasks(t, &fakeTaskStore{}, uuid.New().String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"tasks": []}`, rec.Body.String())
}

func TestListTasksHandler_StoreError_Returns500(t *testing.T) {
	tasks := &fakeTaskStore{listTasksByMilestoneErr: errors.New("boom")}
	rec := serveListTasks(t, tasks, uuid.New().String())

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}
