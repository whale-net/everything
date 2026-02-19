package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// GamesPageData holds data for the games list page
type GamesPageData struct {
	Title  string
	Active string
	User   *htmxauth.UserInfo
	Games  []*manmanpb.Game
}

// GameDetailPageData holds data for game detail page
type GameDetailPageData struct {
	Title       string
	Active      string
	User        *htmxauth.UserInfo
	Game        *manmanpb.Game
	Configs     []*manmanpb.GameConfig
	SgcCounts   map[int64]int
	PathPresets []*manmanpb.GameAddonPathPreset
	Volumes     map[int64]*manmanpb.GameConfigVolume // volumeID -> Volume for preset lookup
}

// GameFormData holds data for create/edit game form
type GameFormData struct {
	Game *manmanpb.Game
	Edit bool
	Title string
	Active string
	User *htmxauth.UserInfo
}

func (app *App) handleGames(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()
	
	games, err := app.grpc.ListGames(ctx)
	if err != nil {
		log.Printf("Error fetching games: %v", err)
		http.Error(w, "Failed to fetch games", http.StatusInternalServerError)
		return
	}
	
	data := GamesPageData{
		Title:  "Games",
		Active: "games",
		User:   user,
		Games:  games,
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "games_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleGameNew(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	data := GameFormData{
		Game: &manmanpb.Game{
			Metadata: &manmanpb.GameMetadata{},
		},
		Edit: false,
		Title: "Create Game",
		Active: "games",
		User: user,
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "game_form_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleGameCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	// Parse form data
	name := r.FormValue("name")
	steamAppID := r.FormValue("steam_app_id")
	genre := r.FormValue("genre")
	publisher := r.FormValue("publisher")
	tags := r.FormValue("tags")
	
	// Parse tags (comma-separated)
	var tagList []string
	if tags != "" {
		for _, tag := range strings.Split(tags, ",") {
			tagList = append(tagList, strings.TrimSpace(tag))
		}
	}
	
	metadata := &manmanpb.GameMetadata{
		Genre:     genre,
		Publisher: publisher,
		Tags:      tagList,
	}
	
	game, err := app.grpc.CreateGame(ctx, name, steamAppID, metadata)
	if err != nil {
		log.Printf("Error creating game: %v", err)
		http.Error(w, "Failed to create game", http.StatusInternalServerError)
		return
	}
	
	// Redirect to game detail page
	w.Header().Set("HX-Redirect", "/games/"+strconv.FormatInt(game.GameId, 10))
	w.WriteHeader(http.StatusOK)
}

func (app *App) handleGameDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	
	// Extract game ID from URL path
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	gameIDStr := pathParts[1]
	
	// Handle different sub-paths
	if len(pathParts) > 2 {
		// Sub-routes like /games/{id}/configs or /games/{id}/edit
		subPath := pathParts[2]
		switch subPath {
		case "edit":
			app.handleGameEdit(w, r, gameIDStr)
			return
		case "delete":
			app.handleGameDelete(w, r, gameIDStr)
			return
		case "actions":
			app.handleGameActions(w, r)
			return
		case "presets":
			// Handle preset routes: /games/{id}/presets/create or /games/{id}/presets/{preset_id}/delete
			if len(pathParts) > 3 {
				if pathParts[3] == "create" {
					app.handleCreateAddonPathPreset(w, r)
					return
				} else if len(pathParts) > 4 && pathParts[4] == "delete" {
					app.handleDeleteAddonPathPreset(w, r)
					return
				}
			}
		case "configs":
			// Handle config routes
			if len(pathParts) > 3 {
				// /games/{id}/configs/{config_id}
				app.handleGameConfigDetail(w, r, gameIDStr, pathParts[3])
				return
			}
		}
	}
	
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	game, err := app.grpc.GetGame(ctx, gameID)
	if err != nil {
		log.Printf("Error fetching game: %v", err)
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}
	
	configs, err := app.grpc.ListGameConfigs(ctx, gameID)
	if err != nil {
		log.Printf("Error fetching game configs: %v", err)
		configs = []*manmanpb.GameConfig{} // Continue with empty list
	}

	// Build SGC count map: configID → number of SGCs deployed
	sgcCounts := make(map[int64]int)
	allSGCs, err := app.grpc.ListServerGameConfigs(ctx, 0)
	if err != nil {
		log.Printf("Warning: failed to fetch SGC counts: %v", err)
	} else {
		for _, sgc := range allSGCs {
			sgcCounts[sgc.GameConfigId]++
		}
	}

	// Fetch path presets for this game
	pathPresets, err := app.grpc.ListAddonPathPresets(ctx, gameID)
	if err != nil {
		log.Printf("Warning: failed to fetch path presets: %v", err)
		pathPresets = []*manmanpb.GameAddonPathPreset{}
	}

	// Fetch all volumes for all configs of this game (for preset dropdown)
	volumeMap := make(map[int64]*manmanpb.GameConfigVolume)
	for _, config := range configs {
		volumes, err := app.grpc.ListGameConfigVolumes(ctx, config.ConfigId)
		if err != nil {
			log.Printf("Warning: failed to fetch volumes for config %d: %v", config.ConfigId, err)
			continue
		}
		for _, vol := range volumes {
			volumeMap[vol.VolumeId] = vol
		}
	}

	data := GameDetailPageData{
		Title:       game.Name,
		Active:      "games",
		User:        user,
		Game:        game,
		Configs:     configs,
		SgcCounts:   sgcCounts,
		PathPresets: pathPresets,
		Volumes:     volumeMap,
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "game_detail_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleGameEdit(w http.ResponseWriter, r *http.Request, gameIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	// Parse form data
	name := r.FormValue("name")
	steamAppID := r.FormValue("steam_app_id")
	genre := r.FormValue("genre")
	publisher := r.FormValue("publisher")
	tags := r.FormValue("tags")
	
	// Parse tags (comma-separated)
	var tagList []string
	if tags != "" {
		for _, tag := range strings.Split(tags, ",") {
			tagList = append(tagList, strings.TrimSpace(tag))
		}
	}
	
	metadata := &manmanpb.GameMetadata{
		Genre:     genre,
		Publisher: publisher,
		Tags:      tagList,
	}
	
	_, err = app.grpc.UpdateGame(ctx, gameID, name, steamAppID, metadata)
	if err != nil {
		log.Printf("Error updating game: %v", err)
		http.Error(w, "Failed to update game", http.StatusInternalServerError)
		return
	}
	
	// Redirect back to game detail
	w.Header().Set("HX-Redirect", "/games/"+gameIDStr)
	w.WriteHeader(http.StatusOK)
}

func (app *App) handleGameDelete(w http.ResponseWriter, r *http.Request, gameIDStr string) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	err = app.grpc.DeleteGame(ctx, gameID)
	if err != nil {
		log.Printf("Error deleting game: %v", err)
		http.Error(w, "Failed to delete game", http.StatusInternalServerError)
		return
	}
	
	// Redirect to games list
	w.Header().Set("HX-Redirect", "/games")
	w.WriteHeader(http.StatusOK)
}

