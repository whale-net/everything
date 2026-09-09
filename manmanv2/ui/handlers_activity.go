package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// ActivityPageData holds data for the /activity page. Aliased from
// pages.ActivityPageData (pages/activity.templ) the same way
// SessionsPageData aliases pages.SessionsPageData in handlers_sessions.go.
type ActivityPageData = pages.ActivityPageData

// ActivityLiveRow and ActivityHistoryRow mirror the pages package's
// row types -- see pages/activity.templ for field-level doc comments,
// including the ground-truth note on TerminalStatus reading
// Session.status directly.
type ActivityLiveRow = pages.ActivityLiveRow
type ActivityHistoryRow = pages.ActivityHistoryRow

// handleActivity serves /activity (FR14, NFR6, NFR10, WD2, task #2271): a
// fleet-wide, observation-only view of what is running now (Live) and what
// ran before (History), built on ListSessions's live_only/status_filter
// filters -- never on resolveScopedServerGameConfigs's server-scoped
// derivation (handlers_sessions.go), which /sessions and its SSE stream
// use. Live updating over SSE is a follow-up task (#TBD); FR15 requires
// this page be correct without the stream, so handleActivity never depends
// on app.sseHub.
//
// Scaffold note (#2271): this is the route/page-structure skeleton --
// Live/History population from ListSessions, the fleet-wide authorized-set
// definition (NFR10: defined explicitly, never inherited from the
// server-scoped derivation; an empty authorized set must be a handled
// response, never a panic), and filter-driven narrowing are
// Implementation-phase work. The filter query params are parsed and
// echoed back into the view-model now so the round-trip contract
// (ActivityPageData.FilterGameID/FilterStatus) is already in place for
// Implementation to build on.
func (app *App) handleActivity(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	var filterGameID int64
	if gameIDStr := strings.TrimSpace(r.URL.Query().Get("game_id")); gameIDStr != "" {
		if id, err := strconv.ParseInt(gameIDStr, 10, 64); err == nil {
			filterGameID = id
		}
	}
	filterStatus := strings.TrimSpace(r.URL.Query().Get("status"))

	// TODO(#2271 Implementation): resolve the fleet-wide authorized set
	// (NFR10) and populate Live/History from ListSessions(live_only=true)
	// and ListSessions(status_filter=...) respectively, narrowed by
	// filterGameID/filterStatus. An empty authorized set must render an
	// empty (not erroring) page.
	data := ActivityPageData{
		Live:         nil,
		History:      nil,
		FilterGameID: filterGameID,
		FilterStatus: filterStatus,
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Activity", URL: "/activity"},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Activity", "Activity", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Activity", pages.Activity(layoutData, data)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
