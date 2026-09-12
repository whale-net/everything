// This file (issue #2544, krill M2, FR5) is the HTTP surface over
// krill/slice's fifth granularity, GetEntitySetSlice: it bridges
// store.RevisionEventStore's append-only round log to krill/slice's
// id-set query, so krill/slice itself never has to import M2's session
// tables (see query.go's doc comment on GetEntitySetSlice). Never gated
// by RequireSession (gate.go) -- a read path never requires `init` (root
// plan issue #2485), exactly like slice.go's four M1 granularities.
package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// entitySetQuerier is the one method of *slice.Querier this handler calls,
// narrowed to an interface so session_slice_test.go can supply a fake that
// records its calls -- slice.Querier is a concrete type wrapping
// *store.Store, unlike every store.*Store interface this package already
// fakes, so this handler cannot depend on the concrete type and stay
// testable without Postgres.
type entitySetQuerier interface {
	GetEntitySetSlice(ctx context.Context, entityIDs []uuid.UUID) (slice.Document, error)
}

var _ entitySetQuerier = (*slice.Querier)(nil)

// SessionSlice wires GET /design-sessions/{id}/slice: FR5's "current
// draft" reconstruction for one design session.
type SessionSlice struct {
	sessions store.DesignSessionStore
	events   store.RevisionEventStore
	querier  entitySetQuerier
}

// NewSessionSlice returns a SessionSlice backed by sessions, events, and
// querier. querier is typed as entitySetQuerier (unexported) rather than
// *slice.Querier so session_slice_test.go can construct one over a fake
// that records its calls -- any caller, in or out of this package, may
// still pass a *slice.Querier value here without naming the interface
// itself (Go's implicit interface satisfaction).
func NewSessionSlice(sessions store.DesignSessionStore, events store.RevisionEventStore, querier entitySetQuerier) *SessionSlice {
	return &SessionSlice{sessions: sessions, events: events, querier: querier}
}

// Register wires GET /design-sessions/{id}/slice onto mux.
func (h *SessionSlice) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /design-sessions/{id}/slice", h.handle)
}

// handle parses {id}, confirms the design_session exists (404 if not),
// unions every entity_id across every one of the session's revision_event
// rows (deduplicated), and calls entitySetQuerier.GetEntitySetSlice with
// that set. A session with no events yields an empty slice.Document, 200
// -- GetEntitySetSlice's own empty-input contract, not a special case
// here.
func (h *SessionSlice) handle(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
		return
	}

	if _, err := h.sessions.GetByID(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	events, err := h.events.ListBySession(r.Context(), id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	doc, err := h.querier.GetEntitySetSlice(r.Context(), UnionEntityDeltaIDs(events))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

// UnionEntityDeltaIDs returns the deduplicated union of every entity_id
// named by any entity_delta of any of events, in first-seen order --
// exactly the "union of every entity_id across every event's
// entity_deltas" this issue's HTTP route section specifies. A nil/empty
// events yields a nil slice, which GetEntitySetSlice treats as an empty
// input set, not an error. Exported (issue #2547) so krill/mcp/tools'
// get_design_session_slice tool computes the same id set this handler
// does, rather than a second, MCP-local reimplementation.
func UnionEntityDeltaIDs(events []store.RevisionEvent) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{})
	var ids []uuid.UUID
	for _, ev := range events {
		for _, delta := range ev.EntityDeltas {
			if _, ok := seen[delta.EntityID]; ok {
				continue
			}
			seen[delta.EntityID] = struct{}{}
			ids = append(ids, delta.EntityID)
		}
	}
	return ids
}
