// This file (issue #2723, FR6, C14) is the work-axis heartbeat HTTP
// surface: POST /tasks/{id}/heartbeat over store.TaskStore.Heartbeat
// (krill/store/task_lease.go), gated behind RequireSession like every
// other write endpoint (NFR6) -- scope and both subjects always come from
// the session RequireSession resolves, never the request body. The
// ClaimID being extended DOES come from the request body (there is no
// other way for the caller to name which claim it holds).
//
// A stale claim -- current_claim_id has moved on, or the named claim was
// already released -- is rejected with a 409 and store.ErrClaimNotCurrent's
// message, never a 500 and never a 200: a caller must be able to tell
// "your claim is gone" from "krill is broken" (FR6).
package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// heartbeatRequest is HeartbeatHandler's request body (FR6): the claim id
// the caller believes it still holds. ScopeID and both subjects come from
// the gated session, never this body (NFR6).
type heartbeatRequest struct {
	ClaimID string `json:"claim_id"`
}

// HeartbeatResponse is HeartbeatHandler's response body (LB7): the wire
// shape of a store.LeaseState -- the task/claim extended and the new
// lease expiry, nothing else. Exported so the MCP heartbeat_task tool
// (krill/mcp/tools/task_lease.go) reuses this exact type rather than a
// bespoke MCP-local mirror, mirroring IDResponse's own precedent (types.go).
type HeartbeatResponse struct {
	TaskID     string `json:"task_id"`
	ClaimID    string `json:"claim_id"`
	ExtendedTo string `json:"extended_to"`
}

// ToHeartbeatResponse converts one store.LeaseState to its wire shape.
func ToHeartbeatResponse(s store.LeaseState) HeartbeatResponse {
	return HeartbeatResponse{
		TaskID:     s.TaskID.String(),
		ClaimID:    s.ClaimID.String(),
		ExtendedTo: s.ExtendedTo.Format(time.RFC3339),
	}
}

// HeartbeatHandler returns the heartbeat endpoint (FR6): POST
// /tasks/{id}/heartbeat. Must be mounted behind RequireSession.
func HeartbeatHandler(tasks store.TaskStore) http.HandlerFunc {
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

		var req heartbeatRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		claimID, err := ParseUUIDField("claim_id", req.ClaimID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		lease, err := tasks.Heartbeat(r.Context(), store.HeartbeatParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			ClaimID:    claimID,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, ToHeartbeatResponse(lease))
	}
}
