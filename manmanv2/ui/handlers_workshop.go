package main

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// WorkshopLibraryPageData holds data for workshop library page
type WorkshopLibraryPageData struct {
	Title          string
	Active         string
	User           *htmxauth.UserInfo
	Games          []*manmanpb.Game
	Addons         []*manmanpb.WorkshopAddon
	Servers        []*manmanpb.Server
	SelectedServer *manmanpb.Server
}

// WorkshopInstallationsPageData holds data for installations page
type WorkshopInstallationsPageData struct {
	Title         string
	Active        string
	User          *htmxauth.UserInfo
	Config        *manmanpb.GameConfig
	Installations []*manmanpb.WorkshopInstallation
	AvailableAddons []*manmanpb.WorkshopAddon
}

func (app *App) handleWorkshopLibrary(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	games, err := app.grpc.ListGames(ctx)
	if err != nil {
		log.Printf("Error fetching games: %v", err)
		http.Error(w, "Failed to fetch games", http.StatusInternalServerError)
		return
	}

	// Get all addons
	addons, err := app.grpc.ListWorkshopAddons(ctx, 0, 100, 0)
	if err != nil {
		log.Printf("Error fetching addons: %v", err)
		http.Error(w, "Failed to fetch addons", http.StatusInternalServerError)
		return
	}

	// Get servers for navigation
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		// Don't fail, just continue without servers
		servers = []*manmanpb.Server{}
	}

	// Get selected server from session
	selectedServer := app.getSelectedServer(r, servers)

	data := WorkshopLibraryPageData{
		Title:          "Workshop Library",
		Active:         "workshop",
		User:           user,
		Games:          games,
		Addons:         addons,
		Servers:        servers,
		SelectedServer: selectedServer,
	}

	layoutData := LayoutData{
		Title:          data.Title,
		Active:         data.Active,
		User:           data.User,
		Servers:        servers,
		SelectedServer: selectedServer,
	}

	if err := renderPage(w, "workshop_library_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func (app *App) handleWorkshopInstallations(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	configIDStr := r.URL.Query().Get("config_id")
	if configIDStr == "" {
		http.Error(w, "config_id required", http.StatusBadRequest)
		return
	}

	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config_id", http.StatusBadRequest)
		return
	}

	config, err := app.grpc.GetGameConfig(ctx, configID)
	if err != nil {
		log.Printf("Error fetching config: %v", err)
		http.Error(w, "Failed to fetch config", http.StatusInternalServerError)
		return
	}

	installations, err := app.grpc.ListWorkshopInstallations(ctx, configID)
	if err != nil {
		log.Printf("Error fetching installations: %v", err)
		http.Error(w, "Failed to fetch installations", http.StatusInternalServerError)
		return
	}

	// Get available addons for this game
	addons, err := app.grpc.ListWorkshopAddons(ctx, 0, 100, config.GameId)
	if err != nil {
		log.Printf("Error fetching addons: %v", err)
		http.Error(w, "Failed to fetch addons", http.StatusInternalServerError)
		return
	}

	data := WorkshopInstallationsPageData{
		Title:           "Workshop Installations",
		Active:          "workshop",
		User:            user,
		Config:          config,
		Installations:   installations,
		AvailableAddons: addons,
	}

	// Get servers for navigation
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		servers = []*manmanpb.Server{}
	}

	selectedServer := app.getSelectedServer(r, servers)

	layoutData := LayoutData{
		Title:          data.Title,
		Active:         data.Active,
		User:           data.User,
		Servers:        servers,
		SelectedServer: selectedServer,
	}

	if err := renderPage(w, "workshop_installations_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func (app *App) handleInstallAddon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	configIDStr := r.FormValue("config_id")
	addonIDStr := r.FormValue("addon_id")

	configID, err := strconv.ParseInt(configIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid config_id", http.StatusBadRequest)
		return
	}

	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}

	_, err = app.grpc.InstallAddon(ctx, configID, addonID, false)
	if err != nil {
		log.Printf("Error installing addon: %v", err)
		http.Error(w, "Failed to install addon", http.StatusInternalServerError)
		return
	}

	// Return updated installations list
	w.Header().Set("HX-Trigger", "installationUpdated")
	http.Redirect(w, r, "/workshop/installations?config_id="+configIDStr, http.StatusSeeOther)
}

func (app *App) handleRemoveInstallation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	installationIDStr := r.FormValue("installation_id")
	configIDStr := r.FormValue("config_id")

	installationID, err := strconv.ParseInt(installationIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid installation_id", http.StatusBadRequest)
		return
	}

	err = app.grpc.RemoveInstallation(ctx, installationID)
	if err != nil {
		log.Printf("Error removing installation: %v", err)
		http.Error(w, "Failed to remove installation", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Trigger", "installationUpdated")
	http.Redirect(w, r, "/workshop/installations?config_id="+configIDStr, http.StatusSeeOther)
}

func (app *App) handleFetchAddonMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	gameIDStr := r.FormValue("game_id")
	workshopID := r.FormValue("workshop_id")
	platformType := r.FormValue("platform_type")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	metadata, err := app.grpc.FetchAddonMetadata(ctx, gameID, workshopID, platformType)
	if err != nil {
		log.Printf("Error fetching metadata: %v", err)
		http.Error(w, "Failed to fetch metadata", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(metadata.Name))
}
