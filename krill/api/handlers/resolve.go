// This file is the resolve HTTP surface: POST /non-goals/{id}/resolve,
// settling a `deferred` Non-Goal. It is deliberately NOT part of
// amend.go's shape -- that file's whole contract is that it never re-kinds
// (FR f0f6bc18), and this endpoint's entire job is to re-kind, so the two
// cannot share a request body or a handler.
//
// store/resolve.go's ResolveStore is the only place the resolution write
// lives; this handler is a thin request/response adapter gated by
// RequireSession (gate.go) like every other write endpoint.
package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// resolveRequest is the resolve endpoint's body. Outcome is required --
// there is no default, because "settle it" and "how" are different
// questions and guessing the second would silently pick between the two
// shapes. Reason is optional and recorded on whichever register the chosen
// outcome writes.
type resolveRequest struct {
	Outcome string  `json:"outcome"`
	Reason  *string `json:"reason,omitempty"`
}

// NonGoalWire is a NonGoal in its wire form (LB7). It is the response to a
// PROMOTE, where the caller wants to see the kind it now carries; a RETIRE
// leaves no current row and answers with the id alone.
type NonGoalWire struct {
	ID         string  `json:"id"`
	ScopeID    string  `json:"scope_id"`
	ProductID  string  `json:"product_id"`
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	Body       *string `json:"body"`
	Position   int     `json:"position"`
	ValidFrom  string  `json:"valid_from"`
	ValidTo    *string `json:"valid_to"`
	RevisionID string  `json:"revision_id"`
}

// NewNonGoalWire converts a store.NonGoal to its wire form.
func NewNonGoalWire(ng store.NonGoal) NonGoalWire {
	w := NonGoalWire{
		ID:         ng.ID.String(),
		ScopeID:    ng.ScopeID.String(),
		ProductID:  ng.ProductID.String(),
		Kind:       string(ng.Kind),
		Name:       ng.Name,
		Body:       ng.Body,
		Position:   ng.Position,
		RevisionID: ng.RevisionID.String(),
		ValidFrom:  ng.ValidFrom.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if ng.ValidTo != nil {
		validTo := ng.ValidTo.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		w.ValidTo = &validTo
	}
	return w
}

// NonGoalPromotionWire is one NonGoalPromotion in its wire form (LB7).
type NonGoalPromotionWire struct {
	ID                  string  `json:"id"`
	NonGoalID           string  `json:"non_goal_id"`
	ProductID           string  `json:"product_id"`
	FromKind            string  `json:"from_kind"`
	ToKind              string  `json:"to_kind"`
	Reason              *string `json:"reason"`
	CreatedByActingIss  string  `json:"created_by_acting_iss"`
	CreatedByActingSub  string  `json:"created_by_acting_sub"`
	CreatedByActingKind string  `json:"created_by_acting_kind"`
	CreatedByOnBehalfIs string  `json:"created_by_on_behalf_of_iss"`
	CreatedByOnBehalfSu string  `json:"created_by_on_behalf_of_sub"`
	CreatedByOnBehalfKi string  `json:"created_by_on_behalf_of_kind"`
	CreatedAt           string  `json:"created_at"`
}

// ListNonGoalPromotionsResponse is the audit read's body: every promotion
// in a scope, oldest first. This is how a caller reaches the fact that a
// now-`permanent` Non-Goal was once `deferred`; the SCD2 row itself
// records only that `kind` changed.
type ListNonGoalPromotionsResponse struct {
	Promotions []NonGoalPromotionWire `json:"promotions"`
}

// NewListNonGoalPromotionsResponse converts store.NonGoalPromotions to
// their wire form.
func NewListNonGoalPromotionsResponse(promotions []store.NonGoalPromotion) ListNonGoalPromotionsResponse {
	out := make([]NonGoalPromotionWire, 0, len(promotions))
	for _, p := range promotions {
		out = append(out, NonGoalPromotionWire{
			ID:                  p.ID.String(),
			NonGoalID:           p.NonGoalID.String(),
			ProductID:           p.ProductID.String(),
			FromKind:            string(p.FromKind),
			ToKind:              string(p.ToKind),
			Reason:              p.Reason,
			CreatedByActingIss:  p.CreatedByActing.Iss,
			CreatedByActingSub:  p.CreatedByActing.Sub,
			CreatedByActingKind: string(p.CreatedByActing.Kind),
			CreatedByOnBehalfIs: p.CreatedByOnBehalfOf.Iss,
			CreatedByOnBehalfSu: p.CreatedByOnBehalfOf.Sub,
			CreatedByOnBehalfKi: string(p.CreatedByOnBehalfOf.Kind),
			CreatedAt:           p.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		})
	}
	return ListNonGoalPromotionsResponse{Promotions: out}
}

// ResolveNonGoalHandler returns the resolve endpoint: POST
// /non-goals/{id}/resolve. Must be mounted behind RequireSession --
// scope_id and both LB4 subjects come from that session, never from the
// request, so a resolution is always attributed to a real caller (NFR6).
//
// Refusals map through the package's one writeStoreError switch rather
// than a bespoke mapping here, so a caller cannot see a different status
// for the same class of error depending on which endpoint it came in
// through: a `permanent` target is a 409 (the request was well-formed, the
// row is simply already settled), and a delivered target inherits void's
// 409 unchanged.
func ResolveNonGoalHandler(resolve store.ResolveStore) http.HandlerFunc {
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

		var req resolveRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		outcome := store.ResolveOutcome(req.Outcome)
		if outcome != store.ResolvePromote && outcome != store.ResolveRetire {
			writeJSONError(w, http.StatusBadRequest,
				fmt.Sprintf("outcome: must be one of %q, %q", store.ResolvePromote, store.ResolveRetire))
			return
		}

		resolved, err := resolve.ResolveNonGoal(r.Context(), sess.ScopeID, id, outcome, req.Reason, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		// A retire leaves no current row, so there is nothing to describe
		// beyond the id the caller already sent.
		if outcome == store.ResolveRetire {
			writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
			return
		}
		writeJSON(w, http.StatusOK, NewNonGoalWire(resolved))
	}
}
