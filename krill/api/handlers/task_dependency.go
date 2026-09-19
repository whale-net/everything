// This file (issue #2720, FR2, C14) is the work-axis dependency-
// declaration HTTP surface: POST /tasks/{id}/dependencies (gated) over
// store.TaskStore.DeclareDependency, and GET /tasks/{id}/dependencies
// (ungated) over store.TaskStore.ListDependencies -- mirroring task.go's
// own CreateTaskHandler for the gated half, and milestone.go's
// GetProductDeliveryHandler for the ungated half's "resolve scope from
// the entity itself" posture, since a read endpoint has no session to
// source scope_id from.
package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// declareTaskDependenciesRequest is DeclareTaskDependenciesHandler's
// request body (FR2): the set of task ids {id} depends on.
type declareTaskDependenciesRequest struct {
	DependsOnTaskIDs []string `json:"depends_on_task_ids"`
}

// DeclareTaskDependenciesHandler returns the dependency-declaration
// endpoint (FR2): POST /tasks/{id}/dependencies. Must be mounted behind
// RequireSession (NFR6) -- scope_id and both subjects always come from
// the session RequireSession resolves, never the request body.
func DeclareTaskDependenciesHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		taskID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req declareTaskDependenciesRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		dependsOnTaskIDs := make([]uuid.UUID, len(req.DependsOnTaskIDs))
		for i, raw := range req.DependsOnTaskIDs {
			id, err := ParseUUIDField(fmt.Sprintf("depends_on_task_ids[%d]", i), raw)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			dependsOnTaskIDs[i] = id
		}

		if err := tasks.DeclareDependency(r.Context(), store.DeclareDependencyParams{
			ScopeID:          sess.ScopeID,
			TaskID:           taskID,
			DependsOnTaskIDs: dependsOnTaskIDs,
			Acting:           sess.Acting,
			OnBehalfOf:       sess.OnBehalfOf,
		}); err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: taskID.String()})
	}
}

// taskDependencyResponse is one entry of ListTaskDependenciesHandler's
// response body -- the wire shape of a store.TaskDependency row.
type taskDependencyResponse struct {
	ID              string `json:"id"`
	TaskID          string `json:"task_id"`
	DependsOnTaskID string `json:"depends_on_task_id"`
}

// listTaskDependenciesResponse is ListTaskDependenciesHandler's response
// body: taskID's declared dependencies, in declaration order.
type listTaskDependenciesResponse struct {
	Dependencies []taskDependencyResponse `json:"dependencies"`
}

// ListTaskDependenciesHandler returns the dependency-list read endpoint
// (FR2): GET /tasks/{id}/dependencies, ungated like every other read
// endpoint in this package. Resolves {id}'s own ScopeID via
// store.TaskStore.GetTaskByID before calling ListDependencies -- a read
// endpoint has no session to source scope_id from, mirroring
// GetProductDeliveryHandler's own "resolve scope from the entity itself"
// posture (milestone.go).
func ListTaskDependenciesHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		taskID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		task, err := tasks.GetTaskByID(r.Context(), taskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "task not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		deps, err := tasks.ListDependencies(r.Context(), task.ScopeID, taskID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		resp := make([]taskDependencyResponse, len(deps))
		for i, d := range deps {
			resp[i] = taskDependencyResponse{
				ID:              d.ID.String(),
				TaskID:          d.TaskID.String(),
				DependsOnTaskID: d.DependsOnTaskID.String(),
			}
		}

		writeJSON(w, http.StatusOK, listTaskDependenciesResponse{Dependencies: resp})
	}
}