// GameConfigDetailPageData holds data for config detail page
type GameConfigDetailPageData struct {
	Title       string
	Active      string
	User        *htmxauth.UserInfo
	Game        *manmanpb.Game
	Config      *manmanpb.GameConfig
	Servers     []*manmanpb.Server
	Deployments []ServerGameConfigView
	DeployError string
	Volumes     []*manmanpb.GameConfigVolume
}

type ServerGameConfigView struct {
	Server *manmanpb.Server
	Config *manmanpb.ServerGameConfig
}

// GameConfigFormData holds data for create/edit config form
type GameConfigFormData struct {
	Game   *manmanpb.Game
	Config *manmanpb.GameConfig
	Edit   bool
	Title  string
	Active string
	User   *htmxauth.UserInfo
}

func (app *App) handleGameConfigDetail(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	user := htmxauth.GetUser(r.Context())
	
	// Handle sub-routes
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) > 4 {
		subPath := pathParts[4]
		switch subPath {
		case "edit":
			app.handleGameConfigEdit(w, r, gameIDStr, configIDStr)
			return
		case "deploy":
			app.handleGameConfigDeploy(w, r, gameIDStr, configIDStr)
			return
		case "update-env":
			app.handleGameConfigUpdateEnv(w, r, gameIDStr, configIDStr)
			return
		case "delete":
			app.handleGameConfigDelete(w, r, gameIDStr, configIDStr)
			return
		case "actions":
			app.handleConfigActions(w, r)
			return
		case "volumes":
			// Handle volume routes
			if len(pathParts) > 5 {
				// /games/{id}/configs/{config_id}/volumes/{action_or_volume_id}
				actionOrVolumeID := pathParts[5]
				if actionOrVolumeID == "create" {
					app.handleGameConfigVolumeCreate(w, r, gameIDStr, configIDStr)
					return
				} else {
					// Assume it's a volume_id for delete
					app.handleGameConfigVolumeDelete(w, r, gameIDStr, configIDStr, actionOrVolumeID)
					return
				}
			}
		}
	}

	// Special handling for "new" config
	if configIDStr == "new" {
		app.handleGameConfigNew(w, r, gameIDStr)
		return
	}
	
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	game, err := app.grpc.GetGame(ctx, gameID)
	if err != nil {
		log.Printf("Error fetching game: %v", err)
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}
	
	config, err := app.grpc.GetGameConfig(ctx, configID)
	if err != nil {
		log.Printf("Error fetching config: %v", err)
		http.Error(w, "Config not found", http.StatusNotFound)
		return
	}

	// Fetch volumes for this GameConfig
	volumes, err := app.grpc.ListGameConfigVolumes(ctx, configID)
	if err != nil {
		log.Printf("Warning: Failed to fetch volumes for config %d: %v", configID, err)
		volumes = []*manmanpb.GameConfigVolume{}
	}

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		servers = []*manmanpb.Server{}
	}

	var deployments []ServerGameConfigView
	for _, server := range servers {
		serverConfigs, err := app.grpc.ListServerGameConfigs(ctx, server.ServerId)
		if err != nil {
			log.Printf("Error fetching server configs for server %d: %v", server.ServerId, err)
			continue
		}
		for _, sgc := range serverConfigs {
			if sgc.GameConfigId == config.ConfigId {
				deployments = append(deployments, ServerGameConfigView{
					Server: server,
					Config: sgc,
				})
			}
		}
	}

	deployError := strings.TrimSpace(r.URL.Query().Get("deploy_error"))
	
	data := GameConfigDetailPageData{
		Title:       config.Name + " - " + game.Name,
		Active:      "games",
		User:        user,
		Game:        game,
		Config:      config,
		Servers:     servers,
		Deployments: deployments,
		DeployError: deployError,
		Volumes:     volumes,
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "config_detail_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleGameConfigDeploy(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}

	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	serverIDStr := strings.TrimSpace(r.FormValue("server_id"))
	if serverIDStr == "" {
		http.Error(w, "Missing server_id", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server_id", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	_, err = app.grpc.DeployGameConfig(ctx, serverID, configID)
	if err != nil {
		log.Printf("Error deploying game config: %v", err)
		redirectURL := "/games/" + gameIDStr + "/configs/" + configIDStr + "?deploy_error=Failed%20to%20deploy%20config"
		if r.Header.Get("HX-Request") != "" {
			w.Header().Set("HX-Redirect", redirectURL)
			w.WriteHeader(http.StatusOK)
		} else {
			http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		}
		return
	}

	redirectURL := "/games/" + strconv.FormatInt(gameID, 10) + "/configs/" + strconv.FormatInt(configID, 10)
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", redirectURL)
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
	}
}

func (app *App) handleGameConfigNew(w http.ResponseWriter, r *http.Request, gameIDStr string) {
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	game, err := app.grpc.GetGame(ctx, gameID)
	if err != nil {
		log.Printf("Error fetching game: %v", err)
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}
	
	user := htmxauth.GetUser(r.Context())

	data := GameConfigFormData{
		Game: game,
		Config: &manmanpb.GameConfig{
			GameId: gameID,
		},
		Edit: false,
		Title: "Create Configuration",
		Active: "games",
		User: user,
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "config_form_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleGameConfigCreate(w http.ResponseWriter, r *http.Request, gameIDStr string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	// Parse form data
	name := r.FormValue("name")
	image := r.FormValue("image")
	argsTemplate := r.FormValue("args_template")
	
	req := &manmanpb.CreateGameConfigRequest{
		GameId:        gameID,
		Name:          name,
		Image:         image,
		ArgsTemplate: argsTemplate,
		EnvTemplate:  make(map[string]string),
	}
	
	config, err := app.grpc.CreateGameConfig(ctx, req)
	if err != nil {
		log.Printf("Error creating config: %v", err)
		http.Error(w, "Failed to create config", http.StatusInternalServerError)
		return
	}
	
	// Redirect to config detail page
	w.Header().Set("HX-Redirect", "/games/"+gameIDStr+"/configs/"+strconv.FormatInt(config.ConfigId, 10))
	w.WriteHeader(http.StatusOK)
}

func (app *App) handleGameConfigEdit(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}
	
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	// Parse form data
	name := r.FormValue("name")
	image := r.FormValue("image")
	argsTemplate := r.FormValue("args_template")
	
	req := &manmanpb.UpdateGameConfigRequest{
		ConfigId:      configID,
		Name:          name,
		Image:         image,
		ArgsTemplate:  argsTemplate,
	}
	
	_, err = app.grpc.UpdateGameConfig(ctx, req)
	if err != nil {
		log.Printf("Error updating config: %v", err)
		http.Error(w, "Failed to update config", http.StatusInternalServerError)
		return
	}
	
	// Redirect back to config detail
	w.Header().Set("HX-Redirect", "/games/"+gameIDStr+"/configs/"+configIDStr)
	w.WriteHeader(http.StatusOK)
}

func (app *App) handleGameConfigDelete(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	
	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
	err = app.grpc.DeleteGameConfig(ctx, configID)
	if err != nil {
		log.Printf("Error deleting config: %v", err)
		http.Error(w, "Failed to delete config", http.StatusInternalServerError)
		return
	}
	
	// Redirect to game detail
	w.Header().Set("HX-Redirect", "/games/"+gameIDStr)
	w.WriteHeader(http.StatusOK)
}

func (app *App) handleGameConfigUpdateEnv(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	envJSON := strings.TrimSpace(r.FormValue("env_template_json"))
	envTemplate := map[string]string{}
	if envJSON != "" {
		if err := json.Unmarshal([]byte(envJSON), &envTemplate); err != nil {
			http.Error(w, "Invalid env template JSON", http.StatusBadRequest)
			return
		}
	}

	ctx := context.Background()
	req := &manmanpb.UpdateGameConfigRequest{
		ConfigId:    configID,
		EnvTemplate: envTemplate,
		UpdatePaths: []string{"env_template"},
	}

	_, err = app.grpc.UpdateGameConfig(ctx, req)
	if err != nil {
		log.Printf("Error updating env template: %v", err)
		http.Error(w, "Failed to update env template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/games/"+gameIDStr+"/configs/"+configIDStr)
	w.WriteHeader(http.StatusOK)
}

// Volume CRUD handlers

func (app *App) handleGameConfigVolumeCreate(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	containerPath := strings.TrimSpace(r.FormValue("container_path"))
	hostSubpath := strings.TrimSpace(r.FormValue("host_subpath"))
	readOnly := r.FormValue("read_only") == "on"

	if name == "" || containerPath == "" {
		http.Error(w, "Name and container path are required", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	_, err = app.grpc.CreateGameConfigVolume(ctx, configID, name, description, containerPath, hostSubpath, readOnly)
	if err != nil {
		log.Printf("Error creating volume: %v", err)
		http.Error(w, "Failed to create volume", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/games/"+gameIDStr+"/configs/"+configIDStr)
	w.WriteHeader(http.StatusOK)
}

func (app *App) handleGameConfigVolumeDelete(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr, volumeIDStr string) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	volumeID, err := strconv.ParseInt(volumeIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid volume ID", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	err = app.grpc.DeleteGameConfigVolume(ctx, volumeID)
	if err != nil {
		log.Printf("Error deleting volume: %v", err)
		http.Error(w, "Failed to delete volume", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/games/"+gameIDStr+"/configs/"+configIDStr)
	w.WriteHeader(http.StatusOK)
}


// Addon Path Preset handlers

func (app *App) handleCreateAddonPathPreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	gameIDStr := r.FormValue("game_id")
	name := r.FormValue("name")
	description := r.FormValue("description")
	installationPath := r.FormValue("installation_path")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	_, err = app.grpc.CreateAddonPathPreset(ctx, gameID, name, description, installationPath)
	if err != nil {
		log.Printf("Error creating preset: %v", err)
		http.Error(w, "Failed to create preset", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/games/"+gameIDStr, http.StatusSeeOther)
}

func (app *App) handleDeleteAddonPathPreset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	presetIDStr := r.FormValue("preset_id")
	gameIDStr := r.FormValue("game_id")

	presetID, err := strconv.ParseInt(presetIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid preset_id", http.StatusBadRequest)
		return
	}

	err = app.grpc.DeleteAddonPathPreset(ctx, presetID)
	if err != nil {
		log.Printf("Error deleting preset: %v", err)
		http.Error(w, "Failed to delete preset", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/games/"+gameIDStr, http.StatusSeeOther)
}
