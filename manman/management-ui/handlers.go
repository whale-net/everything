package main

import (
	"fmt"
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

	// Render template
	data := HomePageData{
		User: user,
	}

	if err := templates.ExecuteTemplate(w, "home.html", data); err != nil {
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
	status := "inactive"
	statusType := ""
	lastHeartbeat := ""

	if err != nil {
		log.Printf("Failed to get worker ID: %v", err)
		workerID = "Error"
		status = "error"
	} else if workerID != "" {
		status = "active"

		// Try to get detailed status information
		workerStatus, statusErr := app.getWorkerStatus(r.Context(), userID)
		if statusErr != nil {
			log.Printf("Failed to get worker status: %v", statusErr)
		} else if workerStatus != nil {
			statusType = string(workerStatus.StatusType)
			if workerStatus.AsOf != nil {
				lastHeartbeat = workerStatus.AsOf.Format("2006-01-02 15:04:05")
			}
		}
	} else {
		workerID = "No active worker"
	}

	data := WorkerStatusData{
		WorkerID:      workerID,
		Status:        status,
		StatusType:    statusType,
		LastHeartbeat: lastHeartbeat,
	}

	if err := templates.ExecuteTemplate(w, "worker_status.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
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
		servers = []Server{}
	}

	data := ServersData{
		Servers: servers,
	}

	if err := templates.ExecuteTemplate(w, "servers.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// HomePageData holds data for the home page template
type HomePageData struct {
	User *htmxauth.UserInfo
}

// WorkerStatusData holds data for worker status template
type WorkerStatusData struct {
	WorkerID      string
	Status        string
	StatusType    string
	LastHeartbeat string
}

// ServersData holds data for servers template
type ServersData struct {
	Servers []Server
}

// Server represents a game server
type Server struct {
	ID         string
	InstanceID string
	Name       string
	Status     string
	StatusType string
	IP         string
	Port       string
}
