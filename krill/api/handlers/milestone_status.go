// This file (issue #2685, FR8, FR9, FR12) is the milestone/milepebble
// status HTTP surface: three endpoints over
// store.MilestoneStatusEventStore (krill/store/milestone_status.go) --
// record a status transition, read the current (derived) status, and read
// the full append-only history. Both a MilestoneKindMilestone and a
// MilestoneKindMilepebble row are `milestone_ref` rows (FR9), so these
// same handlers serve both -- there is no milepebble-specific status
// variant, unlike milestone.go's separate Create*/Add*Delivers pairs. POST
// /milestones/{id}/status must be mounted behind RequireSession (gate.go)
// -- the LB4 subject pair always comes from the caller's session, never
// the request body. Both GET endpoints are ungated, same as every other
// read endpoint in this package.
package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// setMilestoneStatusRequest is SetMilestoneStatusHandler's request body
// (FR8, FR9). Note is optional.
type setMilestoneStatusRequest struct {
	Status string  `json:"status"`
	Note   *string `json:"note"`
}

// MilestoneStatusResponse is GetMilestoneStatusHandler's response body:
// the current, derived status (FR8).
type MilestoneStatusResponse struct {
	Status string `json:"status"`
}

// MilestoneStatusEventWire is one entry of
// MilestoneStatusHistoryResponse.Transitions -- the wire shape of one
// store.MilestoneStatusEvent, carrying its own subject pair and timestamp
// (FR12, NFR4).
type MilestoneStatusEventWire struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	Note       *string     `json:"note,omitempty"`
	Acting     SubjectWire `json:"acting"`
	OnBehalfOf SubjectWire `json:"on_behalf_of"`
	CreatedAt  time.Time   `json:"created_at"`
}

// ToMilestoneStatusEventWire converts one store.MilestoneStatusEvent to
// its wire shape, mirroring ToRevisionEventWire's own conversion (LB7).
func ToMilestoneStatusEventWire(e store.MilestoneStatusEvent) MilestoneStatusEventWire {
	return MilestoneStatusEventWire{
		ID:         e.ID.String(),
		Status:     string(e.Status),
		Note:       e.Note,
		Acting:     ToSubjectWire(e.CreatedByActing),
		OnBehalfOf: ToSubjectWire(e.CreatedByOnBehalfOf),
		CreatedAt:  e.CreatedAt,
	}
}

// MilestoneStatusHistoryResponse is
// GetMilestoneStatusHistoryHandler's response body: the full transition
// history in chronological order (FR12).
type MilestoneStatusHistoryResponse struct {
	Transitions []MilestoneStatusEventWire `json:"transitions"`
}

// ValidMilestoneStatuses is FR8's fixed seven-value set, rejected at the
// Go layer with a field-scoped error before ever reaching the DB CHECK
// constraint (migration 012) that is the real enforcement point. Exported
// (LB7) so krill/mcp/tools' set_milestone_status tool validates against
// this exact set rather than a second, MCP-local copy.
var ValidMilestoneStatuses = map[store.MilestoneStatus]struct{}{
	store.MilestoneStatusNotStarted:        {},
	store.MilestoneStatusInDesign:          {},
	store.MilestoneStatusPlanned:           {},
	store.MilestoneStatusInProgress:        {},
	store.MilestoneStatusShipped:           {},
	store.MilestoneStatusPartiallyComplete: {},
	store.MilestoneStatusAbandoned:         {},
}

// validMilestoneStatusesOrdered is ValidMilestoneStatuses' own fixed FR8
// order (not map iteration order, which Go randomizes) -- used only to
// name the seven valid values in a 400 body (e.g.
// GetProductDeliveryHandler's unknown-`status`-query-param rejection).
var validMilestoneStatusesOrdered = []store.MilestoneStatus{
	store.MilestoneStatusNotStarted,
	store.MilestoneStatusInDesign,
	store.MilestoneStatusPlanned,
	store.MilestoneStatusInProgress,
	store.MilestoneStatusShipped,
	store.MilestoneStatusPartiallyComplete,
	store.MilestoneStatusAbandoned,
}

// validMilestoneStatusesJoined renders validMilestoneStatusesOrdered as a
// comma-separated, quoted list for an error message body.
func validMilestoneStatusesJoined() string {
	quoted := make([]string, len(validMilestoneStatusesOrdered))
	for i, s := range validMilestoneStatusesOrdered {
		quoted[i] = fmt.Sprintf("%q", string(s))
	}
	return strings.Join(quoted, ", ")
}

// SetMilestoneStatusHandler returns the status-transition endpoint (FR8,
// FR9): POST /milestones/{id}/status. Must be mounted behind
// RequireSession.
func SetMilestoneStatusHandler(statuses store.MilestoneStatusEventStore) http.HandlerFunc {
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

		var req setMilestoneStatusRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		status := store.MilestoneStatus(req.Status)
		if _, ok := ValidMilestoneStatuses[status]; !ok {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("status: must be one of the fixed FR8 values, got %q", req.Status))
			return
		}

		event, err := statuses.RecordTransition(r.Context(), sess.ScopeID, id, status, req.Note, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: event.ID.String()})
	}
}

// GetMilestoneStatusHandler returns the current-status read endpoint
// (FR8): GET /milestones/{id}/status, ungated like every other read
// endpoint in this package.
func GetMilestoneStatusHandler(statuses store.MilestoneStatusEventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		// CurrentStatus derives "not started" from the absence of a row
		// rather than validating milestone_ref existence (see its own doc
		// comment) -- a nonexistent id therefore reads back as "not
		// started" here too, same as a real id with no transitions yet.
		status, err := statuses.CurrentStatus(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, MilestoneStatusResponse{Status: string(status)})
	}
}

// GetMilestoneStatusHistoryHandler returns the full-history read endpoint
// (FR12): GET /milestones/{id}/status/history, ungated like every other
// read endpoint in this package.
func GetMilestoneStatusHistoryHandler(statuses store.MilestoneStatusEventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		events, err := statuses.ListTransitions(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wires := make([]MilestoneStatusEventWire, len(events))
		for i, e := range events {
			wires[i] = ToMilestoneStatusEventWire(e)
		}

		writeJSON(w, http.StatusOK, MilestoneStatusHistoryResponse{Transitions: wires})
	}
}
