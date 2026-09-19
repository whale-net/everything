// This file (issue #2721, FR4/FR10, C14/C16, LB7) is the work-axis
// by-task-id fetch surface: GET /tasks/{id} over work.Assembler.Assemble,
// ungated like every other read endpoint in this package (NFR6's gate is
// write-only) -- returns the payload whether or not a claim on the task is
// currently live (FR10), the same document a successful claim (#2722)
// returns. Resolves {id}'s own ScopeID via store.TaskStore.GetTaskByID
// before calling Assemble -- a read endpoint has no session to source
// scope_id from, mirroring ListTaskDependenciesHandler's own "resolve
// scope from the entity itself" posture (task_dependency.go).
package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// GetTaskPayloadHandler returns the by-task-id fetch endpoint (FR4, FR10):
// GET /tasks/{id}.
func GetTaskPayloadHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		payload, err := assembler.Assemble(r.Context(), task.ScopeID, taskID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "task not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, payload)
	}
}
