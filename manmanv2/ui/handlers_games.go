package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

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

// handleGames renders the Games page's flat list (root plan #2266, task
// #2270 -- FR4, FR5, NFR7, WD1, WD6).
//
// Data assembly is a constant number (five) of fleet-wide list calls --
// games, game configs, deployments (server game configs), servers, and
// live sessions -- joined in the UI. NFR7 requires this count not grow
// with the number of games, configs, or deployments rendered: no call is
// issued inside a per-game or per-deployment loop, and expanding a row
// client-side (FR4, in games.templ) issues no additional request.
//
// The join: a deployment's game_config_id resolves to a GameConfig,
// whose game_id resolves to a Game (ServerGameConfig itself carries no
// game_id -- see the ground-truth table on issue #2270). Run-state and
// connect-address are then rolled up per game from that game's
// deployments, using components.ComputeDeploymentStatus /
// components.LatestSession / components.BuildConnectAddressView --
// never ServerGameConfig.status, which is the unrelated active/inactive
// lifecycle flag (FR5).
//
// Task #2272 (FR6) additionally builds each game's expanded-row
// Deployments section from this same in-memory join: per-deployment
// Start/Stop/Restart availability and the session-status badge reuse
// components.ComputeDeploymentActions / pages.DeploymentRow verbatim (the
// same derivation and markup buildDeploymentRowData/DeploymentRow render on
// /sessions, #1627) so the two surfaces cannot drift on the same
// deployment's state -- see buildGameDeploymentRow.
func (app *App) handleGames(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	games, err := app.grpc.ListGames(ctx)
	if err != nil {
		log.Printf("Error fetching games: %v", err)
		http.Error(w, "Failed to fetch games", http.StatusInternalServerError)
		return
	}

	// 0 = no game_id / server_id filter, i.e. fleet-wide (see
	// ListGameConfigsRequest.game_id / ListServerGameConfigsRequest.server_id
	// doc comments in manmanv2/protos/api_messages_game.proto and
	// messages.proto; the same 0-means-all convention handleGameDetail
	// already relies on for SGC counts).
	configs, err := app.grpc.ListGameConfigs(ctx, 0)
	if err != nil {
		log.Printf("Warning: failed to fetch game configs: %v", err)
		configs = nil
	}
	deployments, err := app.grpc.ListServerGameConfigs(ctx, 0)
	if err != nil {
		log.Printf("Warning: failed to fetch deployments: %v", err)
		deployments = nil
	}
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Warning: failed to fetch servers: %v", err)
		servers = nil
	}
	// liveOnly=false (task #2272): the collapsed row's rollup only ever
	// needed a deployment's *live* session, but the expanded row's
	// Deployments section (FR6) must feed components.ComputeDeploymentActions
	// the same way buildDeploymentRowData does on /sessions -- which means
	// knowing the latest session even for a stopped/crashed/lost deployment,
	// not just a currently-live one, so Start/Restart availability doesn't
	// silently go blank for every non-running row. Same call, same count
	// (NFR7): only the filter argument changed, not the number of fleet-wide
	// calls handleGames makes.
	sessions, err := app.grpc.ListSessions(ctx, false)
	if err != nil {
		log.Printf("Warning: failed to fetch sessions: %v", err)
		sessions = nil
	}

	// expand (spec amendment A1, migration task): an entry-point hint,
	// not persisted page state. Absent, non-numeric, or stale (no
	// matching game) all resolve to 0 / "expand nothing" and are never
	// an error -- gameRowExpanded in games.templ does the stale check.
	expandGameID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("expand")), 10, 64)

	rows := buildGameRows(games, configs, deployments, servers, sessions)

	breadcrumbs := []components.Breadcrumb{
		{Label: "Games", URL: "/games"},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Games", "Games", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	data := pages.GamesPageData{
		Layout:       layoutData,
		Games:        rows,
		ExpandGameID: expandGameID,
	}

	if err := RenderTempl(w, r, "Games", pages.Games(data)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// buildGameRows performs the UI-side join (NFR7) from the five fleet-wide
// list results into one pages.GameRow per game. It issues no RPCs itself
// -- all data is already in memory -- so it can be exercised directly by
// tests without a fake gRPC client.
//
// ServerGameConfig carries no game_id (only GameConfig does), so a
// deployment resolves to a game via its game_config_id -> GameConfig.game_id.
// A deployment whose game_config_id has no matching GameConfig (e.g. the
// config was deleted after the deployment was created) is skipped -- it
// cannot be attributed to any game.
//
// It also populates each row's Configs (FR7, task #2273): every
// GameConfig of that game, with a deployment count derived from the same
// deployments slice already joined above -- no new call, no per-config
// query (NFR7). The Workshop Libraries panel (FR8/FR9/FR10, task #2367)
// is deliberately not part of this join at all -- it is a lazily-fetched
// fragment now, see pages.WorkshopPanelData's doc comment for why.
func buildGameRows(
	games []*manmanpb.Game,
	configs []*manmanpb.GameConfig,
	deployments []*manmanpb.ServerGameConfig,
	servers []*manmanpb.Server,
	sessions []*manmanpb.Session,
) []pages.GameRow {
	configByID := make(map[int64]*manmanpb.GameConfig, len(configs))
	for _, c := range configs {
		configByID[c.GetConfigId()] = c
	}

	serverByID := make(map[int64]*manmanpb.Server, len(servers))
	for _, s := range servers {
		serverByID[s.GetServerId()] = s
	}

	sessionsBySGC := make(map[int64][]*manmanpb.Session, len(sessions))
	for _, s := range sessions {
		sgcID := s.GetServerGameConfigId()
		sessionsBySGC[sgcID] = append(sessionsBySGC[sgcID], s)
	}

	deploymentsByGame := make(map[int64][]*manmanpb.ServerGameConfig)
	// deploymentCountByConfig backs FR7's per-config deployment count: a
	// single pass over the already-fetched deployments slice, keyed by
	// game_config_id directly (unlike deploymentsByGame above, this one
	// does not need a config to resolve to a game -- an orphaned
	// game_config_id still just never matches any rendered ConfigRowView).
	deploymentCountByConfig := make(map[int64]int, len(deployments))
	for _, d := range deployments {
		deploymentCountByConfig[d.GetGameConfigId()]++

		cfg, ok := configByID[d.GetGameConfigId()]
		if !ok {
			continue
		}
		gameID := cfg.GetGameId()
		deploymentsByGame[gameID] = append(deploymentsByGame[gameID], d)
	}

	// configsByGame backs FR7's Configurations section: every GameConfig
	// grouped by its game_id, a single pass over the already-fetched
	// configs slice (NFR7 -- no per-game query).
	configsByGame := make(map[int64][]*manmanpb.GameConfig)
	for _, c := range configs {
		gameID := c.GetGameId()
		configsByGame[gameID] = append(configsByGame[gameID], c)
	}

	rows := make([]pages.GameRow, 0, len(games))
	for _, game := range games {
		gameDeployments := deploymentsByGame[game.GetGameId()]
		// Sorted so connect-address selection below (first running
		// deployment with a resolvable address) is deterministic
		// regardless of the RPC's own return order.
		sort.Slice(gameDeployments, func(i, j int) bool {
			return gameDeployments[i].GetServerGameConfigId() < gameDeployments[j].GetServerGameConfigId()
		})

		runState := components.DeploymentStopped
		connect := components.ConnectAddressView{Unavailable: true}
		deploymentRows := make([]pages.GameDeploymentRow, 0, len(gameDeployments))
		for _, d := range gameDeployments {
			latest := components.LatestSession(sessionsBySGC[d.GetServerGameConfigId()])
			server := serverByID[d.GetServerId()]
			cfg := configByID[d.GetGameConfigId()]

			if components.ComputeDeploymentStatus(latest) == components.DeploymentRunning {
				runState = components.DeploymentRunning
				if connect.Unavailable {
					if view := components.BuildConnectAddressView(server.GetHostPublicAddress(), d.GetPortBindings()); !view.Unavailable {
						connect = view
					}
				}
			}

			deploymentRows = append(deploymentRows, buildGameDeploymentRow(game, cfg, server, d, latest))
		}

		gameConfigs := configsByGame[game.GetGameId()]
		configRows := make([]pages.ConfigRowView, 0, len(gameConfigs))
		for _, c := range gameConfigs {
			configRows = append(configRows, pages.ConfigRowView{
				GameID:          game.GetGameId(),
				ConfigID:        c.GetConfigId(),
				Name:            c.GetName(),
				Image:           c.GetImage(),
				DeploymentCount: deploymentCountByConfig[c.GetConfigId()],
			})
		}
		// Deterministic sort, same rationale as the games sort below: by
		// name, tie-broken by config_id.
		sort.Slice(configRows, func(i, j int) bool {
			if configRows[i].Name != configRows[j].Name {
				return configRows[i].Name < configRows[j].Name
			}
			return configRows[i].ConfigID < configRows[j].ConfigID
		})

		rows = append(rows, pages.GameRow{
			GameID:      game.GetGameId(),
			Name:        game.GetName(),
			RunState:    runState,
			Connect:     connect,
			Deployments: deploymentRows,
			Configs:     configRows,
		})
	}

	// Deterministic sort (task #2270): by name, tie-broken by game_id, so
	// two operators loading the page see the same order regardless of
	// ListGames' own return order.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].GameID < rows[j].GameID
	})

	return rows
}

