package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/generated/go/manman/experience_api"
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

// handleAvailableServers returns HTMX fragment for available server configurations
func (app *App) handleAvailableServers(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get all server configs
	configs, err := app.getAllGameServerConfigs(r.Context())
	if err != nil {
		log.Printf("Failed to get game server configs: %v", err)
		configs = []experience_api.GameServerConfig{}
	}

	// Get currently running servers with full response (includes crashed servers)
	resp, err := app.getCurrentServersWithConfigs(r.Context(), user.Sub)
	if err != nil {
		log.Printf("Failed to get current servers: %v", err)
		resp = &experience_api.CurrentInstanceResponse{
			GameServerInstances: []experience_api.GameServerInstance{},
			Workers:             []experience_api.Worker{},
			Configs:             []experience_api.GameServerConfig{},
		}
	}

	// Create a map of running config IDs (exclude crashed servers - they can be restarted)
	runningConfigIDs := make(map[int32]bool)
	for _, inst := range resp.GameServerInstances {
		// Only mark as running if the instance is actually active (no end_date)
		isActive := !inst.EndDate.IsSet() || inst.EndDate.Get() == nil
		if isActive {
			runningConfigIDs[inst.GameServerConfigId] = true
		}
	}

	// Build available servers list
	availableServers := make([]AvailableServer, 0, len(configs))
	for _, config := range configs {
		isRunning := runningConfigIDs[config.GameServerConfigId]

		availableServers = append(availableServers, AvailableServer{
			ConfigID:  strconv.Itoa(int(config.GameServerConfigId)),
			Name:      config.Name,
			IsRunning: isRunning,
		})
	}

	data := AvailableServersData{
		Servers: availableServers,
	}

	if err := templates.ExecuteTemplate(w, "available_servers.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleStartServer handles the start server action
func (app *App) handleStartServer(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get config ID from form
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	configIDStr := r.FormValue("config_id")
	if configIDStr == "" {
		http.Error(w, "Missing config_id", http.StatusBadRequest)
		return
	}

	configID, err := strconv.ParseInt(configIDStr, 10, 32)
	if err != nil {
		http.Error(w, "Invalid config_id", http.StatusBadRequest)
		return
	}

	// Start the server
	if err := app.startGameServer(r.Context(), int32(configID)); err != nil {
		log.Printf("Failed to start server: %v", err)
		http.Error(w, fmt.Sprintf("Failed to start server: %v", err), http.StatusInternalServerError)
		return
	}

	// Return success - HTMX will handle the response
	w.Header().Set("HX-Trigger", "serverStarted")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Server start command sent successfully")
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

// AvailableServer represents a game server configuration that can be started
type AvailableServer struct {
	ConfigID  string
	Name      string
	IsRunning bool
}

// AvailableServersData holds data for available servers template
type AvailableServersData struct {
	Servers []AvailableServer
}
