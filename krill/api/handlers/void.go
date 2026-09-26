// This file is the void HTTP surface: POST /{kind}/{id}/void for each of
// the seven void-able spec-axis entity kinds -- Product, FeatureSet,
// Feature, Requirement, Persona, NonGoal, LoadBearingDecision.
// store/void.go's VoidStore is the only place the tombstoning write lives;
// these handlers are thin request/response adapters gated by
// RequireSession (gate.go) like every other write endpoint, mirroring
// amend.go's structure (one shared begin/decode/finish body, parameterised
// by the kind's store method) rather than seven near-copies of it.
//
// A Milestone is deliberately absent: it is a delivery-axis fact whose
// delivery records are append-only, and FR 39373553 gives it amend for its
// authoring fields instead.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// voidRequest is every void endpoint's body. Reason is optional and
// recorded on the tombstone for the audit read.
type voidRequest struct {
	Reason *string `json:"reason,omitempty"`
}

// VoidEventWire is one VoidEvent in its wire form (LB7). RetiredNumber is
// a pointer because it is NULL for the five kinds that carry no display
// number -- a rendered `FR7` citation is derived at render time, so there
// is nothing stored to retire (FR d38d726e (c)).
type VoidEventWire struct {
	ID                   string  `json:"id"`
	EntityKind           string  `json:"entity_kind"`
	EntityID             string  `json:"entity_id"`
	ProductID            string  `json:"product_id"`
	RetiredNumber        *int    `json:"retired_number"`
	Reason               *string `json:"reason"`
	CreatedByActingIss   string  `json:"created_by_acting_iss"`
	CreatedByActingSub   string  `json:"created_by_acting_sub"`
	CreatedByActingKind  string  `json:"created_by_acting_kind"`
	CreatedByOnBehalfIss string  `json:"created_by_on_behalf_of_iss"`
	CreatedByOnBehalfSub string  `json:"created_by_on_behalf_of_sub"`
	CreatedByOnBehalfKnd string  `json:"created_by_on_behalf_of_kind"`
	CreatedAt            string  `json:"created_at"`
}

// ListVoidEventsResponse is the audit read's body: every void in a scope,
// oldest first. This is the only way to reach a tombstoned entity's
// original id and original display number.
type ListVoidEventsResponse struct {
	VoidEvents []VoidEventWire `json:"void_events"`
}

// NewListVoidEventsResponse converts store.VoidEvents to their wire form.
func NewListVoidEventsResponse(events []store.VoidEvent) ListVoidEventsResponse {
	out := make([]VoidEventWire, 0, len(events))
	for _, e := range events {
		out = append(out, VoidEventWire{
			ID:                   e.ID.String(),
			EntityKind:           string(e.EntityKind),
			EntityID:             e.EntityID.String(),
			ProductID:            e.ProductID.String(),
			RetiredNumber:        e.RetiredDisplayNumber,
			Reason:               e.Reason,
			CreatedByActingIss:   e.CreatedByActing.Iss,
			CreatedByActingSub:   e.CreatedByActing.Sub,
			CreatedByActingKind:  string(e.CreatedByActing.Kind),
			CreatedByOnBehalfIss: e.CreatedByOnBehalfOf.Iss,
			CreatedByOnBehalfSub: e.CreatedByOnBehalfOf.Sub,
			CreatedByOnBehalfKnd: string(e.CreatedByOnBehalfOf.Kind),
			CreatedAt:            e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		})
	}
	return ListVoidEventsResponse{VoidEvents: out}
}

// voidFn is one VoidStore method in the shape VoidHandler needs, so all
// seven endpoints share one body instead of seven copies of it.
type voidFn func(r *http.Request, voids store.VoidStore, sess GatedSession, id uuid.UUID, reason *string) error

// VoidHandler returns the void endpoint for one entity kind: POST
// /{plural}/{id}/void. Must be mounted behind RequireSession -- scope_id
// and both LB4 subjects come from that session, never from the request,
// so a void is always attributed to a real caller (NFR6).
//
// Both refusals surface as 409 via writeStoreError: a delivered or
// shipped entity, and an entity with a live child. Neither is the caller's
// fault in the sense of a malformed request, and both name the correct
// next step in the error body.
func VoidHandler(voids store.VoidStore, voidFn voidFn) http.HandlerFunc {
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

		var req voidRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if err := voidFn(r, voids, sess, id, req.Reason); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
	}
}

// The seven per-kind endpoints, one thin adapter each.

func VoidProductHandler(voids store.VoidStore) http.HandlerFunc {
	return VoidHandler(voids, func(r *http.Request, v store.VoidStore, s GatedSession, id uuid.UUID, reason *string) error {
		return v.VoidProduct(r.Context(), s.ScopeID, id, reason, s.Acting, s.OnBehalfOf)
	})
}

func VoidFeatureSetHandler(voids store.VoidStore) http.HandlerFunc {
	return VoidHandler(voids, func(r *http.Request, v store.VoidStore, s GatedSession, id uuid.UUID, reason *string) error {
		return v.VoidFeatureSet(r.Context(), s.ScopeID, id, reason, s.Acting, s.OnBehalfOf)
	})
}

func VoidFeatureHandler(voids store.VoidStore) http.HandlerFunc {
	return VoidHandler(voids, func(r *http.Request, v store.VoidStore, s GatedSession, id uuid.UUID, reason *string) error {
		return v.VoidFeature(r.Context(), s.ScopeID, id, reason, s.Acting, s.OnBehalfOf)
	})
}

func VoidRequirementHandler(voids store.VoidStore) http.HandlerFunc {
	return VoidHandler(voids, func(r *http.Request, v store.VoidStore, s GatedSession, id uuid.UUID, reason *string) error {
		return v.VoidRequirement(r.Context(), s.ScopeID, id, reason, s.Acting, s.OnBehalfOf)
	})
}

func VoidPersonaHandler(voids store.VoidStore) http.HandlerFunc {
	return VoidHandler(voids, func(r *http.Request, v store.VoidStore, s GatedSession, id uuid.UUID, reason *string) error {
		return v.VoidPersona(r.Context(), s.ScopeID, id, reason, s.Acting, s.OnBehalfOf)
	})
}

func VoidNonGoalHandler(voids store.VoidStore) http.HandlerFunc {
	return VoidHandler(voids, func(r *http.Request, v store.VoidStore, s GatedSession, id uuid.UUID, reason *string) error {
		return v.VoidNonGoal(r.Context(), s.ScopeID, id, reason, s.Acting, s.OnBehalfOf)
	})
}

func VoidLoadBearingDecisionHandler(voids store.VoidStore) http.HandlerFunc {
	return VoidHandler(voids, func(r *http.Request, v store.VoidStore, s GatedSession, id uuid.UUID, reason *string) error {
		return v.VoidLoadBearingDecision(r.Context(), s.ScopeID, id, reason, s.Acting, s.OnBehalfOf)
	})
}
