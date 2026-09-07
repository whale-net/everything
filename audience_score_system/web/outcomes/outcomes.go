// Package outcomes is `web`'s Channel-scoped prediction-vs-outcome browse
// page (milestone M4.3, capability C10): serves GET /channels/{id}/outcomes,
// the `web` mirror of `get_prediction_vs_outcome`
// (audience_score_system/mcp/tools/browse.go). Handlers and templ views
// live together in this one package, mirroring web/matches's (which itself
// mirrors web/research's, which mirrors web/schedule's) package doc comment
// rationale: the read flow and its views are tightly coupled with no reuse
// outside this package.
//
// Read-only in this task (#1928, FR1, FR2, NFR2, plus the "View outcomes"
// half of FR11). The calibration-trend section (C14) and the outcome-bar
// save form (FR3-FR6) land on this SAME page in the follow-up task -- see
// that task's issue for why they depend on this one landing first. Per the
// plan's Out of scope, this package adds no new store method, migration, or
// schema change: the one read it needs, store.BrowseStore.
// PredictionVsOutcome, already exists and is the IDENTICAL call
// get_prediction_vs_outcome's handler makes (LB5 -- one read path, never a
// parallel one).
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/outcomes -- HandleList (FR1, FR2).
package outcomes

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Handlers holds the dependencies outcomes' route needs: the Store (for
// store.CanRead plus Browse()/Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleList serves GET /channels/{id}/outcomes (FR1, FR2). Stubbed for
// Scaffold -- this task's Implementation step replaces the body with the
// real auth/404/403 preamble (mirroring web/matches.Handlers.HandleList/
// research.Handlers.HandleChannelIndex exactly) plus the
// store.BrowseStore.PredictionVsOutcome read and views.templ's List render.
func (h *Handlers) HandleList(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
