// This file (issue #2683, FR1/FR2, C13) is the milestone authoring HTTP
// surface: six endpoints over store.MilestoneAuthoringStore
// (krill/store/milestone_authoring.go) -- create a milestone, revise its
// FR budget, attach a Delivers/Must-not-foreclose association, record a
// deferral, and read a milestone back with its authoring fields and
// association/deferral lists. Every POST endpoint below must be mounted
// behind RequireSession (gate.go), same as every other M1/M2 write
// endpoint -- the LB4 subject pair always comes from the caller's
// session, never the request body. GET /milestones/{id} is ungated, same
// as every other read endpoint in this package.
package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// createMilestoneRequest is CreateMilestoneHandler's request body (FR1).
type createMilestoneRequest struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Outcome   string `json:"outcome"`
	FRBudget  *int   `json:"fr_budget"`
}

// setFRBudgetRequest is SetFRBudgetHandler's request body (FR2's revise
// path).
type setFRBudgetRequest struct {
	FRBudget int `json:"fr_budget"`
}

// addDeliversRequest is AddDeliversHandler's request body (LB6).
type addDeliversRequest struct {
	EntityID string `json:"entity_id"`
}

// addMustNotForecloseRequest is AddMustNotForecloseHandler's request body
// (LB6).
type addMustNotForecloseRequest struct {
	EntityID string `json:"entity_id"`
}

// addDeferralRequest is AddDeferralHandler's request body (FR1). Destination
// must be non-empty -- every deferred entry cites where it went.
type addDeferralRequest struct {
	Body        string `json:"body"`
	Destination string `json:"destination"`
}

// MilestoneResponse is GetMilestoneHandler's response body -- the
// milestone's authoring fields plus its Delivers/Must-not-foreclose
// entity id lists and its deferrals.
type MilestoneResponse struct {
	ID               string                  `json:"id"`
	ProductID        string                  `json:"product_id"`
	Name             string                  `json:"name"`
	Outcome          *string                 `json:"outcome"`
	FRBudget         *int                    `json:"fr_budget"`
	Delivers         []string                `json:"delivers"`
	MustNotForeclose []string                `json:"must_not_foreclose"`
	Deferrals        []MilestoneDeferralWire `json:"deferrals"`
}

// MilestoneDeferralWire is one entry of MilestoneResponse.Deferrals.
type MilestoneDeferralWire struct {
	Body        string `json:"body"`
	Destination string `json:"destination"`
}

// NewMilestoneResponse builds a MilestoneResponse from a
// store.MilestoneRef plus its two entity_milestone association lists and
// its deferrals -- exported so krill/mcp/tools' get_milestone tool builds
// its response the same way this handler does, never a second conversion
// (LB7).
func NewMilestoneResponse(ref store.MilestoneRef, delivers, mustNotForeclose []store.EntityMilestone, deferrals []store.MilestoneDeferral) MilestoneResponse {
	deliversIDs := make([]string, len(delivers))
	for i, m := range delivers {
		deliversIDs[i] = m.EntityID.String()
	}
	mustNotForecloseIDs := make([]string, len(mustNotForeclose))
	for i, m := range mustNotForeclose {
		mustNotForecloseIDs[i] = m.EntityID.String()
	}
	deferralWires := make([]MilestoneDeferralWire, len(deferrals))
	for i, d := range deferrals {
		deferralWires[i] = MilestoneDeferralWire{Body: d.Body, Destination: d.Destination}
	}

	return MilestoneResponse{
		ID:               ref.ID.String(),
		ProductID:        ref.ProductID.String(),
		Name:             ref.Name,
		Outcome:          ref.Outcome,
		FRBudget:         ref.FRBudget,
		Delivers:         deliversIDs,
		MustNotForeclose: mustNotForecloseIDs,
		Deferrals:        deferralWires,
	}
}

// CreateMilestoneHandler returns the milestone-create endpoint (FR1):
// POST /milestones. Must be mounted behind RequireSession (gate.go).
func CreateMilestoneHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		sess, ok := requireSessionOrInternalError(w, r)
		if !ok {
			return
		}

		var req createMilestoneRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		productID, err := ParseUUIDField("product_id", req.ProductID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("name", req.Name); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := RequireNonEmpty("outcome", req.Outcome); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		milestone, err := milestones.CreateMilestone(r.Context(), sess.ScopeID, productID, req.Name, req.Outcome, req.FRBudget, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: milestone.ID.String()})
	}
}

// SetFRBudgetHandler returns the FR-budget revise endpoint (FR2):
// POST /milestones/{id}/fr-budget. Must be mounted behind RequireSession.
func SetFRBudgetHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
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

		var req setFRBudgetRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if err := milestones.SetFRBudget(r.Context(), id, req.FRBudget, sess.Acting, sess.OnBehalfOf); err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
	}
}

// AddDeliversHandler returns the Delivers-attach endpoint (LB6): POST
// /milestones/{id}/delivers. Must be mounted behind RequireSession.
func AddDeliversHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
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

		var req addDeliversRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		entityID, err := ParseUUIDField("entity_id", req.EntityID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := milestones.AddDelivers(r.Context(), sess.ScopeID, id, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
	}
}

// AddMustNotForecloseHandler returns the Must-not-foreclose-attach
// endpoint (LB6): POST /milestones/{id}/must-not-foreclose. Must be
// mounted behind RequireSession.
func AddMustNotForecloseHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
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

		var req addMustNotForecloseRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		entityID, err := ParseUUIDField("entity_id", req.EntityID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := milestones.AddMustNotForeclose(r.Context(), sess.ScopeID, id, entityID, sess.Acting, sess.OnBehalfOf); err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, IDResponse{ID: id.String()})
	}
}

// AddDeferralHandler returns the deferral-record endpoint (FR1): POST
// /milestones/{id}/deferrals. Must be mounted behind RequireSession.
func AddDeferralHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
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

		var req addDeferralRequest
		if err := decodeStrict(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}
		if err := RequireNonEmpty("destination", req.Destination); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		deferral, err := milestones.AddDeferral(r.Context(), sess.ScopeID, id, req.Body, req.Destination, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			writeStoreError(w, err)
			return
		}

		writeJSON(w, http.StatusCreated, IDResponse{ID: deferral.ID.String()})
	}
}

// GetMilestoneHandler returns the milestone read endpoint: GET
// /milestones/{id}, ungated like every other read endpoint in this
// package.
func GetMilestoneHandler(milestones store.MilestoneAuthoringStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid id: must be a UUID")
			return
		}

		ref, delivers, mustNotForeclose, deferrals, err := milestones.GetMilestone(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusNotFound, "milestone not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, NewMilestoneResponse(ref, delivers, mustNotForeclose, deferrals))
	}
}
