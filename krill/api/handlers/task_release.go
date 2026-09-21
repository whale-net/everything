// This file (issue #2872, root plan #2851's M5, FR8) is the work-axis
// release HTTP surface: POST /tasks/{id}/release over
// store.TaskStore.ReleaseLease (krill/store/task_release.go), gated
// behind RequireSession like every other write endpoint (NFR6) --
// scope_id and both subjects always come from the session RequireSession
// resolves, never the request body. The response is
// work.Assembler.Assemble's own payload document, the same document GET
// /tasks/{id} and a successful claim/complete/abandon/cancel return
// (FR4/NFR4, LB7) -- its State/EscalationReason fields (issue #2870)
// report the task's resulting state (claimable vs escalated) 1:1 with the
// MCP release_task tool (krill/mcp/tools/task_release.go).
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// releaseTaskRequest is ReleaseTaskHandler's request body (FR8): an
// optional free-text rationale. ScopeID and both subjects come from the
// gated session, never this body (NFR6).
type releaseTaskRequest struct {
	Reason *string `json:"reason"`
}

// ReleaseTaskHandler returns the work-axis release endpoint (FR8): POST
// /tasks/{id}/release. Must be mounted behind RequireSession.
func ReleaseTaskHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		var req releaseTaskRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		if _, err := tasks.ReleaseLease(r.Context(), store.ReleaseParams{
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
