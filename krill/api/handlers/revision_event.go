// This file (issue #2543, FR2-FR4, FR7's write half) is the append-
// RevisionEvent HTTP surface -- AppendRevisionEventHandler -- plus the wire
// types (RevisionEventWire and friends) design_session.go's
// GetDesignSessionHandler reuses to render a session's ordered event log.
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// entityDeltaRequest is one entry of appendRevisionEventRequest's
// entity_deltas array -- mirrors store.EntityDelta field-for-field.
type entityDeltaRequest struct {
	EntityID    string `json:"entity_id"`
	Change      string `json:"change"`
	SummaryLine string `json:"summary_line"`
}

// openQuestionOpenedRequest mirrors store.OpenQuestionOpened field-for-
// field (issue #2545, FR6): a question's blocking flag and text are
// established here, at open time, and never resent on a later `resolved`
// entry (openQuestionsDeltaRequest.Resolved below is bare ids).
type openQuestionOpenedRequest struct {
	QuestionID string `json:"question_id"`
	Blocking   bool   `json:"blocking"`
	Text       string `json:"text"`
}

// openQuestionsDeltaRequest mirrors store.OpenQuestionsDelta field-for-field.
type openQuestionsDeltaRequest struct {
	Opened   []openQuestionOpenedRequest `json:"opened"`
	Resolved []string                    `json:"resolved"`
}

// appendRevisionEventRequest is AppendRevisionEventHandler's request body
// (FR2-FR4). It deliberately has no field for acting/on_behalf_of/scope_id:
// per this task's gating invariant (NFR2's precondition), those are always
// sourced from the gating krill_session (SessionFromContext), never from
// this body -- a caller cannot assert an identity the gate did not mint.
type appendRevisionEventRequest struct {
	EventType          string                    `json:"event_type"`
	EntityDeltas       []entityDeltaRequest      `json:"entity_deltas"`
	OpenQuestionsDelta openQuestionsDeltaRequest `json:"open_questions_delta"`
	VerifiedAgainst    *string                   `json:"verified_against"`
	SignoffStatus      *string                   `json:"signoff_status"`
}

// RevisionEventCreatedResponse is AppendRevisionEventHandler's response
// body: the new event's id and its store-allocated seq_no -- never a
// caller-supplied value (RevisionEventStore.Append allocates seq_no under
// its own row lock). Exported (issue #2547) so krill/mcp/tools'
// append_revision_event tool returns this exact value (LB7).
type RevisionEventCreatedResponse struct {
	ID    string `json:"id"`
	SeqNo int    `json:"seq_no"`
}

// validEventTypes enumerates FR2's five closed event_type values -- the
// same set migration 008's CHECK constraint declares. ParseEventType
// rejects any value outside this set before AppendRevisionEventHandler ever
// calls RevisionEventStore.Append, so an unrecognized event_type surfaces
// as a 400 with a named message rather than reaching a raw Postgres
// CHECK-constraint violation (which writeRevisionEventError's default case
// would otherwise map to a 500).
var validEventTypes = map[store.EventType]bool{
	store.EventTypeDraft:          true,
	store.EventTypeReconciliation: true,
	store.EventTypeAnswer:         true,
	store.EventTypeSignoff:        true,
	store.EventTypeRuling:         true,
}

// ParseEventType validates raw against validEventTypes.
func ParseEventType(raw string) (store.EventType, error) {
	et := store.EventType(raw)
	if !validEventTypes[et] {
		return "", fmt.Errorf("event_type: unrecognized value %q", raw)
	}
	return et, nil
}

// AppendRevisionEventHandler returns the append-RevisionEvent endpoint
// (FR2-FR4, FR7's write half): POST /design-sessions/{id}/revision-events.
// Must be mounted behind RequireSession (gate.go).
func AppendRevisionEventHandler(events store.RevisionEventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		sessionID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req appendRevisionEventRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		eventType, err := ParseEventType(req.EventType)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		entityDeltas := make([]store.EntityDelta, len(req.EntityDeltas))
		for i, d := range req.EntityDeltas {
			entityID, err := ParseUUIDField(fmt.Sprintf("entity_deltas[%d].entity_id", i), d.EntityID)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			entityDeltas[i] = store.EntityDelta{
				EntityID:    entityID,
				Change:      store.EntityDeltaChange(d.Change),
				SummaryLine: d.SummaryLine,
			}
		}

		var signoffStatus *store.SignoffStatus
		if req.SignoffStatus != nil {
			st := store.SignoffStatus(*req.SignoffStatus)
			signoffStatus = &st
		}

		opened := make([]store.OpenQuestionOpened, len(req.OpenQuestionsDelta.Opened))
		for i, o := range req.OpenQuestionsDelta.Opened {
			opened[i] = store.OpenQuestionOpened{QuestionID: o.QuestionID, Blocking: o.Blocking, Text: o.Text}
		}

		// sess.Acting/sess.OnBehalfOf/sess.ScopeID come from the gating
		// krill_session only -- req above has no field for any of them, so
		// there is nothing here for a caller to override even if it tried.
		newEvent := store.NewRevisionEvent{
			ScopeID:      sess.ScopeID,
			SessionID:    sessionID,
			Acting:       sess.Acting,
			OnBehalfOf:   sess.OnBehalfOf,
			EventType:    eventType,
			EntityDeltas: entityDeltas,
			OpenQuestionsDelta: store.OpenQuestionsDelta{
				Opened:   opened,
				Resolved: req.OpenQuestionsDelta.Resolved,
			},
			VerifiedAgainst: req.VerifiedAgainst,
			SignoffStatus:   signoffStatus,
		}

		ev, err := events.Append(r.Context(), newEvent)
		if err != nil {
			writeRevisionEventError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, RevisionEventCreatedResponse{ID: ev.ID.String(), SeqNo: ev.SeqNo})
	}
}

