// This file (issue #2872, root plan #2851's M5, FR9) is the work-axis
// manual-escalate HTTP surface: POST /tasks/{id}/escalate over
// store.TaskStore.EscalateTask (krill/store/task_escalate.go), gated
// behind RequireSession like every other write endpoint (NFR6) --
// scope_id and both subjects always come from the session RequireSession
// resolves, never the request body. The response is
// work.Assembler.Assemble's own payload document, mirroring
// task_release.go's own posture (LB7) -- 1:1 with the MCP escalate_task
// tool (krill/mcp/tools/task_escalate.go).
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// escalateTaskRequest is EscalateTaskHandler's request body (FR9): an
// optional free-text rationale. ScopeID and both subjects come from the
// gated session, never this body (NFR6).
type escalateTaskRequest struct {
	Reason *string `json:"reason"`
}

// EscalateTaskHandler returns the work-axis manual-escalate endpoint
// (FR9): POST /tasks/{id}/escalate. Must be mounted behind
// RequireSession.
func EscalateTaskHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		var req escalateTaskRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		if _, err := tasks.EscalateTask(r.Context(), store.EscalateParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			Reason:     req.Reason,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		}); err != nil {
			writeStoreError(w, err)
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