// buildGameDeploymentRow builds one row of a game's expanded Deployments
// section (#2272, FR6). Row.Actions/Row.LatestSession/Row.SGCStatus/
// Row.LiveSession are exactly the fields pages.DeploymentRow /
// DeploymentRowInner (#1627) already know how to render Start/Stop/Restart
// availability and the session-status badge from -- components.
// ComputeDeploymentActions(latest) is called here directly (the same
// function buildDeploymentRowData calls on /sessions), not re-derived, so
// the two surfaces cannot independently disagree about a control's
// availability for the same deployment state (FR6's anti-drift
// requirement). Everything else on GameDeploymentRow (Connect, LogsURL,
// ActionsURL, and DisplayName's "config on server" naming, FR2) is
// additive to that reused derivation, never a substitute for it.
//
// liveSession mirrors the server-side live_only filter
// (manmanv2/api/repository/postgres/session.go: status IN pending,
// starting, running, stopping) so DeploymentRowInner's "View Live Session"
// column renders the same way it would from a dedicated getLiveSession
// call, without a second RPC.
func buildGameDeploymentRow(game *manmanpb.Game, cfg *manmanpb.GameConfig, server *manmanpb.Server, d *manmanpb.ServerGameConfig, latest *manmanpb.Session) pages.GameDeploymentRow {
	serverName := server.GetName()
	if serverName == "" {
		serverName = fmt.Sprintf("server %d", d.GetServerId())
	}
	configName := cfg.GetName()
	if configName == "" {
		configName = fmt.Sprintf("config %d", d.GetGameConfigId())
	}
	// "<config> on <server>" (FR2): this is the deployment's display name,
	// distinct from buildDeploymentRowData's "<config> (<game>)" format on
	// /sessions -- the game is already the context this row renders inside,
	// so naming repeats the game here instead of the server would be
	// redundant and less useful than knowing which server it's on.
	displayName := fmt.Sprintf("%s on %s", configName, serverName)

	var liveSession *manmanpb.Session
	if latest != nil && (latest.GetStatus() == "running" || components.IsTransientStatus(latest.GetStatus())) {
		liveSession = latest
	}

	var logsURL string
	if latest != nil {
		logsURL = fmt.Sprintf("/sessions/%d", latest.GetSessionId())
	}

	return pages.GameDeploymentRow{
		Row: pages.DeploymentRowData{
			ServerGameConfigID: d.GetServerGameConfigId(),
			DisplayName:        displayName,
			SGCStatus:          d.GetStatus(),
			LatestSession:      latest,
			LiveSession:        liveSession,
			Actions:            components.ComputeDeploymentActions(latest),
		},
		Connect: components.BuildConnectAddressView(server.GetHostPublicAddress(), d.GetPortBindings()),
		LogsURL: logsURL,
		// The config-level Actions management page (C23/M3): the existing
		// Actions surface FR6 links out to, per decision 8 (not reshaped,
		// not inlined, not a Config Editor tab). There is no routed
		// deployment-level (server_game_config) Actions page today --
		// categorizeActions in handlers_actions.go builds that URL shape for
		// display only, main.go never registers a handler for it -- so this
		// links to the one that is actually routed and already shows this
		// deployment's config-level (and inherited game-level) actions.
		ActionsURL: fmt.Sprintf("/games/%d/configs/%d/actions", game.GetGameId(), cfg.GetConfigId()),
	}
}

