// Package handlers implements krill's HTTP surface over //krill/slice's
// four scoped-slice granularities (FR5-FR9, issue #2491). Every handler
// here is a thin adapter -- parse a path id, call the matching
// slice.Querier method, encode the resulting slice.Document -- never
// business logic itself; assembly lives in //krill/slice.
//
// None of these routes are gated by `init`/session: FR3's init gate is
// write-only, and read paths never require it (root plan issue #2485).
package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// Slice wires the four spec-slice query endpoints (FR5-FR8) over querier.
// Every route returns the same slice.Document shape (LB7) -- a caller
// never special-cases a granularity's response, only which fields of it
// are populated.
type Slice struct {
	querier *slice.Querier
}

// NewSlice returns a Slice backed by querier.
func NewSlice(querier *slice.Querier) *Slice {
	return &Slice{querier: querier}
}

// Register wires all four granularities onto mux.
func (h *Slice) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /slices/feature-sets/{id}", h.handle(h.querier.GetFeatureSetSlice))
	mux.HandleFunc("GET /slices/features/{id}", h.handle(h.querier.GetFeatureSlice))
	mux.HandleFunc("GET /slices/requirements/{id}", h.handle(h.querier.GetRequirementSlice))
	mux.HandleFunc("GET /slices/products/{id}", h.handle(h.querier.GetProductSlice))
}

// sliceQueryFunc is the signature every slice.Querier granularity method
// shares -- what lets one handle() implementation serve all four routes
// (FR9: one document type, four granularities) instead of a hand-written
// handler per endpoint that would only re-prove the same
// parse/call/encode sequence four times.
type sliceQueryFunc func(ctx context.Context, id uuid.UUID) (slice.Document, error)

// handle returns an http.HandlerFunc that parses the request's {id} path
// value, calls query with it, and encodes the resulting slice.Document --
// or maps a bad-id/not-found error to the matching HTTP status.
func (h *Slice) handle(query sliceQueryFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		doc, err := query(r.Context(), id)
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
