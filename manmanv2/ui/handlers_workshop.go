package main

import (
	"context"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// WorkshopLibraryPageData holds data for workshop library home page
type WorkshopLibraryPageData struct {
	Title          string
	Active         string
	User           *htmxauth.UserInfo
	Games          []*manmanpb.Game
	Addons         []*manmanpb.WorkshopAddon
	RecentAddons   []*manmanpb.WorkshopAddon
	Libraries      []*manmanpb.WorkshopLibrary
	Servers        []*manmanpb.Server
	SelectedServer *manmanpb.Server
}

// WorkshopSearchPageData holds data for workshop search page
type WorkshopSearchPageData struct {
	Title          string
	Active         string
	User           *htmxauth.UserInfo
	Games          []*manmanpb.Game
	Addons         []*manmanpb.WorkshopAddon
	Libraries      []*manmanpb.WorkshopLibrary
	Servers        []*manmanpb.Server
	SelectedServer *manmanpb.Server
	Query          string
	GameID         int64
	TypeFilter     string
}

// WorkshopAddonDetailPageData holds data for addon detail page
type WorkshopAddonDetailPageData struct {
	Title               string
	Active              string
	User                *htmxauth.UserInfo
	Addon               *manmanpb.WorkshopAddon
	Game                *manmanpb.Game
	Games               []*manmanpb.Game
	ContainingLibraries []*manmanpb.WorkshopLibrary
	AvailableLibraries  []*manmanpb.WorkshopLibrary
	Servers             []*manmanpb.Server
	SelectedServer      *manmanpb.Server
}

// WorkshopLibraryDetailPageData holds data for library detail page
type WorkshopLibraryDetailPageData struct {
	Title              string
	Active             string
	User               *htmxauth.UserInfo
	Library            *manmanpb.WorkshopLibrary
	Game               *manmanpb.Game
	Games              []*manmanpb.Game
	Addons             []*manmanpb.WorkshopAddon
	AvailableAddons    []*manmanpb.WorkshopAddon
	ChildLibraries     []*manmanpb.WorkshopLibrary
	AvailableLibraries []*manmanpb.WorkshopLibrary
	Servers            []*manmanpb.Server
	SelectedServer     *manmanpb.Server
}

// WorkshopInstallationsPageData holds data for installations page
type WorkshopInstallationsPageData struct {
	Title           string
	Active          string
	User            *htmxauth.UserInfo
	Config          *manmanpb.GameConfig
	Installations   []*manmanpb.WorkshopInstallation
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

	addons, err := app.grpc.ListWorkshopAddons(ctx, 0, 200, 0)
	if err != nil {
		log.Printf("Error fetching addons: %v", err)
		http.Error(w, "Failed to fetch addons", http.StatusInternalServerError)
		return
	}

	libraries, err := app.grpc.ListLibraries(ctx, 200, 0, 0)
	if err != nil {
		log.Printf("Error fetching libraries: %v", err)
		http.Error(w, "Failed to fetch libraries", http.StatusInternalServerError)
		return
	}

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		servers = []*manmanpb.Server{}
	}

	selectedServer := app.getSelectedServer(r, servers)

	// Sort addons by UpdatedAt descending for recent addons
	sorted := make([]*manmanpb.WorkshopAddon, len(addons))
	copy(sorted, addons)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].UpdatedAt > sorted[j].UpdatedAt
	})
	recentAddons := sorted
	if len(recentAddons) > 8 {
		recentAddons = recentAddons[:8]
	}

	data := WorkshopLibraryPageData{
		Title:          "Workshop Library",
		Active:         "workshop",
		User:           user,
		Games:          games,
		Addons:         addons,
		RecentAddons:   recentAddons,
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

func (app *App) handleWorkshopSearch(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	query := r.URL.Query().Get("q")
	gameIDStr := r.URL.Query().Get("game_id")
	typeFilter := r.URL.Query().Get("type")

	var gameID int64
	if gameIDStr != "" {
		gameID, _ = strconv.ParseInt(gameIDStr, 10, 64)
	}

	games, err := app.grpc.ListGames(ctx)
	if err != nil {
		log.Printf("Error fetching games: %v", err)
		http.Error(w, "Failed to fetch games", http.StatusInternalServerError)
		return
	}

	addons, err := app.grpc.ListWorkshopAddons(ctx, 0, 200, gameID)
	if err != nil {
		log.Printf("Error fetching addons: %v", err)
		addons = []*manmanpb.WorkshopAddon{}
	}

	libraries, err := app.grpc.ListLibraries(ctx, 200, 0, gameID)
	if err != nil {
		log.Printf("Error fetching libraries: %v", err)
		libraries = []*manmanpb.WorkshopLibrary{}
	}

	servers, _ := app.grpc.ListServers(ctx)
	selectedServer := app.getSelectedServer(r, servers)

	data := WorkshopSearchPageData{
		Title:          "Workshop Search",
		Active:         "workshop",
		User:           user,
		Games:          games,
		Addons:         addons,
		Libraries:      libraries,
		Servers:        servers,
		SelectedServer: selectedServer,
		Query:          query,
		GameID:         gameID,
		TypeFilter:     typeFilter,
	}

	layoutData := LayoutData{
		Title:          data.Title,
		Active:         data.Active,
		User:           data.User,
		Servers:        servers,
		SelectedServer: selectedServer,
	}

	if err := renderPage(w, "workshop_search_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func (app *App) handleWorkshopAddonDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()

	addonIDStr := r.URL.Query().Get("addon_id")
	if addonIDStr == "" {
		http.Error(w, "addon_id required", http.StatusBadRequest)
		return
	}

	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}

	addon, err := app.grpc.GetWorkshopAddon(ctx, addonID)
	if err != nil {
		log.Printf("Error fetching addon: %v", err)
		http.Error(w, "Failed to fetch addon", http.StatusInternalServerError)
		return
	}

	games, _ := app.grpc.ListGames(ctx)

	var game *manmanpb.Game
	for _, g := range games {
		if g.GameId == addon.GameId {
			game = g
			break
		}
	}

	// Get all libraries for this addon's game and classify them
	allLibraries, _ := app.grpc.ListLibraries(ctx, 200, 0, addon.GameId)

	var containingLibraries []*manmanpb.WorkshopLibrary
	var availableLibraries []*manmanpb.WorkshopLibrary

	for _, lib := range allLibraries {
		libAddons, err := app.grpc.GetLibraryAddons(ctx, lib.LibraryId)
		if err != nil {
			availableLibraries = append(availableLibraries, lib)
			continue
		}
		found := false
		for _, a := range libAddons {
			if a.AddonId == addonID {
				found = true
				break
			}
		}
		if found {
			containingLibraries = append(containingLibraries, lib)
		} else {
			availableLibraries = append(availableLibraries, lib)
		}
	}

	servers, _ := app.grpc.ListServers(ctx)
	selectedServer := app.getSelectedServer(r, servers)

	addonName := addon.Name
	if addonName == "" {
		addonName = "Addon " + addonIDStr
	}

	data := WorkshopAddonDetailPageData{
		Title:               addonName,
		Active:              "workshop",
		User:                user,
		Addon:               addon,
		Game:                game,
		Games:               games,
		ContainingLibraries: containingLibraries,
		AvailableLibraries:  availableLibraries,
		Servers:             servers,
		SelectedServer:      selectedServer,
	}

	layoutData := LayoutData{
		Title:          data.Title,
		Active:         data.Active,
		User:           data.User,
		Servers:        servers,
		SelectedServer: selectedServer,
	}

	if err := renderPage(w, "workshop_addon_detail_content", data, layoutData); err != nil {
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

	addons, err := app.grpc.ListWorkshopAddons(ctx, 0, 200, config.GameId)
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
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`<div style="padding:10px;background:#fef2f2;color:#991b1b;border-radius:4px;margin-top:10px;border:1px solid #fecaca;">Failed to fetch from Steam. Check the Workshop ID and Game.</div>`))
		return
	}

	// Return a confirmation/edit form inline via HTMX
	w.Header().Set("Content-Type", "text/html")
	sizeStr := ""
	if addon.FileSizeBytes > 0 {
		sizeStr = strconv.FormatFloat(float64(addon.FileSizeBytes)/1048576, 'f', 2, 64) + " MB"
	}
	typeLabel := "Addon"
	isCollectionStr := "false"
	if addon.IsCollection {
		typeLabel = "Collection"
		isCollectionStr = "true"
	}
	fileSizeBytesStr := strconv.FormatInt(addon.FileSizeBytes, 10)

	w.Write([]byte(`<div style="margin-top:14px;border:2px solid #10b981;border-radius:8px;background:#f0fdf4;padding:16px;">
<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:12px;">
<strong style="color:#065f46;">Fetched successfully — review and save</strong>
<span style="font-size:12px;color:#6b7280;">` + typeLabel + ` · ` + workshopID + `</span>
</div>
<form method="POST" action="/workshop/create-addon" style="display:grid;grid-template-columns:1fr 1fr;gap:10px;">
<input type="hidden" name="game_id" value="` + gameIDStr + `">
<input type="hidden" name="workshop_id" value="` + workshopID + `">
<input type="hidden" name="platform_type" value="` + platformType + `">
<input type="hidden" name="file_size_bytes" value="` + fileSizeBytesStr + `">
<input type="hidden" name="is_collection" value="` + isCollectionStr + `">
<div>
<label style="font-size:12px;font-weight:600;color:#374151;display:block;margin-bottom:3px;">Name</label>
<input type="text" name="name" value="` + addon.Name + `" required style="width:100%;padding:7px 10px;border:1px solid #d1d5db;border-radius:5px;font-size:14px;">
</div>
<div>
<label style="font-size:12px;font-weight:600;color:#374151;display:block;margin-bottom:3px;">Size / Type</label>
<input type="text" value="` + sizeStr + ` · ` + typeLabel + `" disabled style="width:100%;padding:7px 10px;border:1px solid #e5e7eb;border-radius:5px;font-size:14px;background:#f9fafb;color:#9ca3af;">
</div>
<div style="grid-column:span 2;">
<label style="font-size:12px;font-weight:600;color:#374151;display:block;margin-bottom:3px;">Description</label>
<textarea name="description" rows="2" style="width:100%;padding:7px 10px;border:1px solid #d1d5db;border-radius:5px;font-size:13px;font-family:inherit;resize:vertical;">` + addon.Description + `</textarea>
</div>
<div style="grid-column:span 2;display:flex;justify-content:flex-end;gap:8px;margin-top:4px;">
<button type="submit" class="btn btn-primary">Save Addon</button>
</div>
</form>
</div>`))
}

