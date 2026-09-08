package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
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
	Presets            []*manmanpb.GameAddonPathPreset
	Servers            []*manmanpb.Server
	SelectedServer     *manmanpb.Server
}

// WorkshopInstallationsPageData holds data for installations page
type WorkshopInstallationsPageData struct {
	Title              string
	Active             string
	User               *htmxauth.UserInfo
	Config             *manmanpb.GameConfig
	Installations      []*manmanpb.WorkshopInstallation
	AvailableLibraries []*manmanpb.WorkshopLibrary
}

func (app *App) handleWorkshopLibrary(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

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

	// Recent batch jobs (FR4, plan #2175): ListBatchJobs is scoped to a
	// single game_id, so gather the latest few per game shown on this page
	// and merge -- batch_job_id is assigned in creation order, so sorting by
	// it descending is equivalent to newest-first across games without a
	// second timestamp comparison.
	var recentBatchJobs []*manmanpb.WorkshopBatchJob
	for _, game := range games {
		jobs, err := app.grpc.ListBatchJobs(ctx, game.GameId, 5)
		if err != nil {
			log.Printf("Error fetching batch jobs for game %d: %v", game.GameId, err)
			continue
		}
		recentBatchJobs = append(recentBatchJobs, jobs...)
	}
	sort.Slice(recentBatchJobs, func(i, j int) bool {
		return recentBatchJobs[i].BatchJobId > recentBatchJobs[j].BatchJobId
	})
	if len(recentBatchJobs) > 8 {
		recentBatchJobs = recentBatchJobs[:8]
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop/library"},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Workshop Library", "workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	pageData := pages.WorkshopLibraryPageData{
		Layout:          layoutData,
		Games:           games,
		Libraries:       libraries,
		RecentAddons:    recentAddons,
		Addons:          addons,
		RecentBatchJobs: recentBatchJobs,
	}

	RenderTempl(w, r, "Workshop Library", pages.WorkshopLibrary(pageData))
}

func (app *App) handleWorkshopSearch(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

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

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop/library"},
		{Label: "Search", URL: "/workshop/search"},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Workshop Search", "workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	data := pages.WorkshopSearchPageData{
		Layout:     layoutData,
		Query:      query,
		GameID:     gameID,
		TypeFilter: typeFilter,
		Games:      games,
		Libraries:  libraries,
		Addons:     addons,
	}

	RenderTempl(w, r, "Workshop Search", pages.WorkshopSearch(data))
}

func (app *App) handleWorkshopAddonDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

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

	var game *manmanpb.Game
	games, _ := app.grpc.ListGames(ctx)
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

	var collectionChildren []*manmanpb.WorkshopAddon
	if addon.IsCollection {
		collectionChildren, err = app.grpc.ListCollectionChildren(ctx, addonID)
		if err != nil {
			log.Printf("Error fetching collection children: %v", err)
		}
	}

	var parentCollection *manmanpb.WorkshopAddon
	if addon.CollectionId != 0 {
		parentCollection, err = app.grpc.GetWorkshopAddon(ctx, addon.CollectionId)
		if err != nil {
			log.Printf("Error fetching parent collection: %v", err)
		}
	}

	addonName := addon.Name
	if addonName == "" {
		addonName = "Addon " + addonIDStr
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop/library"},
		{Label: "Addons", URL: "/workshop/search?type=addon"},
		{Label: addonName, URL: ""},
	}
	layoutData, err := app.buildTemplLayoutData(r, addonName, "workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	data := pages.WorkshopAddonDetailPageData{
		Layout:              layoutData,
		Addon:               addon,
		Game:                game,
		ContainingLibraries: containingLibraries,
		AvailableLibraries:  availableLibraries,
		CollectionChildren:  collectionChildren,
		ParentCollection:    parentCollection,
	}

	RenderTempl(w, r, addonName, pages.WorkshopAddonDetail(data))
}

