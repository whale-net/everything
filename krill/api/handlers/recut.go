// This file (issue #2687, FR5, C13) is the delivery-axis re-cut HTTP
// surface: POST /delivery/move (gated) over store.RecutStore.MoveScope,
// and GET /products/{id}/backlog (ungated) over //krill/slice's
// GetBacklog -- FR5's move primitive and FR5/FR6's backlog-bucket read,
// mirroring delivery_shipment.go's own write/read pairing for the same
// milestone-adjacent capability.
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

// moveScopeRequest is MoveScopeHandler's request body (FR5): the entities
// to move, and the from/to container ids -- each a milestone, a
// milepebble, or the backlog bucket (any `milestone_ref` row).
type moveScopeRequest struct {
	EntityIDs []string `json:"entity_ids"`
	From      string   `json:"from"`
	To        string   `json:"to"`
}

// MoveScopeResponse is MoveScopeHandler's response body: the entity ids
// actually moved, and the resolved from/to container ids -- confirming
// the whole batch (not a subset) landed in `to`.
type MoveScopeResponse struct {
	EntityIDs []string `json:"entity_ids"`
	From      string   `json:"from"`
	To        string   `json:"to"`
}

// MoveScopeHandler returns the re-cut endpoint (FR5): POST /delivery/move.
// Must be mounted behind RequireSession. Rejects, with no partial write
// (NFR3):
//   - an entity_id that is not a `from` Delivers association
//     (store.ErrEntityNotInContainer, 409);
//   - an entity_id already shipped in `from` (store.ErrEntityShipped, 409)
//     -- "truncate, never rewind" made tool-enforced;
//   - a move into a milepebble that would violate the FR3 subset
//     invariant (store.ErrMilepebbleDeliversNotSubset, 409);
//   - a move out of a milepebble to a competing milestone while a sibling
//     cut of the same parent still delivers the entity, which would leave
//     it with two milestone-level owners
//     (store.ErrEntityDeliveredBySiblingCut, 409).
func MoveScopeHandler(recut store.RecutStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req moveScopeRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if len(req.EntityIDs) == 0 {
			writeJSONError(w, http.StatusBadRequest, "entity_ids: at least one entity id is required")
			return
		}
		entityIDs := make([]uuid.UUID, len(req.EntityIDs))
		for i, raw := range req.EntityIDs {
			id, err := ParseUUIDField("entity_ids", raw)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			entityIDs[i] = id
		}

		from, err := ParseUUIDField("from", req.From)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		to, err := ParseUUIDField("to", req.To)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := recut.MoveScope(r.Context(), sess.ScopeID, entityIDs, from, to, sess.Acting, sess.OnBehalfOf); err != nil {
			if errors.Is(err, store.ErrEntityNotInContainer) ||
				errors.Is(err, store.ErrEntityShipped) ||
				errors.Is(err, store.ErrMilepebbleDeliversNotSubset) ||
			errors.Is(err, store.ErrEntityDeliveredBySiblingCut) {
				writeJSONError(w, http.StatusConflict, err.Error())
				return
			}
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, MoveScopeResponse{EntityIDs: req.EntityIDs, From: req.From, To: req.To})
	}
}

// backlogQuerier is the one method of *slice.Querier GetBacklogHandler
// calls, narrowed to an interface for the same reason
// deliveryBreakdownQuerier is (slice.go's own note on
// deliveryBreakdownQuerier applies here too).
type backlogQuerier interface {
	GetBacklog(ctx context.Context, productID uuid.UUID) (slice.Document, error)
}

var _ backlogQuerier = (*slice.Querier)(nil)

// GetBacklogHandler returns the backlog-bucket read endpoint (FR5/FR6):
// GET /products/{id}/backlog, ungated like every other read endpoint in
// this package.
func GetBacklogHandler(querier backlogQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		productID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		doc, err := querier.GetBacklog(r.Context(), productID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, doc)
	}
}
