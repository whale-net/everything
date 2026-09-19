// This file (issue #2722, FR3/FR5, C14) is the work-axis claim HTTP
// surface: POST /tasks/{id}/claim over store.TaskStore.ClaimTask
// (krill/store/task_claim.go), gated behind RequireSession like every
// other write endpoint (NFR6) -- the acting session id, scope, and both
// subjects always come from the session RequireSession resolves, never
// the request body. The response is work.Assembler.Assemble's own
// payload document (krill/work/payload.go), enriched with the fresh
// lease -- the exact same document GET /tasks/{id} returns (FR4/NFR4,
// LB7), never a hand-built claim response struct.
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// ClaimTaskHandler returns the claim endpoint (FR3, FR5): POST
// /tasks/{id}/claim. Must be mounted behind RequireSession.
func ClaimTaskHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		if _, err := tasks.ClaimTask(r.Context(), store.ClaimTaskParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			SessionID:  sess.SessionID,
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
