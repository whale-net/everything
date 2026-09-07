// Package videos is `web`'s Channel-scoped published-videos listing page
// (capability C21, issue #2031, milestone re-anchor of #1960's FR27-FR30):
// serves GET /channels/{id}/videos -- every published SyncedVideo on the
// Channel with its latest recorded metrics, filterable by title substring,
// publish-date range, and sync status (FR28/FR29/FR30). Handlers and templ
// views live together in this one package, mirroring web/outcomes's (which
// mirrors web/research's) package doc comment rationale: the read flow and
// its views are tightly coupled with no reuse outside this package.
//
// Web-only in this milestone (no MCP mirror planned, per this task's
// issue): the one read this package needs, store.SyncStore.
// ListPublishedWithMetrics, is new in this task, added alongside
// SyncStore's existing ListSchedule (store/sync.go).
//
// Routes (mounted by ../main.go's setupRoutes, behind
// web/auth.Authenticator.RequireSignedIn):
//
//   - GET /channels/{id}/videos -- HandleList (FR27, FR28, FR29, FR30).
package videos

import (
	"net/http"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Handlers holds the dependencies videos' route needs: the Store (for
// store.CanRead plus Sync()/Channels()/Roles()).
type Handlers struct {
	store *store.Store
}

// New wires st into a Handlers.
func New(st *store.Store) *Handlers {
	return &Handlers{store: st}
}

// HandleList serves GET /channels/{id}/videos (FR27, FR28, FR29, FR30).
// Stubbed for Scaffold -- this task's Implementation step replaces the
// body with the real auth/404/403 preamble (mirroring
// outcomes.Handlers.HandleList/research.Handlers.HandleChannelIndex
// exactly, per this issue's Authorization section), query-param filter
// parsing (FR28/FR29), the store.SyncStore.ListPublishedWithMetrics read,
// and views.templ's List render.
func (h *Handlers) HandleList(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
