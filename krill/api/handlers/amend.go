// This file (issue #2493, FR12) is the amend HTTP surface: the two SCD2
// close-and-open write endpoints over Requirement (FR/NFR) and
// LoadBearingDecision. store/amend.go's AmendStore is the only place the
// actual UPDATE+INSERT pair lives -- these handlers are thin request/
// response adapters, gated by RequireSession (gate.go) like every other
// write endpoint in this milestone. Neither handler accepts a parent id:
// amend never reparents (store/amend.go's doc comment), only replaces an
// existing entity's name/body under its unchanged surrogate id (LB2).
package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// amendRequest is the request body both amend endpoints below share: a
// content replacement (name, body) for the entity the path's {id} names.
type amendRequest struct {
	Name string  `json:"name"`
	Body *string `json:"body,omitempty"`
}

// AmendRequirementHandler returns the amend-Requirement endpoint (FR12):
// POST /requirements/{id}/amend. Must be mounted behind RequireSession.
func AmendRequirementHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if _, ok := requireSessionOrInternalError(w, r); !ok {
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req amendRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if err := requireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		requirement, err := amend.AmendRequirement(r.Context(), id, req.Name, req.Body)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, idResponse{ID: requirement.ID.String()})
	}
}

// AmendLoadBearingDecisionHandler returns the amend-LoadBearingDecision
// endpoint (FR12): POST /load-bearing-decisions/{id}/amend. Must be
// mounted behind RequireSession.
func AmendLoadBearingDecisionHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if _, ok := requireSessionOrInternalError(w, r); !ok {
			return
		}

		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		var req amendRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if err := requireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		decision, err := amend.AmendLoadBearingDecision(r.Context(), id, req.Name, req.Body)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, idResponse{ID: decision.ID.String()})
	}
}
