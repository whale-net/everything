// This file (issue #2688, FR6) is the abandon HTTP surface: POST
// /milestones/{id}/abandon (gated) over store.AbandonStore.Abandon --
// composing #2685's status transitions, #2686's not-yet-shipped
// definition, and #2687's backlog bucket/move primitive into one call,
// mirroring milestone_status.go's and recut.go's own posture for a
// milestone-or-milepebble target (FR9: both are `milestone_ref` rows, one
// handler serves both).
package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// abandonRequest is AbandonHandler's request body (FR6). Note is
// optional, mirroring every other status-transition/shipment request body
// in this package.
type abandonRequest struct {
	Note *string `json:"note"`
}

// AbandonResponse is AbandonHandler's response body: what Abandon actually
// did to the target container (store.AbandonResult, converted to the same
// wire-safe id-strings shape every other response in this package uses).
// MilepebbleResults is populated only when the target was a milestone with
// live milepebbles cascaded (store.AbandonResult's own doc comment) --
// AbandonMilepebbleResult (not AbandonResponse itself) is deliberately a
// separate, flatter type with no MilepebbleResults field of its own, since
// a milepebble's own result never nests further (store.AbandonResult's own
// invariant) -- this also keeps the wire type acyclic, which the MCP tool
// (abandon.go) needs for its generated output schema.
type AbandonResponse struct {
	ContainerID       string                    `json:"container_id"`
	BacklogID         string                    `json:"backlog_id"`
	MovedToBacklogIDs []string                  `json:"moved_to_backlog_ids"`
	ShippedIDs        []string                  `json:"shipped_ids"`
	StatusEvent       MilestoneStatusEventWire  `json:"status_event"`
	MilepebbleResults []AbandonMilepebbleResult `json:"milepebble_results,omitempty"`
}

// AbandonMilepebbleResult is one entry of AbandonResponse.MilepebbleResults
// -- see AbandonResponse's own doc comment for why this is not another
// AbandonResponse.
type AbandonMilepebbleResult struct {
	ContainerID       string                   `json:"container_id"`
	BacklogID         string                   `json:"backlog_id"`
	MovedToBacklogIDs []string                 `json:"moved_to_backlog_ids"`
	ShippedIDs        []string                 `json:"shipped_ids"`
	StatusEvent       MilestoneStatusEventWire `json:"status_event"`
}

// ToAbandonResponse converts one store.AbandonResult to its wire shape
// (LB7), flattening MilepebbleResults to AbandonMilepebbleResult entries.
func ToAbandonResponse(r store.AbandonResult) AbandonResponse {
	var milepebbles []AbandonMilepebbleResult
	for _, mp := range r.MilepebbleResults {
		milepebbles = append(milepebbles, AbandonMilepebbleResult{
			ContainerID:       mp.ContainerID.String(),
			BacklogID:         mp.BacklogID.String(),
			MovedToBacklogIDs: uuidsToStrings(mp.MovedToBacklogIDs),
			ShippedIDs:        uuidsToStrings(mp.ShippedIDs),
			StatusEvent:       ToMilestoneStatusEventWire(mp.StatusEvent),
		})
	}
	return AbandonResponse{
		ContainerID:       r.ContainerID.String(),
		BacklogID:         r.BacklogID.String(),
		MovedToBacklogIDs: uuidsToStrings(r.MovedToBacklogIDs),
		ShippedIDs:        uuidsToStrings(r.ShippedIDs),
		StatusEvent:       ToMilestoneStatusEventWire(r.StatusEvent),
		MilepebbleResults: milepebbles,
	}
}

// uuidsToStrings converts a uuid.UUID slice to its wire (string) form --
// shared by AbandonResponse's own top-level and per-milepebble id lists.
func uuidsToStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// AbandonHandler returns the abandon endpoint (FR6): POST
// /milestones/{id}/abandon. Must be mounted behind RequireSession. Rejects
// (with no partial write -- NFR3, this task's own atomicity requirement):
//   - the backlog bucket itself as a target (store.ErrCannotAbandonBacklog,
//     409);
//   - a container that is already abandoned, or a milestone with an
//     already-abandoned milepebble (store.ErrAlreadyAbandoned, 409) -- a
//     double-abandon is rejected loudly rather than silently re-swept.
//
// Abandon is irreversible: there is no un-abandon endpoint. Shipped scope
// stays exactly where it is (NFR3) -- reviving a commitment means re-cutting
// its backlog scope into a new container via POST /delivery/move.
func AbandonHandler(abandons store.AbandonStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req abandonRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		result, err := abandons.Abandon(r.Context(), sess.ScopeID, id, req.Note, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			if errors.Is(err, store.ErrCannotAbandonBacklog) || errors.Is(err, store.ErrAlreadyAbandoned) {
				writeJSONError(w, http.StatusConflict, err.Error())
				return
			}
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, ToAbandonResponse(result))
	}
}
