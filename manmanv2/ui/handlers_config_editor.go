package main

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleGameConfigEditor is the Config Editor blade's route (root plan
// #2266, task #2276 -- FR13): GET renders the blade fragment appended into
// the Games page via hx-get/hx-swap="beforeend" from #2273's Edit control
// (data-config-editor-trigger in games.templ); POST validates and, on
// success, issues the single explicit-update_paths UpdateGameConfigRequest
// that is FR13's whole point (see buildConfigEditorUpdateRequest's doc
// comment). Dispatched from handleGameConfigDetail's "editor" sub-route,
// the same pattern "edit"/"update-env"/"volumes" already use -- see that
// switch in handlers_games.go.
func (app *App) handleGameConfigEditor(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
	switch r.Method {
	case http.MethodGet:
		app.handleGameConfigEditorGet(w, r, gameIDStr, configIDStr)
	case http.MethodPost:
		app.handleGameConfigEditorSave(w, r, gameIDStr, configIDStr)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGameConfigEditorGet fetches the GameConfig and its volumes and
// renders the blade fragment directly (pages.ConfigEditor(...).Render),
// never through RenderTempl's full-page htmxbase layout -- see
// pages.ConfigEditorData's doc comment for why this must stay a bare
// fragment. Mirrors handleDeploymentRowFragment's rendering shape in
// handlers_deployment_actions.go.
func (app *App) handleGameConfigEditorGet(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
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

	config, err := app.grpc.GetGameConfig(ctx, configID)
	if err != nil {
		log.Printf("Error fetching config %d for Config Editor: %v", configID, err)
		http.Error(w, "Config not found", http.StatusNotFound)
		return
	}

	volumes, err := app.grpc.ListGameConfigVolumes(ctx, configID)
	if err != nil {
		log.Printf("Warning: failed to fetch volumes for config %d: %v", configID, err)
		volumes = nil
	}

	data := pages.ConfigEditorData{
		GameID:        gameID,
		ConfigID:      configID,
		Name:          config.Name,
		Image:         config.Image,
		ArgsTemplate:  config.ArgsTemplate,
		EnvVars:       sortedConfigEditorEnvVars(config.EnvTemplate),
		Volumes:       toConfigEditorVolumes(volumes),
		Dirty:         false,
		ActiveTab:     "basics",
		BackupLinkURL: fmt.Sprintf("/games/%d/configs/%d", gameID, configID),
	}

	w.Header().Set("Content-Type", "text/html")
	if err := pages.ConfigEditor(data).Render(ctx, w); err != nil {
		log.Printf("Error rendering Config Editor fragment for config %d: %v", configID, err)
	}
}

// handleGameConfigEditorSave validates and, on success, issues a single
// UpdateGameConfigRequest with explicit update_paths (FR13). On a
// validation failure it re-renders the blade fragment (htmx hx-target
// swaps it back into place via outerHTML) with the submitted values and
// per-field errors intact -- never a partial re-render that would lose
// values entered in a tab that is not currently visible, since all three
// tabs' fields are always present in the request body regardless of which
// tab was showing when Save was clicked.
func (app *App) handleGameConfigEditorSave(w http.ResponseWriter, r *http.Request, gameIDStr, configIDStr string) {
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

	name := strings.TrimSpace(r.FormValue("name"))
	image := strings.TrimSpace(r.FormValue("image"))
	// ArgsTemplate deliberately keeps whatever was submitted, including ""
	// -- FR13's whole point is that an emptied args template must actually
	// clear (see buildConfigEditorUpdateRequest's doc comment).
	argsTemplate := r.FormValue("args_template")
	envTemplate := parseConfigEditorEnvRows(r.Form["env_key"], r.Form["env_value"])

	var errs pages.ConfigEditorErrors
	if name == "" {
		errs.Name = "Name is required."
	}
	if image == "" {
		errs.Image = "Image is required."
	}

	ctx := r.Context()

	if errs.HasAny() {
		volumes, volErr := app.grpc.ListGameConfigVolumes(ctx, configID)
		if volErr != nil {
			log.Printf("Warning: failed to fetch volumes for config %d while re-rendering Config Editor: %v", configID, volErr)
			volumes = nil
		}

		data := pages.ConfigEditorData{
			GameID:        gameID,
			ConfigID:      configID,
			Name:          name,
			Image:         image,
			ArgsTemplate:  argsTemplate,
			EnvVars:       sortedConfigEditorEnvVars(envTemplate),
			Volumes:       toConfigEditorVolumes(volumes),
			Errors:        errs,
			Dirty:         true,
			ActiveTab:     "basics",
			BackupLinkURL: fmt.Sprintf("/games/%d/configs/%d", gameID, configID),
		}

		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		if err := pages.ConfigEditor(data).Render(ctx, w); err != nil {
			log.Printf("Error re-rendering Config Editor fragment for config %d: %v", configID, err)
		}
		return
	}

	req := buildConfigEditorUpdateRequest(configID, name, image, argsTemplate, envTemplate)
	if _, err := app.grpc.UpdateGameConfig(ctx, req); err != nil {
		log.Printf("Error saving config %d via Config Editor: %v", configID, err)
		http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
		return
	}

	// Atomicity (FR13): Basics and Environment reach here as one request
	// (req above), so both land or neither does -- there is no separate
	// second write. The whole page reload (rather than an in-blade swap)
	// mirrors the existing handleGameConfigEdit/handleGameConfigUpdateEnv
	// HX-Redirect convention (handlers_games.go) and picks up the updated
	// name/image in the Configurations section without a bespoke partial
	// re-render.
	w.Header().Set("HX-Redirect", fmt.Sprintf("/games?expand=%d", gameID))
	w.WriteHeader(http.StatusOK)
}

// buildConfigEditorUpdateRequest is FR13's load-bearing piece: update_paths
// is always explicit -- ["name", "image", "args_template", "env_template"]
// -- never empty.
//
// The obvious-sounding reason ("preserve entrypoint/command") is wrong: on
// the API handler's empty-paths branch, `if req.Entrypoint != nil` and
// `if req.Command != nil` already preserve those omitted fields, since this
// request never sets them. The real risk is args_template: that same
// empty-paths branch guards it with `if req.ArgsTemplate != ""`, so an
// emptied args template would silently fail to clear. Naming update_paths
// explicitly instead assigns args_template unconditionally on the
// explicit-paths branch, so clearing it actually takes -- see
// manmanv2/api/handlers/gameconfig.go's UpdateGameConfig for the two
// branches this sidesteps.
func buildConfigEditorUpdateRequest(configID int64, name, image, argsTemplate string, envTemplate map[string]string) *manmanpb.UpdateGameConfigRequest {
	return &manmanpb.UpdateGameConfigRequest{
		ConfigId:     configID,
		Name:         name,
		Image:        image,
		ArgsTemplate: argsTemplate,
		EnvTemplate:  envTemplate,
		UpdatePaths:  []string{"name", "image", "args_template", "env_template"},
	}
}

// sortedConfigEditorEnvVars renders env_template as deterministically
// ordered rows (sorted by key) -- Go map iteration order is randomized, so
// rendering directly from the map would make both the UI and any test
// asserting on rendered order flaky.
func sortedConfigEditorEnvVars(env map[string]string) []pages.ConfigEditorEnvVar {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]pages.ConfigEditorEnvVar, 0, len(keys))
	for _, k := range keys {
		rows = append(rows, pages.ConfigEditorEnvVar{Key: k, Value: env[k]})
	}
	return rows
}

