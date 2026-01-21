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

// handleInstancePage renders the instance detail page
func (app *App) handleInstancePage(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract instance ID from URL path
	instanceIDStr := strings.TrimPrefix(r.URL.Path, "/instance/")
	if instanceIDStr == "" {
		http.Error(w, "Missing instance ID", http.StatusBadRequest)
		return
	}

	instanceID, err := strconv.Atoi(instanceIDStr)
	if err != nil {
		http.Error(w, "Invalid instance ID", http.StatusBadRequest)
		return
	}

	// Get instance details
	details, err := app.getInstanceDetails(r.Context(), instanceID)
	if err != nil {
		log.Printf("Failed to get instance details: %v", err)
		http.Error(w, "Failed to load instance details", http.StatusInternalServerError)
		return
	}

	// Prepare data for template
	data := InstancePageData{
		User:     user,
		Instance: *details,
	}

	if err := templates.ExecuteTemplate(w, "instance.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleExecuteCommand handles command execution
func (app *App) handleExecuteCommand(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	instanceIDStr := r.FormValue("instance_id")
	commandType := r.FormValue("command_type")
	commandIDStr := r.FormValue("command_id")
	customValue := r.FormValue("custom_value")

	instanceID, err := strconv.Atoi(instanceIDStr)
	if err != nil {
		http.Error(w, "Invalid instance ID", http.StatusBadRequest)
		return
	}

	commandID, err := strconv.Atoi(commandIDStr)
	if err != nil {
		http.Error(w, "Invalid command ID", http.StatusBadRequest)
		return
	}

	// Build request
	request := experience_api.ExecuteCommandRequest{
		CommandType: commandType,
		CommandId:   int32(commandID),
	}
	if customValue != "" {
		request.CustomValue = *experience_api.NewNullableString(&customValue)
	}

	// Execute command
	response, err := app.executeInstanceCommand(r.Context(), instanceID, request)
	if err != nil {
		log.Printf("Failed to execute command: %v", err)
		http.Error(w, fmt.Sprintf("Failed to execute command: %v", err), http.StatusInternalServerError)
		return
	}

	// Return success message
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<div class="success-message">%s</div>`, response.Message)
}

// handleAddCommandModal renders the add command modal
func (app *App) handleAddCommandModal(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get game_server_id from query params
	gameServerIDStr := r.URL.Query().Get("game_server_id")
	configIDStr := r.URL.Query().Get("config_id")

	gameServerID, err := strconv.Atoi(gameServerIDStr)
	if err != nil {
		http.Error(w, "Invalid game_server_id", http.StatusBadRequest)
		return
	}

	configID, err := strconv.Atoi(configIDStr)
	if err != nil {
		http.Error(w, "Invalid config_id", http.StatusBadRequest)
		return
	}

	// Get available commands
	commands, err := app.getAvailableCommands(r.Context(), gameServerID)
	if err != nil {
		log.Printf("Failed to get available commands: %v", err)
		http.Error(w, "Failed to load commands", http.StatusInternalServerError)
		return
	}

	data := AddCommandModalData{
		Commands: commands,
		ConfigID: configID,
	}

	if err := templates.ExecuteTemplate(w, "add_command_modal.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleCreateCommand creates a new config command
func (app *App) handleCreateCommand(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	configIDStr := r.FormValue("config_id")
	commandIDStr := r.FormValue("command_id")
	commandValue := r.FormValue("command_value")
	description := r.FormValue("description")

	configID, err := strconv.Atoi(configIDStr)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}

	commandID, err := strconv.Atoi(commandIDStr)
	if err != nil {
		http.Error(w, "Invalid command ID", http.StatusBadRequest)
		return
	}

	// Build inline request body
	request := experience_api.NewBodyCreateConfigCommandGameserverConfigConfigIdCommandPost(int32(commandID), commandValue)
	if description != "" {
		request.Description = *experience_api.NewNullableString(&description)
	}

	// Create command
	_, err = app.createConfigCommand(r.Context(), configID, *request)
	if err != nil {
		log.Printf("Failed to create command: %v", err)
		http.Error(w, fmt.Sprintf("Failed to create command: %v", err), http.StatusInternalServerError)
		return
	}

	// Trigger page refresh
	w.Header().Set("HX-Trigger", "commandCreated")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Command created successfully")
}

// InstancePageData holds data for the instance page
type InstancePageData struct {
	User     *htmxauth.UserInfo
	Instance experience_api.InstanceDetailsResponse
}

// AddCommandModalData holds data for the add command modal
type AddCommandModalData struct {
	Commands []experience_api.GameServerCommand
	ConfigID int
}

// handleGameServerPage renders the game server detail page
func (app *App) handleGameServerPage(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract game server ID from URL path
	gameServerIDStr := strings.TrimPrefix(r.URL.Path, "/gameserver/")
	if gameServerIDStr == "" {
		http.Error(w, "Missing game server ID", http.StatusBadRequest)
		return
	}

	gameServerID, err := strconv.Atoi(gameServerIDStr)
	if err != nil {
		http.Error(w, "Invalid game server ID", http.StatusBadRequest)
		return
	}

	// Get game server details (from the list, since we don't have a get endpoint yet)
	servers, err := app.getAllGameServers(r.Context())
	if err != nil {
		log.Printf("Failed to get game servers: %v", err)
		http.Error(w, "Failed to load game server", http.StatusInternalServerError)
		return
	}

	var gameServer *experience_api.GameServer
	for _, s := range servers {
		if int(s.GameServerId) == gameServerID {
			gameServer = &s
			break
		}
	}

	if gameServer == nil {
		http.Error(w, "Game server not found", http.StatusNotFound)
		return
	}

	// Get commands for this game server
	commands, err := app.getGameServerCommands(r.Context(), int32(gameServerID))
	if err != nil {
		log.Printf("Failed to get commands: %v", err)
		commands = []experience_api.GameServerCommand{}
	}

	// Get all configs and filter by this game server ID
	allConfigs, err := app.getAllGameServerConfigs(r.Context())
	if err != nil {
		log.Printf("Failed to get configs: %v", err)
		allConfigs = []experience_api.GameServerConfig{}
	}

	var configs []experience_api.GameServerConfig
	for _, cfg := range allConfigs {
		if int(cfg.GameServerId) == gameServerID {
			configs = append(configs, cfg)
		}
	}

	// Get instance history
	history, err := app.getGameServerInstanceHistory(r.Context(), int32(gameServerID), 10)
	if err != nil {
		log.Printf("Failed to get instance history: %v", err)
		history = []InstanceHistoryItem{}
	}

	// Prepare data for template
	data := GameServerPageData{
		User:            user,
		GameServer:      *gameServer,
		Commands:        commands,
		Configs:         configs,
		InstanceHistory: history,
	}

	if err := templates.ExecuteTemplate(w, "gameserver.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleGameServersList renders the game servers list page
func (app *App) handleGameServersList(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get all game servers
	servers, err := app.getAllGameServers(r.Context())
	if err != nil {
		log.Printf("Failed to get game servers: %v", err)
		servers = []experience_api.GameServer{}
	}

	data := GameServersListPageData{
		User:        user,
		GameServers: servers,
	}

	if err := templates.ExecuteTemplate(w, "gameservers_list.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleAddGameServerCommandModal returns the modal HTML for adding a command to a game server
func (app *App) handleAddGameServerCommandModal(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	gameServerIDStr := r.URL.Query().Get("game_server_id")
	gameServerID, err := strconv.Atoi(gameServerIDStr)
	if err != nil {
		http.Error(w, "Invalid game server ID", http.StatusBadRequest)
		return
	}

	data := AddGameServerCommandModalData{
		GameServerID: gameServerID,
	}

	if err := templates.ExecuteTemplate(w, "add_gameserver_command_modal.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleCreateGameServerCommand handles creating a new command for a game server
func (app *App) handleCreateGameServerCommand(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse form data
	gameServerIDStr := r.FormValue("game_server_id")
	name := r.FormValue("name")
	command := r.FormValue("command")
	description := r.FormValue("description")
	isVisible := r.FormValue("is_visible") == "on"

	gameServerID, err := strconv.Atoi(gameServerIDStr)
	if err != nil {
		http.Error(w, "Invalid game server ID", http.StatusBadRequest)
		return
	}

	if name == "" || command == "" {
		http.Error(w, "Name and command are required", http.StatusBadRequest)
		return
	}

	// Create command
	_, err = app.createGameServerCommand(r.Context(), int32(gameServerID), name, command, description, isVisible)
	if err != nil {
		log.Printf("Failed to create command: %v", err)
		http.Error(w, fmt.Sprintf("Failed to create command: %v", err), http.StatusInternalServerError)
		return
	}

	// Set trigger header and respond
	w.Header().Set("HX-Trigger", "commandCreated")
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Command created successfully")
}

// GameServerPageData holds data for the game server detail page
type GameServerPageData struct {
	User            *htmxauth.UserInfo
	GameServer      experience_api.GameServer
	Commands        []experience_api.GameServerCommand
	Configs         []experience_api.GameServerConfig
	InstanceHistory []InstanceHistoryItem
}

// GameServersListPageData holds data for the game servers list page
type GameServersListPageData struct {
	User        *htmxauth.UserInfo
	GameServers []experience_api.GameServer
}

// AddGameServerCommandModalData holds data for the add game server command modal
type AddGameServerCommandModalData struct {
	GameServerID int
}

// InstanceHistoryItem represents a single instance history entry
type InstanceHistoryItem struct {
	InstanceID     int32
	ConfigID       int32
	CreatedDate    string
	EndDate        *string
	RuntimeSeconds int
	Status         string
}
