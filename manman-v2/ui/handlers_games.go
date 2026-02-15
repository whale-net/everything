package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manman/protos"
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
	Title   string
	Active  string
	User    *htmxauth.UserInfo
	Game    *manmanpb.Game
	Configs []*manmanpb.GameConfig
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
	
	data := GameDetailPageData{
		Title:   game.Name,
		Active:  "games",
		User:    user,
		Game:    game,
		Configs: configs,
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
	Volumes     []*manmanpb.ConfigurationStrategy
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
		case "update-parameters":
			app.handleGameConfigUpdateParameters(w, r, gameIDStr, configIDStr)
			return
		case "delete":
			app.handleGameConfigDelete(w, r, gameIDStr, configIDStr)
			return
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

	// Fetch volume strategies
	strategies, err := app.grpc.ListConfigurationStrategies(ctx, &manmanpb.ListConfigurationStrategiesRequest{
		GameId: gameID,
	})
	if err != nil {
		log.Printf("Warning: Failed to fetch configuration strategies: %v", err)
	}

	var volumeMounts []*manmanpb.ConfigurationStrategy
	if strategies != nil {
		for _, s := range strategies.Strategies {
			if s.StrategyType == "volume" {
				volumeMounts = append(volumeMounts, s)
			}
		}
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
		Volumes:     volumeMounts,
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
	_, err = app.grpc.DeployGameConfig(ctx, serverID, configID, map[string]string{})
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
		ArgsTemplate:  argsTemplate,
		EnvTemplate:   make(map[string]string),
		Files:         []*manmanpb.FileTemplate{},
		Parameters:    []*manmanpb.Parameter{},
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

func (app *App) handleGameConfigUpdateParameters(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
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

	paramsJSON := strings.TrimSpace(r.FormValue("parameters_json"))
	var parameters []*manmanpb.Parameter
	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &parameters); err != nil {
			http.Error(w, "Invalid parameters JSON", http.StatusBadRequest)
			return
		}
	}

	ctx := context.Background()
	req := &manmanpb.UpdateGameConfigRequest{
		ConfigId:   configID,
		Parameters: parameters,
		UpdatePaths: []string{"parameters"},
	}

	_, err = app.grpc.UpdateGameConfig(ctx, req)
	if err != nil {
		log.Printf("Error updating parameters: %v", err)
		http.Error(w, "Failed to update parameters", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/games/"+gameIDStr+"/configs/"+configIDStr)
	w.WriteHeader(http.StatusOK)
}
