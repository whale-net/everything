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
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/web/auth"
	"github.com/whale-net/everything/audience_score_system/web/components"
)

// defaultOutcomesLimit bounds HandleList's response to the same fixed page
// size as mcp/tools/browse.go's defaultPredictionVsOutcomeLimit, per NFR2:
// no since/before/limit query parameter, no "load more" control exists
// anywhere in this package. truncated (from PredictionVsOutcome) is
// surfaced in views.templ as a static note, never a paging control.
const defaultOutcomesLimit = 25

// Handlers holds the dependencies outcomes' route needs: the Store (for
// store.CanRead plus Browse()/Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleList serves GET /channels/{id}/outcomes (FR1, FR2). The
// auth/404/403 preamble mirrors web/matches.Handlers.HandleList/
// research.Handlers.HandleChannelIndex exactly (load-bearing, not
// stylistic, per this issue's body): resolve the signed-in Person (401),
// parse {id} (400), load the Channel (404 on pgx.ErrNoRows -- an unknown
// Channel 404s before authorization can turn it into a 403), then
// store.CanRead (403). Rows come from store.BrowseStore.
// PredictionVsOutcome -- the exact same store call
// getPredictionVsOutcome's handler makes, with no parallel query (LB5).
func (h *Handlers) HandleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	person := auth.PersonFromContext(ctx)
	if person == nil {
		http.Error(w, "not signed in", http.StatusUnauthorized)
		return
	}

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid channel id", http.StatusBadRequest)
		return
	}

	ch, err := h.store.Channels().GetByID(ctx, channelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canRead, err := store.CanRead(ctx, h.store.Roles(), channelID, person.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !canRead {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	rows, truncated, err := h.store.Browse().PredictionVsOutcome(ctx, channelID, nil /* ideaID */, nil /* since */, nil /* before */, defaultOutcomesLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	title := ch.Title + " outcomes"
	data := components.LayoutData{Title: title, User: person}
	if err := components.Render(w, r, title, List(data, ch, rows, truncated)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
