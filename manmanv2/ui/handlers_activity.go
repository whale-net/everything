package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
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

// activityTerminalStatuses is the complement of the live_only status set the
// API enforces server-side (postgres/session.go: "pending", "starting",
// "running", "stopping") -- i.e. Session.status values a session reaches
// once it is no longer live. History's default (unfiltered) query narrows
// to exactly this set so it never shows an in-flight session twice (once in
// Live, once in History).
var activityTerminalStatuses = []string{"stopped", "crashed", "completed"}

// resolveFleetWideActivitySet builds the fleet-wide authorized
// ServerGameConfig set (NFR10) Activity is scoped to: every SGC on every
// server, not the request's selected-server scope
// resolveScopedServerGameConfigs (handlers_sessions.go) resolves for
// /sessions and its SSE stream. Activity must define this explicitly rather
// than inherit that server-scoped derivation, per the issue's NFR10 note.
//
// A per-server ListServerGameConfigs failure is logged as a WARNING and
// skipped -- the same degrade-per-server posture handleDashboardSessions
// (handlers_home.go) already uses -- so one bad server never blanks the
// whole fleet-wide view. An empty return (nil maps) is a valid, handled
// result: handleActivity renders an empty page for it, never a panic.
func (app *App) resolveFleetWideActivitySet(ctx context.Context, servers []*manmanpb.Server) (sgcByID map[int64]*manmanpb.ServerGameConfig, serverByID map[int64]*manmanpb.Server) {
	serverByID = make(map[int64]*manmanpb.Server, len(servers))
	for _, server := range servers {
		serverByID[server.ServerId] = server
	}

	sgcByID = make(map[int64]*manmanpb.ServerGameConfig)
	for _, server := range servers {
		sgcs, err := app.grpc.ListServerGameConfigs(ctx, server.ServerId)
		if err != nil {
			log.Printf("WARNING: activity: failed to list server game configs for server %d: %v", server.ServerId, err)
			continue
		}
		for _, sgc := range sgcs {
			sgcByID[sgc.ServerGameConfigId] = sgc
		}
	}
	return sgcByID, serverByID
}

// activityDisplayNames resolves the Game/GameConfig display names for the
// fleet-wide authorized SGC set, batched and deduplicated by config/game id
// the same way handleDashboardSessions (handlers_home.go) does -- one
// GetGameConfig/GetGame call per distinct id, not one per SGC or session. A
// resolution failure for a single config/game is logged as a WARNING and
// that entry is simply absent from the returned maps; callers fall back to
// a placeholder display name rather than failing the page (FR2: no raw SGC
// id ever reaches display text, so the fallback still can't leak one).
func (app *App) activityDisplayNames(ctx context.Context, sgcByID map[int64]*manmanpb.ServerGameConfig) (gameConfigByID map[int64]*manmanpb.GameConfig, gameByID map[int64]*manmanpb.Game) {
	gameConfigByID = make(map[int64]*manmanpb.GameConfig)
	gameByID = make(map[int64]*manmanpb.Game)
	for _, sgc := range sgcByID {
		if _, ok := gameConfigByID[sgc.GameConfigId]; ok {
			continue
		}
		gc, err := app.grpc.GetGameConfig(ctx, sgc.GameConfigId)
		if err != nil {
			log.Printf("WARNING: activity: failed to get game config %d: %v", sgc.GameConfigId, err)
			continue
		}
		gameConfigByID[gc.ConfigId] = gc
		if _, ok := gameByID[gc.GameId]; ok {
			continue
		}
		game, err := app.grpc.GetGame(ctx, gc.GameId)
		if err != nil {
			log.Printf("WARNING: activity: failed to get game %d: %v", gc.GameId, err)
			continue
		}
		gameByID[game.GameId] = game
	}
	return gameConfigByID, gameByID
}

// activitySessionUptime computes an in-progress session's uptime from its
// own start timestamp (NFR6/LB2: uptime is derived from the session's own
// start, not from any container/direct-attach liveness concept). Zero when
// the session never recorded a start.
func activitySessionUptime(session *manmanpb.Session) time.Duration {
	if session.GetStartedAt() == 0 {
		return 0
	}
	return time.Since(time.Unix(session.GetStartedAt(), 0))
}

// activitySessionDuration computes a concluded session's run duration from
// its recorded start/end timestamps. Falls back to elapsed-since-start when
// the session has a start but no recorded end yet (e.g. a status transition
// raced the read), and to zero when it never started.
func activitySessionDuration(session *manmanpb.Session) time.Duration {
	if session.GetStartedAt() == 0 {
		return 0
	}
	start := time.Unix(session.GetStartedAt(), 0)
	if session.GetEndedAt() == 0 {
		return time.Since(start)
	}
	return time.Unix(session.GetEndedAt(), 0).Sub(start)
}