func (app *App) handleWorkshopInstallations(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

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

	libraries, err := app.grpc.ListLibraries(ctx, 200, 0, config.GameId)
	if err != nil {
		log.Printf("Error fetching libraries: %v", err)
		http.Error(w, "Failed to fetch libraries", http.StatusInternalServerError)
		return
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Games", URL: "/games"},
		{Label: fmt.Sprintf("Config %d", config.ConfigId), URL: fmt.Sprintf("/games/%d/configs/%d", config.GameId, config.ConfigId)},
		{Label: "Workshop Installations", URL: ""},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Workshop Installations", "Workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Workshop Installations", pages.WorkshopInstallations(layoutData, config, installations, libraries)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

func (app *App) handleInstallAddon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

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

	ctx := r.Context()

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

func (app *App) handleResetInstallation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	installationIDStr := r.FormValue("installation_id")
	configIDStr := r.FormValue("config_id")

	installationID, err := strconv.ParseInt(installationIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid installation_id", http.StatusBadRequest)
		return
	}

	_, err = app.grpc.ResetInstallation(ctx, installationID)
	if err != nil {
		log.Printf("Error resetting installation: %v", err)
		http.Error(w, "Failed to reset installation", http.StatusInternalServerError)
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

	ctx := r.Context()

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

	presets, _ := app.grpc.ListAddonPathPresets(ctx, gameID)

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
		// A collection reports 0 bytes from Steam — its content lives on its children,
		// which are created as their own addons (with their own real sizes) on save.
		sizeStr = strconv.FormatInt(int64(addon.CollectionItemCount), 10) + " items on save"
	}
	fileSizeBytesStr := strconv.FormatInt(addon.FileSizeBytes, 10)

	presetOptions := ""
	if len(presets) != 1 {
		presetOptions = `<option value="0">— none —</option>`
	}
	for _, p := range presets {
		selected := ""
		if len(presets) == 1 {
			selected = ` selected`
		}
		presetOptions += `<option value="` + strconv.FormatInt(p.PresetId, 10) + `"` + selected + `>` + p.Name + ` (` + p.InstallationPath + `)</option>`
	}

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
<div>
<label style="font-size:12px;font-weight:600;color:#374151;display:block;margin-bottom:3px;">Path Preset</label>
<select name="preset_id" style="width:100%;padding:7px 10px;border:1px solid #d1d5db;border-radius:5px;font-size:14px;">` + presetOptions + `</select>
</div>
<div>
<label style="font-size:12px;font-weight:600;color:#374151;display:block;margin-bottom:3px;">Installation Path <span style="font-weight:400;color:#6b7280;">(overrides preset)</span></label>
<input type="text" name="installation_path" placeholder="e.g. /serverfiles/game/addons" style="width:100%;padding:7px 10px;border:1px solid #d1d5db;border-radius:5px;font-size:14px;">
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

	ctx := r.Context()

	gameIDStr := r.FormValue("game_id")
	workshopID := r.FormValue("workshop_id")
	platformType := r.FormValue("platform_type")
	name := r.FormValue("name")
	description := r.FormValue("description")
	fileSizeBytesStr := r.FormValue("file_size_bytes")
	isCollectionStr := r.FormValue("is_collection")
	installationPath := r.FormValue("installation_path")
	presetIDStr := r.FormValue("preset_id")

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
	var presetID int64
	if presetIDStr != "" {
		presetID, _ = strconv.ParseInt(presetIDStr, 10, 64)
	}

	addon, err := app.grpc.CreateAddon(ctx, gameID, workshopID, platformType, name, description, fileSizeBytes, isCollection, installationPath, presetID)
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

	ctx := r.Context()

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

	ctx := r.Context()

	libraryIDStr := r.FormValue("library_id")
	name := r.FormValue("name")
	description := r.FormValue("description")
	presetIDStr := r.FormValue("preset_id")

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	var presetID int64
	if presetIDStr != "" {
		presetID, err = strconv.ParseInt(presetIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid preset_id", http.StatusBadRequest)
			return
		}
	}

	_, err = app.grpc.UpdateLibrary(ctx, libraryID, name, description, presetID)
	if err != nil {
		log.Printf("Error updating library: %v", err)
		http.Error(w, "Failed to update library", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/workshop/library-detail?library_id="+libraryIDStr, http.StatusSeeOther)
}

// AvailableAddonsData holds data for the HTMX available addons partial
// LEGACY: These types are no longer used (migrated to templ components)
// type AvailableAddonsData struct {
// 	Addons    []*manmanpb.WorkshopAddon
// 	LibraryID int64
// }
// type AvailableLibrariesData struct {
// 	Libraries []*manmanpb.WorkshopLibrary
// 	LibraryID int64
// }

func (app *App) handleAvailableAddons(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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

	w.Header().Set("Content-Type", "text/html")
	RenderTempl(w, r, "", components.WorkshopAvailableAddons(components.WorkshopAvailableAddonsProps{
		Addons:    available,
		LibraryID: libraryID,
	}))
}

func (app *App) handleAvailableLibraries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

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

	w.Header().Set("Content-Type", "text/html")
	RenderTempl(w, r, "", components.WorkshopAvailableLibraries(components.WorkshopAvailableLibrariesProps{
		Libraries: available,
		LibraryID: libraryID,
	}))
}

func (app *App) handleDeleteAddon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

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

// loadWorkshopLibraryDetailData gathers the data needed to render the
// library detail page. It is shared by the GET handler and by
// handleBulkAddCollection/handleBatchCreateAddons (FR1/FR2, plan #2175),
// which re-render this page inline -- rather than redirecting -- on a
// job-level RPC failure so the Server Manager's pasted text isn't lost.
func (app *App) loadWorkshopLibraryDetailData(ctx context.Context, libraryID int64) (library *manmanpb.WorkshopLibrary, game *manmanpb.Game, games []*manmanpb.Game, addons, availableAddons []*manmanpb.WorkshopAddon, presets []*manmanpb.GameAddonPathPreset, err error) {
	library, err = app.grpc.GetLibrary(ctx, libraryID)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, err
	}

	// Get addons in this library
	addons, err = app.grpc.GetLibraryAddons(ctx, libraryID)
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
	for _, a := range allAddons {
		if !inLibrary[a.AddonId] {
			availableAddons = append(availableAddons, a)
		}
	}

	games, _ = app.grpc.ListGames(ctx)
	for _, g := range games {
		if g.GameId == library.GameId {
			game = g
			break
		}
	}

	presets, err = app.grpc.ListAddonPathPresets(ctx, library.GameId)
	if err != nil {
		log.Printf("Error fetching presets: %v", err)
		presets = []*manmanpb.GameAddonPathPreset{}
	}

	return library, game, games, addons, availableAddons, presets, nil
}

func (app *App) handleLibraryDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

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

	library, game, games, addons, availableAddons, presets, err := app.loadWorkshopLibraryDetailData(ctx, libraryID)
	if err != nil {
		log.Printf("Error fetching library: %v", err)
		http.Error(w, "Failed to fetch library", http.StatusInternalServerError)
		return
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop/library"},
		{Label: library.Name, URL: ""},
	}
	layoutData, err := app.buildTemplLayoutData(r, library.Name, "workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	data := pages.WorkshopLibraryDetailPageData{
		Layout:          layoutData,
		Library:         library,
		Game:            game,
		Games:           games,
		Addons:          addons,
		AvailableAddons: availableAddons,
		Presets:         presets,
	}

	RenderTempl(w, r, library.Name, pages.WorkshopLibraryDetail(data))
}