// writeRevisionEventError maps a RevisionEventStore.Append error onto this
// package's one JSON error shape:
//   - store.ErrInvalidRevisionEvent (FR3/FR4's conditional-presence rules,
//     or an entity_deltas.change outside created|updated)   -> 400, the
//     store's own named message
//   - store.ErrNotFound (unknown design_session id, surfaced via
//     errParentNotFound)                                     -> 404
//   - anything else (a genuine store failure)                -> 500
func writeRevisionEventError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidRevisionEvent):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeDesignSessionNotFoundOrInternalError(w, err)
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to append revision event")
	}
}

// SubjectWire is the wire shape of a store.Subject -- shared by
// RevisionEventWire's Acting/OnBehalfOf fields.
type SubjectWire struct {
	Iss  string `json:"iss"`
	Sub  string `json:"sub"`
	Kind string `json:"kind"`
}

func ToSubjectWire(s store.Subject) SubjectWire {
	return SubjectWire{Iss: s.Iss, Sub: s.Sub, Kind: string(s.Kind)}
}

// EntityDeltaWire is the wire shape of one store.EntityDelta.
type EntityDeltaWire struct {
	EntityID    string `json:"entity_id"`
	Change      string `json:"change"`
	SummaryLine string `json:"summary_line"`
}

// OpenQuestionOpenedWire is the wire shape of one store.OpenQuestionOpened.
type OpenQuestionOpenedWire struct {
	QuestionID string `json:"question_id"`
	Blocking   bool   `json:"blocking"`
	Text       string `json:"text"`
}

// OpenQuestionsDeltaWire is the wire shape of a store.OpenQuestionsDelta.
type OpenQuestionsDeltaWire struct {
	Opened   []OpenQuestionOpenedWire `json:"opened"`
	Resolved []string                 `json:"resolved"`
}

// RevisionEventWire is the wire shape of one store.RevisionEvent -- used by
// design_session.go's GetDesignSessionHandler to render a session's ordered
// revision_events list, with both identity triples included (FR2).
type RevisionEventWire struct {
	ID                 string                 `json:"id"`
	SeqNo              int                    `json:"seq_no"`
	Acting             SubjectWire            `json:"acting"`
	OnBehalfOf         SubjectWire            `json:"on_behalf_of"`
	EventType          string                 `json:"event_type"`
	EntityDeltas       []EntityDeltaWire      `json:"entity_deltas"`
	OpenQuestionsDelta OpenQuestionsDeltaWire `json:"open_questions_delta"`
	VerifiedAgainst    *string                `json:"verified_against,omitempty"`
	SignoffStatus      *string                `json:"signoff_status,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
}

func ToRevisionEventWire(ev store.RevisionEvent) RevisionEventWire {
	deltas := make([]EntityDeltaWire, len(ev.EntityDeltas))
	for i, d := range ev.EntityDeltas {
		deltas[i] = EntityDeltaWire{
			EntityID:    d.EntityID.String(),
			Change:      string(d.Change),
			SummaryLine: d.SummaryLine,
		}
	}

	var signoffStatus *string
	if ev.SignoffStatus != nil {
		s := string(*ev.SignoffStatus)
		signoffStatus = &s
	}

	opened := make([]OpenQuestionOpenedWire, len(ev.OpenQuestionsDelta.Opened))
	for i, o := range ev.OpenQuestionsDelta.Opened {
		opened[i] = OpenQuestionOpenedWire{QuestionID: o.QuestionID, Blocking: o.Blocking, Text: o.Text}
	}

	return RevisionEventWire{
		ID:           ev.ID.String(),
		SeqNo:        ev.SeqNo,
		Acting:       ToSubjectWire(ev.Acting),
		OnBehalfOf:   ToSubjectWire(ev.OnBehalfOf),
		EventType:    string(ev.EventType),
		EntityDeltas: deltas,
		OpenQuestionsDelta: OpenQuestionsDeltaWire{
			Opened:   opened,
			Resolved: ev.OpenQuestionsDelta.Resolved,
		},
		VerifiedAgainst: ev.VerifiedAgainst,
		SignoffStatus:   signoffStatus,
		CreatedAt:       ev.CreatedAt,
	}
}
