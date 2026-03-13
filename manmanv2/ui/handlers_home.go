package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// HomePageData holds data for the home page
type HomePageData struct {
	Title  string
	Active string
	User   *htmxauth.UserInfo
}

// DashboardSummaryData is now in components package
// ActiveSessionInfo is now in components package
// PortInfo is now in components package

func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	layoutData, err := app.buildTemplLayoutData(r, "Dashboard", "home", user, nil)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Use templ to render the home page
	if err := RenderTempl(w, r, "Dashboard", pages.Home(layoutData)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		http.Error(w, "Failed to fetch servers", http.StatusInternalServerError)
		return
	}

	games, err := app.grpc.ListGames(ctx)
	if err != nil {
		log.Printf("Error fetching games: %v", err)
		http.Error(w, "Failed to fetch games", http.StatusInternalServerError)
		return
	}

	sessions, err := app.grpc.ListSessions(ctx, true)
	if err != nil {
		log.Printf("Error fetching sessions: %v", err)
		http.Error(w, "Failed to fetch sessions", http.StatusInternalServerError)
		return
	}

	onlineServers := 0
	for _, server := range servers {
		if server.Status == "online" {
			onlineServers++
		}
	}

	data := components.DashboardSummaryData{
		TotalServers:   len(servers),
		OnlineServers:  onlineServers,
		TotalGames:     len(games),
		ActiveSessions: len(sessions),
	}

	components.DashboardSummary(data).Render(r.Context(), w)
}

// PortInfo holds resolved port binding data for templates.
type PortInfo = components.PortInfo

// ActiveSessionInfo holds enriched session data for the dashboard.
type ActiveSessionInfo = components.ActiveSessionInfo

func (app *App) handleDashboardSessions(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	sessions, err := app.grpc.ListSessions(ctx, true)
	if err != nil {
		log.Printf("Error fetching sessions: %v", err)
		http.Error(w, "Failed to fetch sessions", http.StatusInternalServerError)
		return
	}

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		servers = []*manmanpb.Server{}
	}
	serverByID := make(map[int64]*manmanpb.Server, len(servers))
	for _, s := range servers {
		serverByID[s.ServerId] = s
	}

	sgcByID := make(map[int64]*manmanpb.ServerGameConfig)
	for _, server := range servers {
		sgcs, err := app.grpc.ListServerGameConfigs(ctx, server.ServerId)
		if err != nil {
			log.Printf("Error fetching SGCs for server %d: %v", server.ServerId, err)
			continue
		}
		for _, sgc := range sgcs {
			sgcByID[sgc.ServerGameConfigId] = sgc
		}
	}

	gameConfigByID := make(map[int64]*manmanpb.GameConfig)
	gameByID := make(map[int64]*manmanpb.Game)
	for _, sgc := range sgcByID {
		if _, ok := gameConfigByID[sgc.GameConfigId]; !ok {
			gc, err := app.grpc.GetGameConfig(ctx, sgc.GameConfigId)
			if err != nil {
				log.Printf("Error fetching game config %d: %v", sgc.GameConfigId, err)
				continue
			}
			gameConfigByID[gc.ConfigId] = gc
			if _, ok := gameByID[gc.GameId]; !ok {
				game, err := app.grpc.GetGame(ctx, gc.GameId)
				if err != nil {
					log.Printf("Error fetching game %d: %v", gc.GameId, err)
					continue
				}
				gameByID[game.GameId] = game
			}
		}
	}

	var enriched []ActiveSessionInfo
	for _, s := range sessions {
		info := ActiveSessionInfo{
			SessionID: s.SessionId,
			Status:    s.Status,
			Uptime:    computeUptime(s.StartedAt),
		}

		if sgc, ok := sgcByID[s.ServerGameConfigId]; ok {
			if server, ok := serverByID[sgc.ServerId]; ok {
				info.ServerName = server.Name
			}
			if gc, ok := gameConfigByID[sgc.GameConfigId]; ok {
				info.ConfigName = gc.Name
				if game, ok := gameByID[gc.GameId]; ok {
					info.GameName = game.Name
				}
			}
			for _, pb := range sgc.PortBindings {
				info.Ports = append(info.Ports, PortInfo{
					HostPort: pb.HostPort,
					Protocol: pb.Protocol,
				})
			}
		}

		if info.ServerName == "" {
			info.ServerName = fmt.Sprintf("SGC %d", s.ServerGameConfigId)
		}
		if info.GameName == "" {
			info.GameName = "Unknown Game"
		}
		if info.ConfigName == "" {
			info.ConfigName = fmt.Sprintf("Config %d", s.ServerGameConfigId)
		}

		enriched = append(enriched, info)
	}

	components.DashboardSessions(enriched).Render(r.Context(), w)
}

func computeUptime(startedAt int64) string {
	if startedAt == 0 {
		return "—"
	}
	d := time.Since(time.Unix(startedAt, 0))
	if d < time.Minute {
		return "< 1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours < 24 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	days := hours / 24
	hours = hours % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}

func (app *App) handleConfigStrategiesDocs(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	breadcrumbs := []components.Breadcrumb{
		{Label: "Configuration Strategies", URL: "/config-strategies"},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Configuration Strategies", "docs", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Configuration Strategies", pages.ConfigStrategiesDocs(layoutData)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