// renderWorkshopLibraryDetailWithFormState re-renders the library detail
// page inline (no redirect) after a job-level failure from
// handleBulkAddCollection or handleBatchCreateAddons, preserving whichever
// form's error/echoed input is non-empty so the Server Manager doesn't have
// to retype a pasted collection ID/URL or batch block (FR1/FR2/FR3, plan
// #2175).
func (app *App) renderWorkshopLibraryDetailWithFormState(w http.ResponseWriter, r *http.Request, libraryID int64, bulkAddErr, bulkAddInput, batchErr, batchEntries string) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	library, game, games, addons, availableAddons, presets, err := app.loadWorkshopLibraryDetailData(ctx, libraryID)
	if err != nil {
		log.Printf("Error fetching library: %v", err)
		http.Error(w, "Failed to fetch library", http.StatusInternalServerError)
		return
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop/library"},
		{Label: library.Name, URL: ""},
	}
	layoutData, err := app.buildTemplLayoutData(r, library.Name, "workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	data := pages.WorkshopLibraryDetailPageData{
		Layout:                 layoutData,
		Library:                library,
		Game:                   game,
		Games:                  games,
		Addons:                 addons,
		AvailableAddons:        availableAddons,
		Presets:                presets,
		BulkAddCollectionError: bulkAddErr,
		BulkAddCollectionInput: bulkAddInput,
		BatchCreateError:       batchErr,
		BatchCreateEntries:     batchEntries,
	}

	RenderTempl(w, r, library.Name, pages.WorkshopLibraryDetail(data))
}

