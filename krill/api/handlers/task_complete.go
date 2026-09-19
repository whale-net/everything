// This file (issue #2725, FR8, C15) is the work-axis complete-with-a-
// verdict HTTP surface: POST /tasks/{id}/complete over
// store.TaskStore.CompleteTask (krill/store/task_complete.go), gated
// behind RequireSession like every other write endpoint (NFR6) -- scope_id
// and both subjects always come from the session RequireSession resolves,
// never the request body. completeTaskRequest declares no lane/destination
// field of any kind, and decodeStrict (types.go) rejects any unknown field
// a request body carries -- together these mean a caller supplying a
// destination lane is rejected outright (FR8: "in neither case does the
// completing Agent name or choose the destination lane"), not merely
// ignored. The response is work.Assembler.Assemble's own payload document
// (krill/work/payload.go), the exact same document GET /tasks/{id} and a
// successful claim (#2722) return (FR4/NFR4, LB7) -- never a hand-built
// completion response struct.
package handlers

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// completeTaskRequest is CompleteTaskHandler's request body (FR8): the
// claim the caller holds, and a pass/fail verdict -- ScopeID and both
// subjects come from the gated session (NFR6), and there is deliberately
// no lane/destination field for decodeStrict to reject a caller that
// supplies one.
type completeTaskRequest struct {
	ClaimID string  `json:"claim_id"`
	Verdict string  `json:"verdict"`
	Summary *string `json:"summary"`
}

// CompleteTaskHandler returns the complete-with-a-verdict endpoint (FR8):
// POST /tasks/{id}/complete. Must be mounted behind RequireSession.
func CompleteTaskHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		var req completeTaskRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		claimID, err := ParseUUIDField("claim_id", req.ClaimID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if _, err := tasks.CompleteTask(r.Context(), store.CompleteTaskParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			ClaimID:    claimID,
			Verdict:    store.Verdict(req.Verdict),
			Summary:    req.Summary,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		}); err != nil {
			writeCompleteStoreError(w, err)
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

// writeCompleteStoreError maps a CompleteTask error onto this package's
// one JSON error shape -- mirrors writeStoreError (types.go), extended
// with CompleteTask's own named FR8 rejections.
func writeCompleteStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, store.ErrUnknownVerdict):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrClaimNotCurrent):
		writeJSONError(w, http.StatusConflict, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to complete task")
	}
}
