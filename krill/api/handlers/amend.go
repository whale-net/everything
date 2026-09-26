// This file is the amend HTTP surface: the SCD2 close-and-open write
// endpoints for every spec-axis entity kind -- Product, FeatureSet,
// Feature, Requirement, Persona, NonGoal, LoadBearingDecision, and
// Milestone. store/amend.go's AmendStore is the only place the actual
// UPDATE+INSERT pair lives; these handlers are thin request/response
// adapters, gated by RequireSession (gate.go) like every other write
// endpoint.
//
// None of them accepts a parent id or a kind as something to apply. Each
// body embeds store.AmendPlacementChange purely so that a caller who tries
// to move or re-kind the entity gets a named refusal rather than a generic
// unknown-field decode error (FR f0f6bc18).
package handlers

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// amendRequest is the shape the three "name plus description" kinds share --
// a FeatureSet, a Feature, and a Persona carry nothing else amendable.
type amendRequest struct {
	store.AmendPlacementChange
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
}

// amendContentRequest is the shape the two "name plus body" kinds share --
// a Requirement and a NonGoal.
type amendContentRequest struct {
	store.AmendPlacementChange
	Name string  `json:"name"`
	Body *string `json:"body,omitempty"`
}

// amendProductRequest is a Product's shape: its amendable content is its
// name and its vision sentence.
type amendProductRequest struct {
	store.AmendPlacementChange
	Name   string `json:"name"`
	Vision string `json:"vision"`
}

// amendMilestoneRequest is a Milestone's shape: its amendable content is
// its name and its outcome sentence (FR 39373553). Its delivery axis --
// status transitions, Delivers, must-not-foreclose, deferrals -- is not
// addressable from here at all.
type amendMilestoneRequest struct {
	store.AmendPlacementChange
	Name    string  `json:"name"`
	Outcome *string `json:"outcome,omitempty"`
}

// beginAmend resolves the part of an amend request every handler shares --
// the POST method, the session gate, and the {id} path value -- and returns
// the entity to supersede.
func beginAmend(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return uuid.Nil, false
	}

	if _, ok := requireSessionOrInternalError(w, r); !ok {
		return uuid.Nil, false
	}

	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}

// decodeAmendBody decodes a kind-specific amend body into req and runs the
// checks every one of them shares: the placement guard (FR f0f6bc18) and a
// non-empty name. It writes the 400 itself and reports whether the handler
// may proceed. placement and name are read after the decode -- the first a
// pointer into the decoded body, the second a getter -- so neither can be
// evaluated against a still-empty request.
func decodeAmendBody(w http.ResponseWriter, r *http.Request, entityKind string, req any, placement *store.AmendPlacementChange, name func() string) bool {
	if err := decodeStrict(r, req); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return false
	}
	if err := placement.Refuse(entityKind); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := RequireNonEmpty("name", name()); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// finishAmend runs the store amend, mapping its error onto the package's
// one JSON error shape, and writes the (unchanged) surrogate id back.
func finishAmend(w http.ResponseWriter, err error, id uuid.UUID) {
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
}

// AmendProductHandler returns the amend-Product endpoint:
// POST /products/{id}/amend. Must be mounted behind RequireSession.
func AmendProductHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendProductRequest
		if !decodeAmendBody(w, r, "product", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		if err := RequireNonEmpty("vision", req.Vision); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		product, err := amend.AmendProduct(r.Context(), id, req.Name, req.Vision)
		finishAmend(w, err, product.ID)
	}
}

// AmendFeatureSetHandler returns the amend-FeatureSet endpoint:
// POST /feature-sets/{id}/amend. Must be mounted behind RequireSession.
func AmendFeatureSetHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendRequest
		if !decodeAmendBody(w, r, "feature set", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		amended, err := amend.AmendFeatureSet(r.Context(), id, req.Name, req.Description)
		finishAmend(w, err, amended.ID)
	}
}

// AmendFeatureHandler returns the amend-Feature endpoint:
// POST /features/{id}/amend. Must be mounted behind RequireSession.
func AmendFeatureHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendRequest
		if !decodeAmendBody(w, r, "feature", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		amended, err := amend.AmendFeature(r.Context(), id, req.Name, req.Description)
		finishAmend(w, err, amended.ID)
	}
}

// AmendRequirementHandler returns the amend-Requirement endpoint:
// POST /requirements/{id}/amend. Must be mounted behind RequireSession.
func AmendRequirementHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendContentRequest
		if !decodeAmendBody(w, r, "requirement", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		amended, err := amend.AmendRequirement(r.Context(), id, req.Name, req.Body)
		finishAmend(w, err, amended.ID)
	}
}

// AmendPersonaHandler returns the amend-Persona endpoint:
// POST /personas/{id}/amend. Must be mounted behind RequireSession.
func AmendPersonaHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendRequest
		if !decodeAmendBody(w, r, "persona", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		amended, err := amend.AmendPersona(r.Context(), id, req.Name, req.Description)
		finishAmend(w, err, amended.ID)
	}
}

// AmendNonGoalHandler returns the amend-NonGoal endpoint:
// POST /non-goals/{id}/amend. Must be mounted behind RequireSession.
func AmendNonGoalHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendContentRequest
		if !decodeAmendBody(w, r, "non-goal", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		amended, err := amend.AmendNonGoal(r.Context(), id, req.Name, req.Body)
		finishAmend(w, err, amended.ID)
	}
}

// AmendLoadBearingDecisionHandler returns the amend-LoadBearingDecision
// endpoint: POST /load-bearing-decisions/{id}/amend. Must be mounted
// behind RequireSession.
func AmendLoadBearingDecisionHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendContentRequest
		if !decodeAmendBody(w, r, "load-bearing decision", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		amended, err := amend.AmendLoadBearingDecision(r.Context(), id, req.Name, req.Body)
		finishAmend(w, err, amended.ID)
	}
}

// AmendMilestoneHandler returns the amend-Milestone endpoint:
// POST /milestones/{id}/amend. Must be mounted behind RequireSession.
func AmendMilestoneHandler(amend store.AmendStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := beginAmend(w, r)
		if !ok {
			return
		}
		var req amendMilestoneRequest
		if !decodeAmendBody(w, r, "milestone", &req, &req.AmendPlacementChange, func() string { return req.Name }) {
			return
		}
		amended, err := amend.AmendMilestone(r.Context(), id, req.Name, req.Outcome)
		finishAmend(w, err, amended.ID)
	}
}