// handleBulkAddCollection resolves a Steam Workshop collection's current
// membership and adds every item to a library in one action (FR1, plan
// #2175), then hands off to the batch-status view (#2179) for the returned
// batch_job_id -- the Server Manager lands on per-item results, not a
// spinner or a bare toast.
func (app *App) handleBulkAddCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	gameIDStr := r.FormValue("game_id")
	libraryIDStr := r.FormValue("library_id")
	collectionInput := r.FormValue("collection_input")
	presetIDStr := r.FormValue("preset_id")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	// Required-field rejection happens before any RPC call -- an empty
	// collection input can never resolve to a collection, so there is
	// nothing useful for the server to do with it.
	if strings.TrimSpace(collectionInput) == "" {
		app.renderWorkshopLibraryDetailWithFormState(w, r, libraryID, "Collection ID or Workshop URL is required.", collectionInput, "", "")
		return
	}

	var presetID int64
	if presetIDStr != "" {
		presetID, _ = strconv.ParseInt(presetIDStr, 10, 64)
	}

	resp, err := app.grpc.AddCollectionToLibrary(ctx, gameID, libraryID, collectionInput, presetID)
	if err != nil {
		log.Printf("Error adding collection to library: %v", err)
		app.renderWorkshopLibraryDetailWithFormState(w, r, libraryID, "Could not add this collection: "+err.Error(), collectionInput, "", "")
		return
	}

	http.Redirect(w, r, "/workshop/batch-status?batch_job_id="+strconv.FormatInt(resp.BatchJobId, 10), http.StatusSeeOther)
}

// handleBatchCreateAddons takes a pasted block of mixed raw Workshop IDs and
// Workshop URLs and creates an addon per valid entry (FR2, plan #2175),
// then hands off to the batch-status view (#2179) for the returned
// batch_job_id, same as handleBulkAddCollection. A partial-failure result
// (completed_with_errors) is still a successful RPC call -- it is reported
// per-item on the batch-status view (FR3), not surfaced as a UI-level error
// here.
func (app *App) handleBatchCreateAddons(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	gameIDStr := r.FormValue("game_id")
	libraryIDStr := r.FormValue("library_id")
	entries := r.FormValue("entries")
	presetIDStr := r.FormValue("preset_id")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	libraryID, err := strconv.ParseInt(libraryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid library_id", http.StatusBadRequest)
		return
	}

	// Required-field rejection happens before any RPC call. Note this only
	// rejects a wholly-empty textarea -- individual bad lines within a
	// non-empty block are the server's job to flag per-item (FR3), not the
	// UI's job to pre-filter (see handlers_workshop_bulk_test.go).
	if strings.TrimSpace(entries) == "" {
		app.renderWorkshopLibraryDetailWithFormState(w, r, libraryID, "", "", "At least one Workshop ID or URL is required.", entries)
		return
	}

	var presetID int64
	if presetIDStr != "" {
		presetID, _ = strconv.ParseInt(presetIDStr, 10, 64)
	}

	resp, err := app.grpc.BatchCreateAddons(ctx, gameID, libraryID, entries, presetID)
	if err != nil {
		log.Printf("Error batch-creating addons: %v", err)
		app.renderWorkshopLibraryDetailWithFormState(w, r, libraryID, "", "", "Could not process this batch: "+err.Error(), entries)
		return
	}

	http.Redirect(w, r, "/workshop/batch-status?batch_job_id="+strconv.FormatInt(resp.BatchJobId, 10), http.StatusSeeOther)
}