func (app *App) handleGameNew(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	game := &manmanpb.Game{
		Metadata: &manmanpb.GameMetadata{},
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Games", URL: "/games"},
		{Label: "Create", URL: "/games/new"},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Create Game", "Games", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Create Game", pages.GameForm(layoutData, game, false)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleGameCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	
	ctx := r.Context()
	
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
		case "workshop-panel":
			// GET /games/{id}/workshop-panel (task #2367, FR8/FR9/FR10):
			// the lazily-fetched Workshop Libraries panel fragment -- see
			// gameWorkshopPlaceholder's doc comment in games.templ and
			// handlers_games_libraries.go.
			app.handleGameWorkshopPanel(w, r, gameIDStr)
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
	
	ctx := r.Context()
	
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

	// Convert volume map to slice
	var volumeSlice []*manmanpb.GameConfigVolume
	for _, vol := range volumeMap {
		volumeSlice = append(volumeSlice, vol)
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Games", URL: "/games"},
		{Label: game.Name, URL: ""},
	}
	layoutData, err := app.buildTemplLayoutData(r, game.Name, "games", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Failed to build layout", http.StatusInternalServerError)
		return
	}

	data := pages.GameDetailPageData{
		Layout:      layoutData,
		Game:        game,
		PathPresets: pathPresets,
		Volumes:     volumeSlice,
		Configs:     configs,
		SgcCounts:   sgcCounts,
	}

	RenderTempl(w, r, game.Name, pages.GameDetail(data))
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
	
	ctx := r.Context()
	
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
	
	ctx := r.Context()
	
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
	Title          string
	Active         string
	User           *htmxauth.UserInfo
	Game           *manmanpb.Game
	Config         *manmanpb.GameConfig
	Servers        []*manmanpb.Server
	Deployments    []ServerGameConfigView
	DeployError    string
	Volumes        []*manmanpb.GameConfigVolume
	BackupConfigs  []*BackupConfigsForVolume // backup configs grouped by volume
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
		case "editor":
			// Config Editor blade (#2276, FR13): GET fetches the blade
			// fragment, POST validates and saves. See
			// handlers_config_editor.go.
			app.handleGameConfigEditor(w, r, gameIDStr, configIDStr)
			return
		case "volumes":
			// Handle volume routes
			if len(pathParts) > 5 {
				// /games/{id}/configs/{config_id}/volumes/{action_or_volume_id}
				actionOrVolumeID := pathParts[5]
				if actionOrVolumeID == "create" {
					app.handleGameConfigVolumeCreate(w, r, gameIDStr, configIDStr)
					return
				}
				if len(pathParts) > 6 && pathParts[6] == "backup-config" {
					// /games/{id}/configs/{config_id}/volumes/{volume_id}/backup-config/{assign|edit|remove}
					// (FR14/FR15, #2363: Config Editor Volumes tab inline
					// backup-config assign/edit/remove -- see
					// handlers_config_editor_volumes.go.)
					action := ""
					if len(pathParts) > 7 {
						action = pathParts[7]
					}
					app.handleConfigEditorVolumeBackupConfig(w, r, gameIDStr, configIDStr, actionOrVolumeID, action)
					return
				}
				// Assume it's a volume_id for delete
				app.handleGameConfigVolumeDelete(w, r, gameIDStr, configIDStr, actionOrVolumeID)
				return
			} else {
				// POST to /games/{id}/configs/{config_id}/volumes (create)
				if r.Method == http.MethodPost {
					app.handleGameConfigVolumeCreate(w, r, gameIDStr, configIDStr)
					return
				}
			}
		case "libraries":
			// GC-level Workshop library routes (task #2367, FR8/FR9):
			//   GET  /games/{id}/configs/{config_id}/libraries/available
			//   POST /games/{id}/configs/{config_id}/libraries/add
			//   POST /games/{id}/configs/{config_id}/libraries/{library_id}/remove
			// See handlers_games_libraries.go.
			if len(pathParts) > 5 {
				sub := pathParts[5]
				if sub == "available" {
					app.handleGameConfigAvailableLibraries(w, r, gameIDStr, configIDStr)
					return
				}
				if sub == "add" {
					app.handleGameConfigLibraryAdd(w, r, gameIDStr, configIDStr)
					return
				}
				if len(pathParts) > 6 && pathParts[6] == "remove" {
					app.handleGameConfigLibraryRemove(w, r, gameIDStr, configIDStr, sub)
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
	
	if configIDStr == "create" {
		app.handleGameConfigCreate(w, r, gameIDStr)
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
	
	ctx := r.Context()
	
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

	// Convert backup configs to templ format
	var templBackupConfigs []pages.ConfigBackupGroup
	for _, vol := range volumes {
		cfgs, err := app.grpc.ListBackupConfigs(ctx, vol.VolumeId)
		if err != nil {
			log.Printf("Warning: failed to fetch backup configs for volume %d: %v", vol.VolumeId, err)
			cfgs = []*manmanpb.BackupConfig{}
		}
		templBackupConfigs = append(templBackupConfigs, pages.ConfigBackupGroup{
			Volume:  vol,
			Configs: cfgs,
		})
	}

	// Convert deployments to templ format
	var templDeployments []pages.ServerGameConfigView
	for _, dep := range deployments {
		templDeployments = append(templDeployments, pages.ServerGameConfigView{
			Server: dep.Server,
			Config: dep.Config,
		})
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Games", URL: "/games"},
		{Label: game.Name, URL: fmt.Sprintf("/games/%d", game.GameId)},
		{Label: config.Name, URL: ""},
	}
	layoutData, err := app.buildTemplLayoutData(r, config.Name+" - "+game.Name, "games", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	pageData := pages.ConfigDetailPageData{
		Layout:        layoutData,
		Game:          game,
		Config:        config,
		Servers:       servers,
		Deployments:   templDeployments,
		DeployError:   deployError,
		Volumes:       volumes,
		BackupConfigs: templBackupConfigs,
	}

	RenderTempl(w, r, config.Name+" - "+game.Name, pages.ConfigDetail(pageData))
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

	portBindings, err := parsePortBindingsJSON(r.FormValue("port_bindings_json"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	_, err = app.grpc.DeployGameConfig(ctx, serverID, configID, portBindings)
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

// parsePortBindingsJSON parses a JSON array of {container_port, host_port, protocol}
// objects submitted via a hidden form field into PortBinding protos.
func parsePortBindingsJSON(raw string) ([]*manmanpb.PortBinding, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var entries []struct {
		ContainerPort int32  `json:"container_port"`
		HostPort      int32  `json:"host_port"`
		Protocol      string `json:"protocol"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("invalid port bindings: %w", err)
	}

	bindings := make([]*manmanpb.PortBinding, 0, len(entries))
	for _, e := range entries {
		if e.ContainerPort <= 0 || e.ContainerPort > 65535 {
			return nil, fmt.Errorf("invalid container port: %d", e.ContainerPort)
		}
		if e.HostPort <= 0 || e.HostPort > 65535 {
			return nil, fmt.Errorf("invalid host port: %d", e.HostPort)
		}
		protocol := strings.ToUpper(strings.TrimSpace(e.Protocol))
		if protocol != "TCP" && protocol != "UDP" {
			return nil, fmt.Errorf("invalid protocol: %q", e.Protocol)
		}
		bindings = append(bindings, &manmanpb.PortBinding{
			ContainerPort: e.ContainerPort,
			HostPort:      e.HostPort,
			Protocol:      protocol,
		})
	}
	return bindings, nil
}

func (app *App) handleGameConfigNew(w http.ResponseWriter, r *http.Request, gameIDStr string) {
	gameID, err := strconv.ParseInt(gameIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid game ID", http.StatusBadRequest)
		return
	}
	
	ctx := r.Context()
	
	game, err := app.grpc.GetGame(ctx, gameID)
	if err != nil {
		log.Printf("Error fetching game: %v", err)
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}
	
	user := htmxauth.GetUser(r.Context())

	breadcrumbs := []components.Breadcrumb{
		{Label: "Games", URL: "/games"},
		{Label: game.Name, URL: fmt.Sprintf("/games/%d", game.GameId)},
		{Label: "Create Config", URL: ""},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Create Configuration", "Games", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Create Configuration", pages.ConfigForm(layoutData, game, nil, false)); err != nil {
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
	
	ctx := r.Context()
	
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
	
	ctx := r.Context()
	
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
	
	ctx := r.Context()
	
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

	ctx := r.Context()
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
	volumeType := strings.TrimSpace(r.FormValue("volume_type"))
	readOnly := r.FormValue("read_only") == "true"

	if name == "" || containerPath == "" {
		http.Error(w, "Name and container path are required", http.StatusBadRequest)
		return
	}

	if volumeType == "" {
		volumeType = "bind"
	}

	ctx := r.Context()
	_, err = app.grpc.CreateGameConfigVolume(ctx, configID, name, description, containerPath, hostSubpath, readOnly, volumeType)
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

	ctx := r.Context()
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

	ctx := r.Context()

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

	ctx := r.Context()

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
