// This file (issue #2726, FR9, C15) is the work-axis abandon HTTP surface:
// POST /tasks/{id}/abandon over store.TaskStore.AbandonClaim
// (krill/store/task_abandon.go), gated behind RequireSession like every
// other write endpoint (NFR6) -- scope_id and both subjects always come
// from the session RequireSession resolves, never the request body.
// abandonTaskRequest declares no verdict field of any kind, and
// decodeStrict (types.go) rejects any unknown field a request body
// carries -- together these mean a caller supplying a verdict is rejected
// outright (FR9: abandoning reports no outcome), not merely ignored. The
// response is work.Assembler.Assemble's own payload document
// (krill/work/payload.go), the exact same document GET /tasks/{id} and a
// successful claim/complete return (FR4/NFR4, LB7).
//
// This is the work-axis abandon verb, kept distinct in name and route
// shape from the pre-existing delivery-axis milestone abandon
// (krill/store/abandon.go, POST /milestones/{id}/abandon,
// krill/api/handlers/abandon.go) -- neither path calls the other.
package handlers

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// abandonTaskRequest is AbandonTaskHandler's request body (FR9): the claim
// the caller holds, and an optional free-text reason. ScopeID and both
// subjects come from the gated session, never this body (NFR6). There is
// deliberately no verdict field for decodeStrict to reject a caller that
// supplies one.
type abandonTaskRequest struct {
	ClaimID string  `json:"claim_id"`
	Reason  *string `json:"reason"`
}

// AbandonTaskHandler returns the work-axis abandon endpoint (FR9): POST
// /tasks/{id}/abandon. Must be mounted behind RequireSession.
func AbandonTaskHandler(tasks store.TaskStore, assembler *work.Assembler) http.HandlerFunc {
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

		var req abandonTaskRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		claimID, err := ParseUUIDField("claim_id", req.ClaimID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if _, err := tasks.AbandonClaim(r.Context(), store.AbandonParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			ClaimID:    claimID,
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
