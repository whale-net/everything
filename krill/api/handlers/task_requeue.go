// This file (issue #2876, root plan #2851's M5, FR6) is the work-axis
// requeue HTTP surface: POST /tasks/{id}/requeue over
// store.TaskStore.RequeueTask (krill/store/task_requeue.go), gated behind
// RequireSession like every other write endpoint (NFR6) -- scope_id and
// both subjects always come from the session RequireSession resolves,
// never the request body. The response is work.Assembler.Assemble's own
// payload document, the same document GET /tasks/{id} and a successful
// claim/complete/abandon/cancel/release/escalate return (FR4/NFR4, LB7):
// its State/CurrentLane fields report the task's resulting state
// (claimable, at the lane it held when escalated) 1:1 with the MCP
// requeue_task tool (krill/mcp/tools/task_requeue.go).
package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// requeueTaskRequest is RequeueTaskHandler's request body (FR6): an
// optional free-text rationale. ScopeID and both subjects come from the
// gated session, never this body (NFR6).
type requeueTaskRequest struct {
	Reason *string `json:"reason"`
}

// RequeueTaskHandler returns the work-axis requeue endpoint (FR6): POST
// /tasks/{id}/requeue. Must be mounted behind RequireSession.
func RequeueTaskHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		var req requeueTaskRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		if _, err := tasks.RequeueTask(r.Context(), store.RequeueParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			Reason:     req.Reason,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		}); err != nil {
			writeRequeueStoreError(w, err)
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

// writeRequeueStoreError maps a RequeueTask error onto this package's one
// JSON error shape -- mirrors writeStoreError (types.go), extended with
// RequeueTask's own named FR6 rejection (ErrTaskNotEscalated).
func writeRequeueStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrTaskNotEscalated),
		errors.Is(err, store.ErrTaskCancelled):
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to requeue task")
	}
}
