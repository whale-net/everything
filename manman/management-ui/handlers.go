package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handleHealth is the health check endpoint
func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"healthy"}`)
}

// handleHome renders the home page
func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get user ID (prefer sub, fallback to preferred_username)
	userID := user.Sub
	if userID == "" {
		userID = user.PreferredUsername
	}

	// Fetch worker ID and servers from Experience API
	workerID, err := app.getActiveWorkerID(r.Context(), userID)
	if err != nil {
		log.Printf("Failed to get worker ID: %v", err)
		workerID = ""
	}

	servers, err := app.getRunningServers(r.Context(), userID)
	if err != nil {
		log.Printf("Failed to get servers: %v", err)
		servers = []Server{}
	}

	// Render template
	data := HomePageData{
		User:     user,
		UserID:   userID,
		WorkerID: workerID,
		Servers:  servers,
	}

	if err := homeTemplate.Execute(w, data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleWorkerStatus returns HTMX fragment for worker status
func (app *App) handleWorkerStatus(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract user ID from URL path
	userID := strings.TrimPrefix(r.URL.Path, "/api/worker-status/")
	if userID == "" {
		userID = user.Sub
	}

	workerID, err := app.getActiveWorkerID(r.Context(), userID)
	if err != nil {
		log.Printf("Failed to get worker ID: %v", err)
		fmt.Fprint(w, `<span class="worker-inactive">Error loading worker</span>`)
		return
	}

	if workerID != "" {
		fmt.Fprintf(w, `<span class="worker-active">%s</span>`, workerID)
	} else {
		fmt.Fprint(w, `<span class="worker-inactive">No active worker</span>`)
	}
}

// handleServers returns HTMX fragment for server list
func (app *App) handleServers(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract user ID from URL path
	userID := strings.TrimPrefix(r.URL.Path, "/api/servers/")
	if userID == "" {
		userID = user.Sub
	}

	servers, err := app.getRunningServers(r.Context(), userID)
	if err != nil {
		log.Printf("Failed to get servers: %v", err)
		fmt.Fprint(w, `<tr><td colspan="3"><p class="no-servers">Error loading servers</p></td></tr>`)
		return
	}

	if len(servers) == 0 {
		fmt.Fprint(w, `<tr><td colspan="3"><p class="no-servers">No running servers</p></td></tr>`)
		return
	}

	// Render server rows
	for _, server := range servers {
		statusClass := "status-other"
		if server.Status == "running" {
			statusClass = "status-running"
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td><span class="%s">%s</span></td><td>%s:%s</td></tr>`,
			template.HTMLEscapeString(server.Name),
			statusClass,
			template.HTMLEscapeString(server.Status),
			template.HTMLEscapeString(server.IP),
			template.HTMLEscapeString(server.Port))
	}
}

// HomePageData holds data for the home page template
type HomePageData struct {
	User     *htmxauth.UserInfo
	UserID   string
	WorkerID string
	Servers  []Server
}

// Server represents a game server
type Server struct {
	ID     string
	Name   string
	Status string
	IP     string
	Port   string
}