// This file (issue #2724, FR7, C14/C16) is the work-axis reclaim HTTP
// surface: POST /tasks/reclaim over store.TaskStore.ReclaimExpired
// (krill/store/task_reclaim.go), gated behind RequireSession like every
// other write endpoint (NFR6) -- scope and both subjects always come from
// the session RequireSession resolves, never the request body. This is an
// operator/automation-triggered sweep (no background scheduler/cron exists
// in this milestone -- the sweep is an explicit call): it always sweeps
// the caller's own scope, and reclaims every lease-expired task in it
// unless the request body names one particular task_id to sweep instead.
package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// reclaimRequest is ReclaimExpiredHandler's request body (FR7): an
// optional task id to sweep exactly one task instead of every lease-
// expired task in the caller's scope. ScopeID and both subjects come from
// the gated session, never this body (NFR6). An empty/absent body sweeps
// the whole scope.
type reclaimRequest struct {
	TaskID string `json:"task_id,omitempty"`
}

// ReclaimedTaskResponse is one task ReclaimExpiredHandler's response body
// names -- the wire shape of one store.ReclaimedTask.
type ReclaimedTaskResponse struct {
	TaskID       string `json:"task_id"`
	CapExhausted bool   `json:"cap_exhausted"`
}

// ReclaimResponse is ReclaimExpiredHandler's response body (LB7): every
// task the sweep actually reclaimed, nothing else.
type ReclaimResponse struct {
	Reclaimed []ReclaimedTaskResponse `json:"reclaimed"`
}

// ToReclaimResponse converts one store.ReclaimResult to its wire shape.
func ToReclaimResponse(r store.ReclaimResult) ReclaimResponse {
	reclaimed := make([]ReclaimedTaskResponse, len(r.Reclaimed))
	for i, t := range r.Reclaimed {
		reclaimed[i] = ReclaimedTaskResponse{TaskID: t.TaskID.String(), CapExhausted: t.CapExhausted}
	}
	return ReclaimResponse{Reclaimed: reclaimed}
}

// ReclaimExpiredHandler returns the reclaim-sweep endpoint (FR7): POST
// /tasks/reclaim. Must be mounted behind RequireSession.
func ReclaimExpiredHandler(tasks store.TaskStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req reclaimRequest
		if err := decodeStrict(r, &req); err != nil && !errors.Is(err, io.EOF) {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		var taskID *uuid.UUID
		if req.TaskID != "" {
			id, err := ParseUUIDField("task_id", req.TaskID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			taskID = &id
		}

		result, err := tasks.ReclaimExpired(r.Context(), store.ReclaimParams{
			ScopeID:    sess.ScopeID,
			TaskID:     taskID,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, ToReclaimResponse(result))
	}
}
