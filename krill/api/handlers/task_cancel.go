// This file (issue #2873, root plan #2851's M5, FR7) is the work-axis
// cancel HTTP surface: POST /tasks/{id}/cancel over
// store.TaskStore.CancelTask (krill/store/task_cancel.go), gated behind
// RequireSession like every other write endpoint (NFR6) -- scope_id and
// both subjects always come from the session RequireSession resolves,
// never the request body. The response is work.Assembler.Assemble's own
// payload document, the same document GET /tasks/{id} and a successful
// claim/complete/abandon return (FR4/NFR4, LB7) -- NFR5's proof that
// nothing besides the terminal marker and the intervention event changed
// is exactly what re-fetching that same document lets a caller see.
package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// cancelTaskRequest is CancelTaskHandler's request body (FR7): an
// optional free-text rationale. ScopeID and both subjects come from the
// gated session, never this body (NFR6).
type cancelTaskRequest struct {
	Reason *string `json:"reason"`
}

// CancelTaskHandler returns the work-axis cancel endpoint (FR7): POST
// /tasks/{id}/cancel. Must be mounted behind RequireSession.
func CancelTaskHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		var req cancelTaskRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		if _, err := tasks.CancelTask(r.Context(), store.CancelTaskParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			Reason:     req.Reason,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		}); err != nil {
			writeCancelStoreError(w, err)
			return
		}

		payload, err := assembler.Assemble(r.Context(), sess.ScopeID, taskID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, payload)
	}
}

// writeCancelStoreError maps a CancelTask error onto this package's one
// JSON error shape -- mirrors writeStoreError (types.go), extended with
// CancelTask's own named FR7 rejection.
func writeCancelStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrTaskAlreadyCancelled):
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to cancel task")
	}
}
