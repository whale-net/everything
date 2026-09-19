// This file (issue #2686, FR10) is the per-delivered-item shipment HTTP
// surface: POST /milestones/{id}/shipped (gated) over
// store.DeliveryShipmentStore.MarkShipped, and GET /milestones/{id}/delivery
// (ungated) over //krill/slice's GetDeliveryBreakdown -- FR10's "what
// shipped, what didn't" read for a `partially complete` milestone or
// milepebble, in one call. Both a MilestoneKindMilestone and a
// MilestoneKindMilepebble row are `milestone_ref` rows (FR9), so these
// same handlers serve both, mirroring milestone_status.go's own posture.
package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// markShippedRequest is MarkShippedHandler's request body (FR10). Note is
// optional.
type markShippedRequest struct {
	EntityID string  `json:"entity_id"`
	Note     *string `json:"note"`
}

// deliveryBreakdownQuerier is the one method of *slice.Querier this
// handler calls, narrowed to an interface for the same reason
// session_slice.go's entitySetQuerier is: *slice.Querier wraps a concrete
// *store.Store, so a handler that wants to be testable without Postgres
// cannot depend on the concrete type.
type deliveryBreakdownQuerier interface {
	GetDeliveryBreakdown(ctx context.Context, milestoneID uuid.UUID) (shipped, unshipped slice.Document, err error)
}

var _ deliveryBreakdownQuerier = (*slice.Querier)(nil)

// DeliveryBreakdownResponse is GetDeliveryBreakdownHandler's response body
// (FR10's acceptance shape): the container's current, derived status
// (migration 012) alongside the two Documents its delivered scope
// partitions into. Shipped/Unshipped reuse //krill/slice's existing typed
// entities rather than a bare id list or a new flattened shape (LB7).
type DeliveryBreakdownResponse struct {
	Status    string         `json:"status"`
	Shipped   slice.Document `json:"shipped"`
	Unshipped slice.Document `json:"unshipped"`
}

// MarkShippedHandler returns the per-item shipment endpoint (FR10): POST
// /milestones/{id}/shipped. Must be mounted behind RequireSession.
// Rejects an entity_id that is not one of {id}'s `delivers` associations
// (store.ErrEntityNotDelivered) with 409, mirroring
// AddMilepebbleDeliversHandler's own ErrMilepebbleDeliversNotSubset
// handling for the same "you cannot ship/associate what the container
// does not name" shape of rejection.
func MarkShippedHandler(shipments store.DeliveryShipmentStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		milestoneID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req markShippedRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		entityID, err := ParseUUIDField("entity_id", req.EntityID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := shipments.MarkShipped(r.Context(), sess.ScopeID, milestoneID, entityID, req.Note, sess.Acting, sess.OnBehalfOf); err != nil {
			if errors.Is(err, store.ErrEntityNotDelivered) {
				writeJSONError(w, http.StatusConflict, err.Error())
				return
			}
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: milestoneID.String()})
	}
}

// GetDeliveryBreakdownHandler returns the FR10 read endpoint: GET
// /milestones/{id}/delivery, ungated like every other read endpoint in
// this package. Combines the container's current status (statuses) with
// its shipped/unshipped breakdown (querier) into one response -- FR10's
// "one krill read answers what shipped, what didn't" acceptance shape.
func GetDeliveryBreakdownHandler(statuses store.MilestoneStatusEventStore, querier deliveryBreakdownQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		// CurrentStatus derives "not started" from the absence of a row
		// rather than validating milestone_ref existence (see its own doc
		// comment) -- a nonexistent id therefore reads back as "not
		// started" with an empty breakdown here too, same posture as
		// GetMilestoneStatusHandler.
		status, err := statuses.CurrentStatus(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		shipped, unshipped, err := querier.GetDeliveryBreakdown(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, DeliveryBreakdownResponse{
			Status:    string(status),
			Shipped:   shipped,
			Unshipped: unshipped,
		})
	}
}
