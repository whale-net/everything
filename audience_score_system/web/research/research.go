// Package research is `web`'s read-only Loop 1 browse surface (milestone
// M4.1, FR1/FR2/FR8/FR9/FR10, issue #1899): a Channel-scoped research
// index (every Idea with its note count and verdict presence, plus a
// separate section for research notes that predate any Idea, M1 FR9) and
// a per-Idea detail page (that Idea's research notes, most-recent first,
// and its full viability-verdict history). Handlers and templ views live
// together in this one package, mirroring web/schedule.Handlers' package
// doc comment rationale: the read flow and its two views are tightly
// coupled with no reuse outside this package.
//
// This task ships no POST route and no form anywhere in this package --
// FR3/FR4/FR6/FR7's save forms land on these same pages in follow-up
// tasks, dropped into the page structure built here without
// restructuring it.
//
// Authorization (NFR2, NFR5): both routes are visible to a Channel's
// Founder, Co-Creator, AND Analyst (store.CanRead) -- mirrors
// web/schedule.Handlers.HandleList's read gate exactly, since Loop 1
// browse carries the same three-tier read visibility as schedule
// approval's read side. Every authorization decision is re-derived fresh
// from the {id} path segment on every call, never cached or trusted from
// the session alone.
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/research -- HandleChannelIndex (FR1).
//   - GET /channels/{id}/research/ideas/{ideaID} -- HandleIdeaDetail
//     (FR2, FR9, FR10).
package research

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Handlers holds the dependencies research's routes need: the Store (for
// store.CanRead and Ideas()/Research()/Verdicts()/Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleChannelIndex serves GET /channels/{id}/research (FR1's read
// side): every Idea on the Channel with its note count and verdict
// presence, plus a separate unattached-notes section (M1 FR9). Visible to
// Founder, Co-Creator, and Analyst alike (store.CanRead).
//
// TODO(#1899, Implementation phase): stubbed for Scaffold.
func (h *Handlers) HandleChannelIndex(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// HandleIdeaDetail serves GET /channels/{id}/research/ideas/{ideaID}
// (FR2, FR9, FR10): that Idea's research notes (most-recent first) and
// its current verdict plus full version history -- the identical pair of
// store.VerdictStore calls get_viability_verdict makes, so `web` and
// `mcp` can never disagree on which version is current.
//
// TODO(#1899, Implementation phase): stubbed for Scaffold.
func (h *Handlers) HandleIdeaDetail(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
