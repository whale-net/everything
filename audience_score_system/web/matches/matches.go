// Package matches is `web`'s Channel-scoped pending-matches browse page
// (milestone M4.3, capability C9): serves GET /channels/{id}/matches, the
// `web` mirror of `list_pending_matches`
// (audience_score_system/mcp/tools/matches.go). Handlers and templ views
// live together in this one package, mirroring web/research's (which
// itself mirrors web/schedule's) package doc comment rationale: the read
// flow and its views are tightly coupled with no reuse outside this
// package.
//
// Read-only in this issue (#1926, FR7/FR8/NFR2, plus the "Resolve pending
// matches" link half of FR11) -- the confirm/reject forms land in the
// follow-up task (FR9/FR10), which depends on this one. Per the plan's
// Out of scope, this package adds no new store method, migration, or
// schema change: every read it needs (store.MatchStore.ListPending plus
// the same store.SyncStore/store.VideoScriptStore/store.VerdictStore
// enrichment mcp/tools/matches.go's renderPendingMatch already performs)
// already exists.
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/matches -- HandleList (FR7, FR8).
package matches

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Handlers holds the dependencies matches' route needs: the Store (for
// store.CanRead plus Matches()/Sync()/VideoScripts()/Verdicts()/
// Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleList serves GET /channels/{id}/matches (FR7, FR8) -- scaffolded
// here as a compiling stub. The Implementation phase replaces this body
// with: the auth/404/403 preamble mirroring web/schedule.Handlers.
// HandleList and research.Handlers.HandleChannelIndex exactly (resolve
// signed-in Person, parse {id}, load the Channel via
// h.store.Channels().GetByID, then store.CanRead), the
// store.MatchStore.ListPending(ctx, channelID, nil, defaultPendingLimit)
// read (NFR2's fixed 50-row page, no "since"/paging), and per-match
// video/best-guess-script/confidence enrichment reproducing
// mcp/tools/matches.go's renderPendingMatch/renderMatchVideo/
// renderMatchScript against the same stores (LB5: no parallel query or
// new store method).
func (h *Handlers) HandleList(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
