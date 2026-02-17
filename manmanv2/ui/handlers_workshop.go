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
	Libraries      []*manmanpb.WorkshopLibrary
	Servers        []*manmanpb.Server
	SelectedServer *manmanpb.Server
}

// WorkshopLibraryDetailPageData holds data for library detail page
type WorkshopLibraryDetailPageData struct {
	Title          string
	Active         string
	User           *htmxauth.UserInfo
	Library        *manmanpb.WorkshopLibrary
	Addons         []*manmanpb.WorkshopAddon
	AvailableAddons []*manmanpb.WorkshopAddon
	ChildLibraries []*manmanpb.WorkshopLibrary
	AvailableLibraries []*manmanpb.WorkshopLibrary
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

	// Get all libraries
	libraries, err := app.grpc.ListLibraries(ctx, 0, 100, 0)
	if err != nil {
		log.Printf("Error fetching libraries: %v", err)
		http.Error(w, "Failed to fetch libraries", http.StatusInternalServerError)
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
		Libraries:      libraries,
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

	addon, err := app.grpc.FetchAddonMetadata(ctx, gameID, workshopID, platformType)
	if err != nil {
		log.Printf("Error fetching metadata: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`<div class="alert alert-error">Failed to fetch metadata</div>`))
		return
	}

	// Return success message with addon details
	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("HX-Trigger", "addonCreated")
	w.Write([]byte(`<div class="alert alert-success">Successfully added: ` + addon.Name + `</div>`))
}

func (app *App) handleDeleteAddon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	addonIDStr := r.FormValue("addon_id")
	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}

	err = app.grpc.DeleteAddon(ctx, addonID)
	if err != nil {
		log.Printf("Error deleting addon: %v", err)
		http.Error(w, "Failed to delete addon", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library", http.StatusSeeOther)
}

func (app *App) handleLibraryDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	libraryIDStr := r.URL.Query().Get("library_id")
	if libraryIDStr == "" {
		http.Error(w, "library_id required", http.StatusBadRequest)
		return
	}

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	library, err := app.grpc.GetLibrary(ctx, libraryID)
	if err != nil {
		log.Printf("Error fetching library: %v", err)
		http.Error(w, "Failed to fetch library", http.StatusInternalServerError)
		return
	}

	// Get addons in this library
	addons, err := app.grpc.GetLibraryAddons(ctx, libraryID)
	if err != nil {
		log.Printf("Error fetching library addons: %v", err)
		addons = []*manmanpb.WorkshopAddon{}
	}

	// Get available addons for this game
	availableAddons, err := app.grpc.ListWorkshopAddons(ctx, 0, 100, library.GameId)
	if err != nil {
		log.Printf("Error fetching available addons: %v", err)
		availableAddons = []*manmanpb.WorkshopAddon{}
	}

	// Get child libraries
	childLibraries, err := app.grpc.GetChildLibraries(ctx, libraryID)
	if err != nil {
		log.Printf("Error fetching child libraries: %v", err)
		childLibraries = []*manmanpb.WorkshopLibrary{}
	}

	// Get available libraries for nesting
	availableLibraries, err := app.grpc.ListLibraries(ctx, 0, 100, library.GameId)
	if err != nil {
		log.Printf("Error fetching available libraries: %v", err)
		availableLibraries = []*manmanpb.WorkshopLibrary{}
	}

	servers, _ := app.grpc.ListServers(ctx)
	selectedServer := app.getSelectedServer(r, servers)

	data := WorkshopLibraryDetailPageData{
		Title:              library.Name,
		Active:             "workshop",
		User:               user,
		Library:            library,
		Addons:             addons,
		AvailableAddons:    availableAddons,
		ChildLibraries:     childLibraries,
		AvailableLibraries: availableLibraries,
		Servers:            servers,
		SelectedServer:     selectedServer,
	}

	layoutData := LayoutData{
		Title:          data.Title,
		Active:         data.Active,
		User:           data.User,
		Servers:        servers,
		SelectedServer: selectedServer,
	}

	if err := renderPage(w, "workshop_library_detail_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func (app *App) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	gameIDStr := r.FormValue("game_id")
	name := r.FormValue("name")
	description := r.FormValue("description")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	library, err := app.grpc.CreateLibrary(ctx, gameID, name, description)
	if err != nil {
		log.Printf("Error creating library: %v", err)
		http.Error(w, "Failed to create library", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library-detail?library_id="+strconv.FormatInt(library.LibraryId, 10), http.StatusSeeOther)
}

func (app *App) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	libraryIDStr := r.FormValue("library_id")
	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	err = app.grpc.DeleteLibrary(ctx, libraryID)
	if err != nil {
		log.Printf("Error deleting library: %v", err)
		http.Error(w, "Failed to delete library", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library", http.StatusSeeOther)
}

func (app *App) handleAddAddonToLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	libraryIDStr := r.FormValue("library_id")
	addonIDStr := r.FormValue("addon_id")

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}

	err = app.grpc.AddAddonToLibrary(ctx, libraryID, addonID)
	if err != nil {
		log.Printf("Error adding addon to library: %v", err)
		http.Error(w, "Failed to add addon", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library-detail?library_id="+libraryIDStr, http.StatusSeeOther)
}

func (app *App) handleRemoveAddonFromLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	libraryIDStr := r.FormValue("library_id")
	addonIDStr := r.FormValue("addon_id")

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}

	err = app.grpc.RemoveAddonFromLibrary(ctx, libraryID, addonID)
	if err != nil {
		log.Printf("Error removing addon from library: %v", err)
		http.Error(w, "Failed to remove addon", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library-detail?library_id="+libraryIDStr, http.StatusSeeOther)
}

func (app *App) handleAddLibraryReference(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	parentIDStr := r.FormValue("parent_library_id")
	childIDStr := r.FormValue("child_library_id")

	parentID, err := strconv.ParseInt(parentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid parent_library_id", http.StatusBadRequest)
		return
	}

	childID, err := strconv.ParseInt(childIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid child_library_id", http.StatusBadRequest)
		return
	}

	err = app.grpc.AddLibraryReference(ctx, parentID, childID)
	if err != nil {
		log.Printf("Error adding library reference: %v", err)
		http.Error(w, "Failed to add library reference", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library-detail?library_id="+parentIDStr, http.StatusSeeOther)
}
