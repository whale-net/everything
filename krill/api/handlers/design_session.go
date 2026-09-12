// This file (issue #2543, FR1/FR8) is the DesignSession HTTP surface:
// OpenDesignSessionHandler (write, gated) and GetDesignSessionHandler (read,
// ungated) over store.DesignSessionStore/store.RevisionEventStore. See
// revision_event.go for the append-RevisionEvent write endpoint and the
// wire types (RevisionEventWire, SubjectWire, ...) this file's
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

// openDesignSessionRequest is OpenDesignSessionHandler's request body (FR1,
// FR8). Deliberately has no entity-reference field of any kind -- FR8's
// whole point is that a Requirement Contributor contributes without
// knowing krill's entity model.
type openDesignSessionRequest struct {
	ProductID         string `json:"product_id"`
	OpeningSubmission string `json:"opening_submission"`
}

// OpenDesignSessionHandler returns the open-DesignSession endpoint (FR1,
// FR8): POST /design-sessions. Must be mounted behind RequireSession
// (gate.go) -- scope_id and opened_by_krill_session_id are always taken
// from the caller's session (LB1, FR1 "provenance only"), never accepted
// as request fields.
func OpenDesignSessionHandler(designSessions store.DesignSessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req openDesignSessionRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		productID, err := ParseUUIDField("product_id", req.ProductID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("opening_submission", req.OpeningSubmission); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		ds, err := designSessions.Open(r.Context(), sess.ScopeID, productID, req.OpeningSubmission, sess.SessionID)
		if err != nil {
			// designSessions.Open returns store.ErrNotFound (via
			// errParentNotFound) for an unknown or cross-scope product_id --
			// writeStoreError maps that to 400 with a named message, never a
			// raw pg error.
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: ds.ID.String()})
	}
}

// DesignSessionResponse is GetDesignSessionHandler's response body: the
// design_session row plus its ordered revision_event log. Exported (issue
// #2547) so krill/mcp/tools' get_design_session tool returns this exact
// value -- built by the same NewDesignSessionResponse this handler calls --
// rather than a second, MCP-local projection of the same data (LB7).
type DesignSessionResponse struct {
	ID                     string              `json:"id"`
	ProductID              string              `json:"product_id"`
	OpeningSubmission      string              `json:"opening_submission"`
	OpenedByKrillSessionID string              `json:"opened_by_krill_session_id"`
	CreatedAt              time.Time           `json:"created_at"`
	RevisionEvents         []RevisionEventWire `json:"revision_events"`
}

// GetDesignSessionHandler returns the read-DesignSession endpoint: GET
// /design-sessions/{id}. Never gated by RequireSession -- FR3's write-only
// gate applies to no read path in this milestone (root plan issue #2485),
// exactly like slice.go's four granularities and history.go's as-of reads.
func GetDesignSessionHandler(designSessions store.DesignSessionStore, events store.RevisionEventStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		ds, err := designSessions.GetByID(r.Context(), id)
		if err != nil {
			writeDesignSessionNotFoundOrInternalError(w, err)
			return
		}

		revisionEvents, err := events.ListBySession(r.Context(), id)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list revision events")
			return
		}

		writeJSON(w, http.StatusOK, NewDesignSessionResponse(ds, revisionEvents))
	}
}

// NewDesignSessionResponse builds a DesignSessionResponse from a
// store.DesignSession and its ordered store.RevisionEvent log. Exported
// (issue #2547) so krill/mcp/tools' get_design_session tool builds its
// response the same way this handler does -- one conversion, never a
// second one reimplemented in the MCP package (LB7).
func NewDesignSessionResponse(ds store.DesignSession, revisionEvents []store.RevisionEvent) DesignSessionResponse {
	out := make([]RevisionEventWire, len(revisionEvents))
	for i, ev := range revisionEvents {
		out[i] = ToRevisionEventWire(ev)
	}

	return DesignSessionResponse{
		ID:                     ds.ID.String(),
		ProductID:              ds.ProductID.String(),
		OpeningSubmission:      ds.OpeningSubmission,
		OpenedByKrillSessionID: uuid.UUID(ds.OpenedByKrillSessionID).String(),
		CreatedAt:              ds.CreatedAt,
		RevisionEvents:         out,
	}
}

// writeDesignSessionNotFoundOrInternalError maps a DesignSessionStore.
// GetByID error (or, from revision_event.go's AppendRevisionEventHandler, a
// RevisionEventStore.Append error against an unknown design_session id) onto
// this package's one JSON error shape: store.ErrNotFound -> 404, anything
// else -> 500.
func writeDesignSessionNotFoundOrInternalError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "design session not found")
		return
	}
	writeJSONError(w, http.StatusInternalServerError, "internal error")
}
