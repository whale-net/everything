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
	"context"
	"errors"
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

// ValidMilestoneStatuses is FR8's fixed eight-value set, rejected at the
// Go layer with a field-scoped error before ever reaching the DB CHECK
// constraint (migration 012, widened by 019) that is the real enforcement
// point. Exported (LB7) so krill/mcp/tools' set_milestone_status tool
// validates against this exact set rather than a second, MCP-local copy.
var ValidMilestoneStatuses = map[store.MilestoneStatus]struct{}{
	store.MilestoneStatusNotStarted:        {},
	store.MilestoneStatusInDesign:          {},
	store.MilestoneStatusDesigned:          {},
	store.MilestoneStatusPlanned:           {},
	store.MilestoneStatusInProgress:        {},
	store.MilestoneStatusShipped:           {},
	store.MilestoneStatusPartiallyComplete: {},
	store.MilestoneStatusAbandoned:         {},
}

// validMilestoneStatusesOrdered is ValidMilestoneStatuses' own fixed FR8
// order (not map iteration order, which Go randomizes) -- used only to
// name the eight valid values in a 400 body (e.g.
// GetProductDeliveryHandler's unknown-`status`-query-param rejection).
var validMilestoneStatusesOrdered = []store.MilestoneStatus{
	store.MilestoneStatusNotStarted,
	store.MilestoneStatusInDesign,
	store.MilestoneStatusDesigned,
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

// ============================================================================
// Transition edge table (issue #2963, FR 5652b8b9) -- a status may only ever
// be replaced by one of the values listed under the status it already holds
// ============================================================================
// The value set above answers "which statuses exist"; this table answers the
// separate, stricter question "which of them may follow which". Without it a
// Swarm Operator holding a Requirement Contributor session could drive a
// milestone from "not started" straight to "shipped", and the append-only
// history would faithfully record an incoherent lifecycle as though it had
// been planned -- the history is immutable, so an illegal edge is not
// something a later correction can quietly overwrite.
//
// Deliberately NOT a DB CHECK: the DB constrains each row's value in
// isolation and knows nothing of the row before it. Enforcing the table here,
// in the one write path both surfaces share, is the same split every other
// krill write rule makes (e.g. the lane-sequence rule on a task).
//
// The table, read as "from -> to":
//
//	not started       -> in design | abandoned
//	in design         -> designed | abandoned
//	designed          -> planned | in design | abandoned
//	planned           -> in progress | abandoned
//	in progress       -> partially complete | shipped | abandoned
//	partially complete -> in progress | shipped | abandoned
//	shipped           -> (terminal)
//	abandoned         -> (terminal)
//
// Three of those rows carry more than the obvious reading:
//   - "designed -> in design" is the rework edge: a container whose design
//     turned out to be wrong goes back to being designed, but a planned
//     one may NOT -- a plan is a commitment, and un-committing it is a
//     different operation than re-designing.
//   - "in progress -> partially complete -> in progress" is the loop: a
//     container with shipped scope reports partial completion, keeps its
//     unshipped remainder in flight, and can move again.
//   - "abandoned" is reachable from every non-terminal row, because
//     abandoning a commitment is always allowed; "shipped" only from
//     "in progress" or "partially complete", because a container that was
//     never worked on has nothing to have shipped.
var milestoneStatusTransitions = map[store.MilestoneStatus][]store.MilestoneStatus{
	store.MilestoneStatusNotStarted:        {store.MilestoneStatusInDesign, store.MilestoneStatusAbandoned},
	store.MilestoneStatusInDesign:          {store.MilestoneStatusDesigned, store.MilestoneStatusAbandoned},
	store.MilestoneStatusDesigned:          {store.MilestoneStatusPlanned, store.MilestoneStatusInDesign, store.MilestoneStatusAbandoned},
	store.MilestoneStatusPlanned:           {store.MilestoneStatusInProgress, store.MilestoneStatusAbandoned},
	store.MilestoneStatusInProgress:        {store.MilestoneStatusPartiallyComplete, store.MilestoneStatusShipped, store.MilestoneStatusAbandoned},
	store.MilestoneStatusPartiallyComplete: {store.MilestoneStatusInProgress, store.MilestoneStatusShipped, store.MilestoneStatusAbandoned},
	store.MilestoneStatusShipped:           nil,
	store.MilestoneStatusAbandoned:         nil,
}

// ErrIllegalMilestoneStatusTransition is the sentinel every rejection from
// ValidateMilestoneStatusTransition unwraps to, so a caller can tell "you
// asked for a transition the table forbids" apart from "the store was
// unreachable" without string-matching the message.
var ErrIllegalMilestoneStatusTransition = errors.New("illegal milestone status transition")

// IllegalMilestoneStatusTransition names the rejected edge and the legal
// alternatives out of the status the container already holds, so a caller
// is told what it may do next rather than only what it may not.
type IllegalMilestoneStatusTransition struct {
	From  store.MilestoneStatus
	To    store.MilestoneStatus
	Legal []store.MilestoneStatus
}

func (e *IllegalMilestoneStatusTransition) Error() string {
	if len(e.Legal) == 0 {
		return fmt.Sprintf("milestone status: illegal transition %q -> %q: %q is terminal, so no transition is legal from it",
			e.From, e.To, e.From)
	}
	legal := make([]string, len(e.Legal))
	for i, s := range e.Legal {
		legal[i] = fmt.Sprintf("%q", string(s))
	}
	return fmt.Sprintf("milestone status: illegal transition %q -> %q: the legal transitions out of %q are: %s",
		e.From, e.To, e.From, strings.Join(legal, ", "))
}

func (e *IllegalMilestoneStatusTransition) Unwrap() error { return ErrIllegalMilestoneStatusTransition }

// ValidateMilestoneStatusTransition reports whether `to` may replace
// `from` per the edge table above. It knows nothing about persistence, so
// it is a pure function of the two statuses -- the caller supplies `from`
// (normally MilestoneStatusEventStore.CurrentStatus) and applies the
// verdict.
func ValidateMilestoneStatusTransition(from, to store.MilestoneStatus) error {
	for _, legal := range milestoneStatusTransitions[from] {
		if legal == to {
			return nil
		}
	}
	return &IllegalMilestoneStatusTransition{From: from, To: to, Legal: milestoneStatusTransitions[from]}
}

// ApplyMilestoneStatus is the one set_milestone_status write path, shared
// by the HTTP handler below and krill/mcp/tools' set_milestone_status tool
// (LB7) so the edge table cannot be enforced on one surface and quietly
// skipped on the other.
//
// Two behaviors, both required by issue #2963:
//   - a self-transition (to == the status already held) is not an edge at
//     all: it is accepted and writes NO history row, returning the latest
//     existing transition (zero-valued when the container has none, which
//     is the "not started" -> "not started" case) so callers still get a
//     stable id back. Re-affirming a status is not a way to fabricate
//     history.
//   - every other transition must be an edge the table allows, else it is
//     rejected with *IllegalMilestoneStatusTransition before any write.
func ApplyMilestoneStatus(ctx context.Context, statuses store.MilestoneStatusEventStore, scopeID, milestoneID uuid.UUID, status store.MilestoneStatus, note *string, acting, onBehalfOf store.Subject) (store.MilestoneStatusEvent, bool, error) {
	current, err := statuses.CurrentStatus(ctx, milestoneID)
	if err != nil {
		return store.MilestoneStatusEvent{}, false, err
	}
	if current == status {
		transitions, err := statuses.ListTransitions(ctx, milestoneID)
		if err != nil {
			return store.MilestoneStatusEvent{}, false, err
		}
		if len(transitions) == 0 {
			return store.MilestoneStatusEvent{}, true, nil
		}
		return transitions[len(transitions)-1], true, nil
	}
	if err := ValidateMilestoneStatusTransition(current, status); err != nil {
		return store.MilestoneStatusEvent{}, false, err
	}
	event, err := statuses.RecordTransition(ctx, scopeID, milestoneID, status, note, acting, onBehalfOf)
	if err != nil {
		return store.MilestoneStatusEvent{}, false, err
	}
	return event, false, nil
}

// SetMilestoneStatusHandler returns the status-transition endpoint (FR8,
// FR9, issue #2963): POST /milestones/{id}/status. Must be mounted behind
// RequireSession. The transition itself -- which status may follow which,
// and the no-op-on-self-transition rule -- is ApplyMilestoneStatus below,
// shared with the MCP surface.
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

		event, noop, err := ApplyMilestoneStatus(r.Context(), statuses, sess.ScopeID, id, status, req.Note, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			// An illegal edge conflicts with the status the container
			// already holds, so 409 rather than 400: the request was
			// well-formed, it just cannot be applied now. The body names
			// the edge and the legal alternatives.
			if errors.Is(err, ErrIllegalMilestoneStatusTransition) {
				writeJSONError(w, http.StatusConflict, err.Error())
				return
			}
			writeStoreError(w, err)
			return
		}

		if noop {
			// 200, not 201: nothing was created. See ApplyMilestoneStatus.
			writeJSON(w, http.StatusOK, IDResponse{ID: eventIDString(event)})
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: event.ID.String()})
	}
}

// eventIDString renders a MilestoneStatusEvent's id for the wire, empty
// when the event is the zero value -- the "not started" container that was
// re-affirmed as "not started" has no transition row to point at, and an
// empty id is more honest than a nil UUID rendered as a real-looking one.
func eventIDString(e store.MilestoneStatusEvent) string {
	if e.ID == uuid.Nil {
		return ""
	}
	return e.ID.String()
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