func (app *App) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	gameIDStr := r.FormValue("game_id")
	name := r.FormValue("name")
	description := r.FormValue("description")
	presetIDStr := r.FormValue("preset_id")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	var presetID int64
	if presetIDStr != "" {
		presetID, err = strconv.ParseInt(presetIDStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid preset_id", http.StatusBadRequest)
			return
		}
	}

	library, err := app.grpc.CreateLibrary(ctx, gameID, name, description, presetID)
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

	ctx := r.Context()

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

	ctx := r.Context()

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

	ctx := r.Context()

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

	ctx := r.Context()

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

	ctx := r.Context()

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

func (app *App) handlePresetsForGame(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	gameIDStr := r.URL.Query().Get("game_id")

	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game_id", http.StatusBadRequest)
		return
	}

	presets, err := app.grpc.ListAddonPathPresets(ctx, gameID)
	if err != nil {
		log.Printf("Error fetching presets: %v", err)
		presets = []*manmanpb.GameAddonPathPreset{}
	}

	// Return HTML for preset selector
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<label>Default Path Preset <small style="color:#9ca3af;">(Optional)</small></label>
<select name="preset_id">
    <option value="">-- No preset --</option>`)

	for _, preset := range presets {
		fmt.Fprintf(w, `<option value="%d">%s (%s)</option>`,
			preset.PresetId, preset.Name, preset.InstallationPath)
	}

	fmt.Fprintf(w, `</select>`)
}

// handleWorkshopBatchStatus renders the manual-reload batch-status view for
// a Workshop collection add or batch create job (FR4, plan #2175). Reloads
// are entirely manual (the page's "Refresh" link does a plain navigation) --
// no SSE/polling/hx-trigger is wired here, by design (FR4 scope boundary).
func (app *App) handleWorkshopBatchStatus(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	batchJobIDStr := r.URL.Query().Get("batch_job_id")
	if batchJobIDStr == "" {
		http.Error(w, "batch_job_id required", http.StatusBadRequest)
		return
	}

	batchJobID, err := strconv.ParseInt(batchJobIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid batch_job_id", http.StatusBadRequest)
		return
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop/library"},
		{Label: "Batch Status", URL: "/workshop/batch-status"},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Batch Job Status", "workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	job, items, err := app.grpc.GetBatchJob(ctx, batchJobID)
	if err != nil {
		// Unknown batch_job_id (control-api returns NotFound) renders the
		// "not found" state rather than an error page -- there is no
		// well-known distinction to make between NotFound and other RPC
		// failures from this read-only view.
		log.Printf("Error fetching batch job %d: %v", batchJobID, err)
		data := pages.WorkshopBatchStatusPageData{
			Layout: layoutData,
			Job:    nil,
			Items:  nil,
		}
		RenderTempl(w, r, "Batch Job Status", pages.WorkshopBatchStatus(data))
		return
	}

	data := pages.WorkshopBatchStatusPageData{
		Layout: layoutData,
		Job:    job,
		Items:  items,
	}

	RenderTempl(w, r, "Batch Job Status", pages.WorkshopBatchStatus(data))
}

// handleWorkshopCache renders the Admin fleet-wide Workshop cache visibility
// view for a single addon (FR10, plan #2175): every content-addressed cache
// entry, which hosts hold a copy, and each entry's staleness -- from one
// place, without querying hosts one at a time. Manual reload only, same M4
// boundary as the batch-status view: no polling or SSE.
func (app *App) handleWorkshopCache(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

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

	// Best-effort: an unresolvable addon name still renders the cache view
	// (an addon_id-only title/breadcrumb) rather than failing the page --
	// ListAddonCacheEntries below is the RPC that actually determines whether
	// the addon exists.
	addonName := "Addon " + addonIDStr
	if addon, addonErr := app.grpc.GetWorkshopAddon(ctx, addonID); addonErr == nil && addon.Name != "" {
		addonName = addon.Name
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop/library"},
		{Label: addonName, URL: fmt.Sprintf("/workshop/addon?addon_id=%d", addonID)},
		{Label: "Cache", URL: ""},
	}
	layoutData, err := app.buildTemplLayoutData(r, "Workshop Cache", "workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	entries, err := app.grpc.ListAddonCacheEntries(ctx, addonID)
	if err != nil {
		// Unknown addon_id (control-api returns NotFound) renders the empty
		// state rather than an error page, matching the batch-status view's
		// precedent for a read-only lookup by id.
		log.Printf("Error fetching workshop cache entries for addon %d: %v", addonID, err)
		entries = nil
	}

	data := pages.WorkshopCachePageData{
		Layout:      layoutData,
		AddonID:     addonID,
		Entries:     entries,
		VerifyState: r.URL.Query().Get("verify_status"),
		VerifyServerID: func() int64 {
			id, _ := strconv.ParseInt(r.URL.Query().Get("verify_server_id"), 10, 64)
			return id
		}(),
	}

	RenderTempl(w, r, "Workshop Cache", pages.WorkshopCache(data))
}

// handleWorkshopCacheVerify dispatches an Admin's on-demand SteamCMD verify of a single
// cache entry (FR11, plan #2175, #2186). Dispatch is fire-and-forget from this handler's
// perspective -- the up-to-date/changed outcome is not part of the RPC response and only
// appears on the cache view's next manual reload (same M4 boundary as the rest of this
// view: no polling/SSE). The redirect carries the dispatch outcome as a query param so
// the cache page can render it as an inline banner rather than a toast, and so a
// no_host_available result renders as a clear message rather than an error page.
func (app *App) handleWorkshopCacheVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	addonIDStr := r.FormValue("addon_id")
	cacheEntryIDStr := r.FormValue("cache_entry_id")

	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}
	cacheEntryID, err := strconv.ParseInt(cacheEntryIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid cache_entry_id", http.StatusBadRequest)
		return
	}

	redirectURL := fmt.Sprintf("/workshop/cache?addon_id=%d", addonID)

	// serverID 0: let control-api pick a host that already holds a copy (FR11).
	resp, err := app.grpc.VerifyCacheEntry(ctx, cacheEntryID, 0)
	if err != nil {
		log.Printf("Error dispatching workshop cache verify for cache_entry_id %d: %v", cacheEntryID, err)
		http.Redirect(w, r, redirectURL+"&verify_status=error", http.StatusSeeOther)
		return
	}

	if !resp.Dispatched {
		// no_host_available is a real, reportable outcome, not an error (issue: "renders
		// as a clear inline message, not an error toast").
		http.Redirect(w, r, redirectURL+"&verify_status="+resp.Status, http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("%s&verify_status=dispatched&verify_server_id=%d", redirectURL, resp.ServerId), http.StatusSeeOther)
}

// handleWorkshopCacheEvict implements FR12's UI entry point: Admin manual
// eviction of exactly one content-addressed cache entry, submitted from the
// confirmation step on workshop_cache.templ that names the specific content
// version being evicted (the destructive scope is exactly one version, and
// the confirmation makes that unambiguous before the request is sent).
// Redirects back to the cache view for the same addon so the evicted row
// disappears on the reloaded list while every sibling version remains.
func (app *App) handleWorkshopCacheEvict(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()

	addonIDStr := r.FormValue("addon_id")
	addonID, err := strconv.ParseInt(addonIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid addon_id", http.StatusBadRequest)
		return
	}

	cacheEntryID, err := strconv.ParseInt(r.FormValue("cache_entry_id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid cache_entry_id", http.StatusBadRequest)
		return
	}

	if _, err := app.grpc.EvictCacheEntry(ctx, cacheEntryID); err != nil {
		log.Printf("Error evicting workshop cache entry %d: %v", cacheEntryID, err)
		http.Error(w, "Failed to evict cache entry", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/workshop/cache?addon_id=%d", addonID), http.StatusSeeOther)
}
