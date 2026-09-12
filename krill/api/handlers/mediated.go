// This file (issue #2546, FR9, FR10, NFR2) is the mediated-intake HTTP
// surface: ProposeEntitiesHandler, POST /design-sessions/{id}/propose. It
// is the write half of the FR8->FR9 seam -- a Requirement Contributor's
// free-text opening_submission (design_session.go's
// OpenDesignSessionHandler) is turned into conforming Feature/Requirement
// rows by a producer-role Agent, who calls this endpoint.
package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// mediatedProposalRequest is one element of proposeEntitiesRequest's
// proposals array -- mirrors store.MediatedEntityProposal field-for-field,
// except ParentID/ParentProposalIndex arrive as wire-friendly types
// (string/*int) ahead of parsing.
type mediatedProposalRequest struct {
	Kind                string  `json:"kind"`
	ParentID            *string `json:"parent_id,omitempty"`
	ParentProposalIndex *int    `json:"parent_proposal_index,omitempty"`
	Name                string  `json:"name"`
	Body                *string `json:"body,omitempty"`
	Position            int     `json:"position"`
	RequirementKind     string  `json:"requirement_kind,omitempty"`
	SummaryLine         string  `json:"summary_line"`
}

// proposeEntitiesRequest is ProposeEntitiesHandler's request body.
// Deliberately has no field for acting/on_behalf_of/scope_id, and no
// event_type field -- per this task's gating invariant, the two identity
// triples and scope_id always come from the gating krill_session
// (SessionFromContext), never from this body, and event_type is always
// EventTypeDraft on this path (never a caller-chosen value).
type proposeEntitiesRequest struct {
	VerifiedAgainst *string                   `json:"verified_against"`
	Proposals       []mediatedProposalRequest `json:"proposals"`
}

// proposedEntityWire is the wire shape of one store.ProposedEntity.
type proposedEntityWire struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// proposeEntitiesResponse is ProposeEntitiesHandler's 201 response body:
// the new revision_event's id/seq_no, plus every created entity's kind and
// id, in request order.
type proposeEntitiesResponse struct {
	RevisionEventID string               `json:"revision_event_id"`
	SeqNo           int                  `json:"seq_no"`
	Entities        []proposedEntityWire `json:"entities"`
}

// ProposeEntitiesHandler returns the mediated-intake endpoint (FR9, FR10,
// NFR2): POST /design-sessions/{id}/propose. Must be mounted behind
// RequireSession (gate.go).
func ProposeEntitiesHandler(mediated store.MediatedWriteStore) http.HandlerFunc {
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

		var req proposeEntitiesRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if len(req.Proposals) == 0 {
			writeJSONError(w, http.StatusBadRequest, "proposals: must not be empty")
			return
		}

		proposals := make([]store.MediatedEntityProposal, len(req.Proposals))
		for i, p := range req.Proposals {
			mp := store.MediatedEntityProposal{
				Name:        p.Name,
				Body:        p.Body,
				Position:    p.Position,
				SummaryLine: p.SummaryLine,
			}

			switch store.MediatedEntityKind(p.Kind) {
			case store.MediatedEntityKindFeature:
				mp.Kind = store.MediatedEntityKindFeature
			case store.MediatedEntityKindRequirement:
				mp.Kind = store.MediatedEntityKindRequirement
				mp.RequirementKind = store.RequirementKind(p.RequirementKind)
			default:
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("proposals[%d].kind: unrecognized value %q", i, p.Kind))
				return
			}

			switch {
			case p.ParentID != nil && p.ParentProposalIndex != nil:
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("proposals[%d]: exactly one of parent_id/parent_proposal_index must be set", i))
				return
			case p.ParentID != nil:
				parentID, err := parseUUIDField(fmt.Sprintf("proposals[%d].parent_id", i), *p.ParentID)
				if err != nil {
					writeJSONError(w, http.StatusBadRequest, err.Error())
					return
				}
				mp.ParentID = &parentID
			case p.ParentProposalIndex != nil:
				idx := *p.ParentProposalIndex
				mp.ParentProposalIndex = &idx
			default:
				writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("proposals[%d]: exactly one of parent_id/parent_proposal_index must be set", i))
				return
			}

			proposals[i] = mp
		}

		// sess.Acting/sess.OnBehalfOf/sess.ScopeID come from the gating
		// krill_session only -- req above has no field for any of them, so
		// there is nothing here for a caller to override even if it tried
		// (mirrors revision_event.go's AppendRevisionEventHandler).
		in := store.MediatedProposal{
			SessionID:       sessionID,
			ScopeID:         sess.ScopeID,
			Acting:          sess.Acting,
			OnBehalfOf:      sess.OnBehalfOf,
			EventType:       store.EventTypeDraft,
			VerifiedAgainst: req.VerifiedAgainst,
			Proposals:       proposals,
		}

		ev, entities, err := mediated.ProposeEntities(r.Context(), in)
		if err != nil {
			writeMediatedProposalError(w, err)
			return
		}

		out := make([]proposedEntityWire, len(entities))
		for i, e := range entities {
			out[i] = proposedEntityWire{Kind: string(e.Kind), ID: e.ID.String()}
		}

		writeJSON(w, http.StatusCreated, proposeEntitiesResponse{
			RevisionEventID: ev.ID.String(),
			SeqNo:           ev.SeqNo,
			Entities:        out,
		})
	}
}

// writeMediatedProposalError maps a MediatedWriteStore.ProposeEntities
// error onto this package's one JSON error shape:
//   - store.ErrEmptyMediatedProposal, store.ErrInvalidMediatedProposal, or
//     store.ErrInvalidRevisionEvent (an empty proposal list, a malformed
//     proposal, a missing verified_against, or a non-draft event_type)
//     -> 400, the store's own named message.
//   - store.ErrMediatedIdentitySame (FR10: acting == on_behalf_of)
//     -> 400, a message that plainly states a mediated write requires an
//     agent acting on a contributor's behalf -- never the store's own
//     message verbatim, so this specific rejection reads the same
//     regardless of how the store happens to phrase it internally.
//   - store.ErrNotFound naming design_session (an unknown design session
//     id, from appendRevisionEventTx's own errParentNotFound) -> 404.
//   - store.ErrNotFound naming feature_set/feature (an unknown or
//     cross-scope proposal parent, from ProposeEntities' own
//     currentRowExists check) -> 400, matching types.go's
//     writeStoreError precedent for every other Create* endpoint's
//     unknown-parent case.
//   - anything else (a genuine store failure) -> 500.
func writeMediatedProposalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrMediatedIdentitySame):
		writeJSONError(w, http.StatusBadRequest, "a mediated write requires an agent acting on a contributor's behalf, not the same identity for both")
	case errors.Is(err, store.ErrEmptyMediatedProposal), errors.Is(err, store.ErrInvalidMediatedProposal), errors.Is(err, store.ErrInvalidRevisionEvent):
		writeJSONError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		if strings.Contains(err.Error(), "design_session") {
			writeJSONError(w, http.StatusNotFound, "design session not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSONError(w, http.StatusInternalServerError, "failed to propose entities")
	}
}
