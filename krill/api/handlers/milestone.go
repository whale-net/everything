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
//
// Scaffold stage: handler bodies are stubs returning 501; routes.go
// wiring and store-backed bodies land in this issue's Implementation
// phase.
package handlers

import "net/http"

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

// CreateMilestoneHandler will return the milestone-create endpoint (FR1):
// POST /milestones. Scaffold stub -- see this file's doc comment.
func CreateMilestoneHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "CreateMilestoneHandler: not implemented -- see issue #2683's Implementation phase")
	}
}

// SetFRBudgetHandler will return the FR-budget revise endpoint (FR2):
// POST /milestones/{id}/fr-budget. Scaffold stub -- see this file's doc
// comment.
func SetFRBudgetHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "SetFRBudgetHandler: not implemented -- see issue #2683's Implementation phase")
	}
}

// AddDeliversHandler will return the Delivers-attach endpoint (LB6): POST
// /milestones/{id}/delivers. Scaffold stub -- see this file's doc comment.
func AddDeliversHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "AddDeliversHandler: not implemented -- see issue #2683's Implementation phase")
	}
}

// AddMustNotForecloseHandler will return the Must-not-foreclose-attach
// endpoint (LB6): POST /milestones/{id}/must-not-foreclose. Scaffold
// stub -- see this file's doc comment.
func AddMustNotForecloseHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "AddMustNotForecloseHandler: not implemented -- see issue #2683's Implementation phase")
	}
}

// AddDeferralHandler will return the deferral-record endpoint (FR1): POST
// /milestones/{id}/deferrals. Scaffold stub -- see this file's doc
// comment.
func AddDeferralHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "AddDeferralHandler: not implemented -- see issue #2683's Implementation phase")
	}
}

// GetMilestoneHandler will return the milestone read endpoint: GET
// /milestones/{id}, ungated like every other read endpoint in this
// package. Scaffold stub -- see this file's doc comment.
func GetMilestoneHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSONError(w, http.StatusNotImplemented, "GetMilestoneHandler: not implemented -- see issue #2683's Implementation phase")
	}
}