// parseConfigEditorEnvRows zips the Environment tab's parallel env_key/
// env_value form fields (see config_editor.templ's data-env-rows markup)
// back into a map. A row with a blank (whitespace-only) key is dropped
// silently -- it is either an unused "+ Add Variable" row the operator
// never filled in, or a removed row's leftover pair should the remove
// button somehow not fire -- never surfaced as a validation error, since
// an env var key is not one of the two required Basics fields (NFR5: this
// only ever touches env_template, nothing else). A duplicate key keeps its
// last occurrence.
func parseConfigEditorEnvRows(keys, values []string) map[string]string {
	env := make(map[string]string, len(keys))
	for i, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		v := ""
		if i < len(values) {
			v = values[i]
		}
		env[k] = v
	}
	return env
}

// toConfigEditorVolumes converts GameConfigVolume protos to the Volumes
// tab's read-only view (WD4): all six fields, nothing else, no derived
// aggregate.
func toConfigEditorVolumes(volumes []*manmanpb.GameConfigVolume) []pages.ConfigEditorVolume {
	if len(volumes) == 0 {
		return nil
	}
	rows := make([]pages.ConfigEditorVolume, 0, len(volumes))
	for _, v := range volumes {
		rows = append(rows, pages.ConfigEditorVolume{
			Name:          v.Name,
			Description:   v.Description,
			ContainerPath: v.ContainerPath,
			HostSubpath:   v.HostSubpath,
			ReadOnly:      v.ReadOnly,
			VolumeType:    v.VolumeType,
		})
	}
	return rows
}