func (app *App) handleCreateAddon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	gameIDStr := r.FormValue("game_id")
	workshopID := r.FormValue("workshop_id")
	platformType := r.FormValue("platform_type")
	name := r.FormValue("name")
	description := r.FormValue("description")
	fileSizeBytesStr := r.FormValue("file_size_bytes")
	isCollectionStr := r.FormValue("is_collection")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	var fileSizeBytes int64
	if fileSizeBytesStr != "" {
		fileSizeBytes, _ = strconv.ParseInt(fileSizeBytesStr, 10, 64)
	}
	isCollection := isCollectionStr == "true"

	addon, err := app.grpc.CreateAddon(ctx, gameID, workshopID, platformType, name, description, fileSizeBytes, isCollection)
	if err != nil {
		log.Printf("Error creating addon: %v", err)
		http.Error(w, "Failed to create addon", http.StatusInternalServerError)
		return
	}

	addonIDStr := strconv.FormatInt(addon.AddonId, 10)
	http.Redirect(w, r, "/workshop/addon?addon_id="+addonIDStr, http.StatusSeeOther)
}

func (app *App) handleUpdateAddonDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	addonIDStr := r.FormValue("addon_id")
	name := r.FormValue("name")
	description := r.FormValue("description")

	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}

	_, err = app.grpc.UpdateAddon(ctx, addonID, name, description)
	if err != nil {
		log.Printf("Error updating addon: %v", err)
		http.Error(w, "Failed to update addon", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/addon?addon_id="+addonIDStr, http.StatusSeeOther)
}

func (app *App) handleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := context.Background()

	libraryIDStr := r.FormValue("library_id")
	name := r.FormValue("name")
	description := r.FormValue("description")

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	_, err = app.grpc.UpdateLibrary(ctx, libraryID, name, description)
	if err != nil {
		log.Printf("Error updating library: %v", err)
		http.Error(w, "Failed to update library", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library-detail?library_id="+libraryIDStr, http.StatusSeeOther)
}

// AvailableAddonsData holds data for the HTMX available addons partial
type AvailableAddonsData struct {
	Addons    []*manmanpb.WorkshopAddon
	LibraryID int64
}

// AvailableLibrariesData holds data for the HTMX available libraries partial
type AvailableLibrariesData struct {
	Libraries []*manmanpb.WorkshopLibrary
	LibraryID int64
}

func (app *App) handleAvailableAddons(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	libraryIDStr := r.URL.Query().Get("library_id")
	q := strings.ToLower(r.URL.Query().Get("q"))

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	library, err := app.grpc.GetLibrary(ctx, libraryID)
	if err != nil {
		http.Error(w, "Failed to fetch library", http.StatusInternalServerError)
		return
	}

	allAddons, _ := app.grpc.ListWorkshopAddons(ctx, 0, 200, library.GameId)
	libraryAddons, _ := app.grpc.GetLibraryAddons(ctx, libraryID)

	inLibrary := make(map[int64]bool)
	for _, a := range libraryAddons {
		inLibrary[a.AddonId] = true
	}

	var available []*manmanpb.WorkshopAddon
	for _, a := range allAddons {
		if inLibrary[a.AddonId] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(a.Name), q) && !strings.Contains(a.WorkshopId, q) {
			continue
		}
		available = append(available, a)
	}

	data := AvailableAddonsData{Addons: available, LibraryID: libraryID}
	w.Header().Set("Content-Type", "text/html")
	if err := renderTemplate(w, "workshop_available_addons_partial", data); err != nil {
		log.Printf("Error rendering partial: %v", err)
	}
}

func (app *App) handleAvailableLibraries(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	libraryIDStr := r.URL.Query().Get("library_id")
	q := strings.ToLower(r.URL.Query().Get("q"))

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	library, err := app.grpc.GetLibrary(ctx, libraryID)
	if err != nil {
		http.Error(w, "Failed to fetch library", http.StatusInternalServerError)
		return
	}

	allLibraries, _ := app.grpc.ListLibraries(ctx, 200, 0, library.GameId)
	childLibraries, _ := app.grpc.GetChildLibraries(ctx, libraryID)

	isChild := make(map[int64]bool)
	for _, cl := range childLibraries {
		isChild[cl.LibraryId] = true
	}

	var available []*manmanpb.WorkshopLibrary
	for _, lib := range allLibraries {
		if lib.LibraryId == libraryID || isChild[lib.LibraryId] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(lib.Name), q) {
			continue
		}
		available = append(available, lib)
	}

	data := AvailableLibrariesData{Libraries: available, LibraryID: libraryID}
	w.Header().Set("Content-Type", "text/html")
	if err := renderTemplate(w, "workshop_available_libraries_partial", data); err != nil {
		log.Printf("Error rendering partial: %v", err)
	}
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

	// Build set of addon IDs already in library
	inLibrary := make(map[int64]bool)
	for _, a := range addons {
		inLibrary[a.AddonId] = true
	}

	// Get available addons for this game, excluding those already in library
	allAddons, err := app.grpc.ListWorkshopAddons(ctx, 0, 200, library.GameId)
	if err != nil {
		log.Printf("Error fetching available addons: %v", err)
		allAddons = []*manmanpb.WorkshopAddon{}
	}
	var availableAddons []*manmanpb.WorkshopAddon
	for _, a := range allAddons {
		if !inLibrary[a.AddonId] {
			availableAddons = append(availableAddons, a)
		}
	}

	// Get child libraries
	childLibraries, err := app.grpc.GetChildLibraries(ctx, libraryID)
	if err != nil {
		log.Printf("Error fetching child libraries: %v", err)
		childLibraries = []*manmanpb.WorkshopLibrary{}
	}

	// Build set of child library IDs
	isChild := make(map[int64]bool)
	for _, cl := range childLibraries {
		isChild[cl.LibraryId] = true
	}

	// Get available libraries for nesting (exclude self and already-nested)
	allLibraries, err := app.grpc.ListLibraries(ctx, 200, 0, library.GameId)
	if err != nil {
		log.Printf("Error fetching available libraries: %v", err)
		allLibraries = []*manmanpb.WorkshopLibrary{}
	}
	var availableLibraries []*manmanpb.WorkshopLibrary
	for _, lib := range allLibraries {
		if lib.LibraryId != libraryID && !isChild[lib.LibraryId] {
			availableLibraries = append(availableLibraries, lib)
		}
	}

	games, _ := app.grpc.ListGames(ctx)
	var game *manmanpb.Game
	for _, g := range games {
		if g.GameId == library.GameId {
			game = g
			break
		}
	}

	servers, _ := app.grpc.ListServers(ctx)
	selectedServer := app.getSelectedServer(r, servers)

	data := WorkshopLibraryDetailPageData{
		Title:              library.Name,
		Active:             "workshop",
		User:               user,
		Library:            library,
		Game:               game,
		Games:              games,
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
	returnURL := r.FormValue("return_url")

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

	if returnURL != "" {
		http.Redirect(w, r, returnURL, http.StatusSeeOther)
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
	returnURL := r.FormValue("return_url")

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

	if returnURL != "" {
		http.Redirect(w, r, returnURL, http.StatusSeeOther)
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

func (app *App) handleRemoveLibraryReference(w http.ResponseWriter, r *http.Request) {
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

	err = app.grpc.RemoveLibraryReference(ctx, parentID, childID)
	if err != nil {
		log.Printf("Error removing library reference: %v", err)
		http.Error(w, "Failed to remove library reference", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library-detail?library_id="+parentIDStr, http.StatusSeeOther)
}