// handleActivity serves /activity (FR14, NFR6, NFR10, WD2, task #2271): a
// fleet-wide, observation-only view of what is running now (Live) and what
// ran before (History), built on ListSessions's live_only/status_filter
// filters -- never on resolveScopedServerGameConfigs's server-scoped
// derivation (handlers_sessions.go), which /sessions and its SSE stream
// use. Live updating over SSE is a follow-up task (#TBD); FR15 requires
// this page be correct without the stream, so handleActivity never depends
// on app.sseHub.
//
// NFR10: Live/History are filtered to resolveFleetWideActivitySet's
// authorized SGC set before rendering, defence-in-depth against a session
// whose SGC fell outside that set (deleted mid-request, etc.) ever
// reaching the page. An empty authorized set naturally yields empty
// Live/History slices -- a handled response, not a panic or error.
func (app *App) handleActivity(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	var filterGameID int64
	if gameIDStr := strings.TrimSpace(r.URL.Query().Get("game_id")); gameIDStr != "" {
		if id, err := strconv.ParseInt(gameIDStr, 10, 64); err == nil {
			filterGameID = id
		}
	}
	filterStatus := strings.TrimSpace(r.URL.Query().Get("status"))

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("WARNING: activity: failed to list servers: %v", err)
		servers = []*manmanpb.Server{}
	}

	sgcByID, serverByID := app.resolveFleetWideActivitySet(ctx, servers)
	gameConfigByID, gameByID := app.activityDisplayNames(ctx, sgcByID)

	// activityRowLabels resolves one session's display fields against the
	// authorized set. ok is false when the session's SGC fell outside the
	// fleet-wide authorized set (NFR10) or when the game filter excludes it
	// (FilterGameID has no ListSessions-side filter -- ListSessionsRequest
	// carries no game_id -- so this is applied client-side here).
	activityRowLabels := func(session *manmanpb.Session) (gameName, serverName, configName string, ok bool) {
		sgc, found := sgcByID[session.ServerGameConfigId]
		if !found {
			return "", "", "", false
		}
		gc := gameConfigByID[sgc.GameConfigId]
		if filterGameID > 0 && (gc == nil || gc.GameId != filterGameID) {
			return "", "", "", false
		}
		configName = "Unknown Config"
		if gc != nil {
			configName = gc.Name
			if game, gameOK := gameByID[gc.GameId]; gameOK {
				gameName = game.Name
			}
		}
		if gameName == "" {
			gameName = "Unknown Game"
		}
		serverName = "Unknown Server"
		if server, serverOK := serverByID[sgc.ServerId]; serverOK {
			serverName = server.Name
		}
		return gameName, serverName, configName, true
	}

	// Live now (FR14): ListSessions(live_only=true), fleet-wide (no
	// ServerId set on the request -- that is what /sessions's
	// server-scoped view sets, not Activity).
	liveReq := &manmanpb.ListSessionsRequest{
		PageSize: 200,
		LiveOnly: true,
	}
	if filterStatus != "" {
		liveReq.StatusFilter = []string{filterStatus}
	}
	var liveRows []ActivityLiveRow
	liveSessions, err := app.grpc.ListSessionsWithFilters(ctx, liveReq)
	if err != nil {
		log.Printf("WARNING: activity: failed to list live sessions: %v", err)
	}
	for _, session := range liveSessions {
		gameName, serverName, configName, ok := activityRowLabels(session)
		if !ok {
			continue
		}
		liveRows = append(liveRows, ActivityLiveRow{
			GameName:   gameName,
			ServerName: serverName,
			ConfigName: configName,
			Uptime:     activitySessionUptime(session),
			SessionID:  session.SessionId,
		})
	}

	// History (FR14): ListSessions(status_filter=...), fleet-wide. Default
	// (no status filter selected) narrows to activityTerminalStatuses so a
	// still-live session is never double-counted into History.
	historyReq := &manmanpb.ListSessionsRequest{
		PageSize: 200,
	}
	if filterStatus != "" {
		historyReq.StatusFilter = []string{filterStatus}
	} else {
		historyReq.StatusFilter = activityTerminalStatuses
	}
	var historyRows []ActivityHistoryRow
	historySessions, err := app.grpc.ListSessionsWithFilters(ctx, historyReq)
	if err != nil {
		log.Printf("WARNING: activity: failed to list history sessions: %v", err)
	}
	for _, session := range historySessions {
		gameName, serverName, configName, ok := activityRowLabels(session)
		if !ok {
			continue
		}
		historyRows = append(historyRows, ActivityHistoryRow{
			GameName:       gameName,
			ServerName:     serverName,
			ConfigName:     configName,
			TerminalStatus: session.Status,
			StartTime:      time.Unix(session.GetStartedAt(), 0),
			Duration:       activitySessionDuration(session),
			SessionID:      session.SessionId,
		})
	}

	data := ActivityPageData{
		Live:         liveRows,
		History:      historyRows,
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
