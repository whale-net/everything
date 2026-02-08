package main

import (
	"context"
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// HomePageData holds data for the home page
type HomePageData struct {
	Title  string
	Active string
	User   *htmxauth.UserInfo
}

// DashboardSummaryData holds dashboard summary statistics
type DashboardSummaryData struct {
	TotalServers   int
	OnlineServers  int
	TotalGames     int
	ActiveSessions int
}

func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	
	data := HomePageData{
		Title:  "Dashboard",
		Active: "home",
		User:   user,
	}
	
	if err := templates.ExecuteTemplate(w, "layout.html", data); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	
	// Fetch data from gRPC API
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
	
	sessions, err := app.grpc.ListSessions(ctx, true) // live only
	if err != nil {
		log.Printf("Error fetching sessions: %v", err)
		http.Error(w, "Failed to fetch sessions", http.StatusInternalServerError)
		return
	}
	
	// Count online servers
	onlineServers := 0
	for _, server := range servers {
		if server.Status == "online" {
			onlineServers++
		}
	}
	
	data := DashboardSummaryData{
		TotalServers:   len(servers),
		OnlineServers:  onlineServers,
		TotalGames:     len(games),
		ActiveSessions: len(sessions),
	}
	
	if err := templates.ExecuteTemplate(w, "dashboard_summary.html", data); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
