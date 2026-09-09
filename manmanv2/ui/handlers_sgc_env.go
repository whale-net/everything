package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	manman "github.com/whale-net/everything/manmanv2/models"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// Deployment Environment overrides (root plan #2080, task #2090):
// layered env view + deployment-level override CRUD (FR1/FR2/FR4).
//
// Overrides persist as an ordinary ConfigurationPatch at
// patch_level = server_game_config on the game's env_vars strategy
// (properties format, KEY=VALUE lines) -- written only through the public
// API (NFR3). Removing the last override deletes the patch, which reverts
// the effective value to the GameConfig env template at next session
// start (FR4).

// sgcEnvOverridePatch returns the deployment-level env override patch for
// this SGC, or nil when none exists yet. The game's env_vars strategy
// must exist for overrides to be possible (it is created lazily by
// handleSGCEnvSet on the first override, not here).
func (app *App) sgcEnvOverridePatch(ctx context.Context, gameID, sgcID int64) (*manmanpb.ConfigurationPatch, error) {
	strategiesResp, err := app.grpc.GetAPI().ListConfigurationStrategies(ctx, &manmanpb.ListConfigurationStrategiesRequest{
		GameId: gameID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list configuration strategies: %w", err)
	}

	var envStrategy *manmanpb.ConfigurationStrategy
	for _, s := range strategiesResp.GetStrategies() {
		if s.GetStrategyType() == manman.StrategyTypeEnvVars {
			envStrategy = s
			break
		}
	}
	if envStrategy == nil {
		return nil, nil
	}

	level := manman.PatchLevelServerGameConfig
	patchesResp, err := app.grpc.GetAPI().ListConfigurationPatches(ctx, &manmanpb.ListConfigurationPatchesRequest{
		StrategyId: &envStrategy.StrategyId,
		PatchLevel: &level,
		EntityId:   &sgcID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list env override patches: %w", err)
	}

	var found *manmanpb.ConfigurationPatch
	for _, p := range patchesResp.GetPatches() {
		if found == nil {
			found = p
		} else {
			log.Printf("Warning: multiple env override patches on sgc %d (patch %d ignored)", sgcID, p.PatchId)
		}
	}
	return found, nil
}

// buildSGCEnvOverridesData assembles the layered view (FR1): one row per
// known variable with its template layer, deployment override, and the
// effective value the next session start will use. hasTemplateKeys is
// false when the GameConfig env template defines nothing, so the section
// can render its empty-state nudge. envPatch (possibly nil) is returned
// alongside so mutation handlers don't re-fetch it.
func (app *App) buildSGCEnvOverridesData(ctx context.Context, sgc *manmanpb.ServerGameConfig, gc *manmanpb.GameConfig) (data pages.SGCEnvOverridesData, envPatch *manmanpb.ConfigurationPatch, err error) {
	data.SGC = sgc

	var template map[string]string
	if gc != nil {
		template = gc.GetEnvTemplate()
	}
	data.HasTemplateKeys = len(template) > 0

	overrides := map[string]string{}
	if gc != nil {
		envPatch, err = app.sgcEnvOverridePatch(ctx, gc.GetGameId(), sgc.GetServerGameConfigId())
		if err != nil {
			return data, nil, err
		}
		if envPatch != nil {
			overrides = parsePropertiesContent(envPatch.GetPatchContent())
		}
	}

	keys := make([]string, 0, len(template)+len(overrides))
	seen := map[string]bool{}
	for k := range template {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	for k := range overrides {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	sort.Strings(keys)

	for _, k := range keys {
		layer := pages.EnvVarLayer{
			Key:           k,
			TemplateValue: template[k],
		}
		if override, ok := overrides[k]; ok {
			layer.OverrideValue = &override
			layer.EffectiveValue = override
		} else {
			layer.EffectiveValue = template[k]
		}
		data.Layers = append(data.Layers, layer)
	}
	return data, envPatch, nil
}

// pendingEnvOverrideHint implements FR3's exact clearing rule (task
// #2096): the pending hint is visible only while the deployment has an
// active session AND the latest saved deployment-level env override edit
// (patch updated_at) is newer than that session's start -- i.e. the
// session's start command was built before the edit was saved, so it ran
// values predating the edit. A start whose command build happened after
// the save ran the newly saved values and clears the hint (session start
// time is set when the start command is built, before the rendered env is
// published). A start that began before the save -- however long it
// keeps running -- therefore leaves the hint visible, and no active
// session or no patch means there is nothing pending.
// UI state only: derived entirely from existing patch timestamps +
// session start times; no new write path (NFR3).
func pendingEnvOverrideHint(patch *manmanpb.ConfigurationPatch, sessions []*manmanpb.Session) bool {
	if patch == nil || patch.GetUpdatedAt() <= 0 {
		return false
	}
	// The deployment's current start attempt: pending/starting/running
	// sessions are starts whose command build already happened (start time
	// is set at build time). Stopped/crashed/lost sessions are not starts
	// that ran anything. Newest start wins if several are somehow active.
	var activeStart int64
	active := false
	for _, s := range sessions {
		switch s.GetStatus() {
		case manman.SessionStatusPending, manman.SessionStatusStarting, manman.SessionStatusRunning:
			if !active || s.GetStartedAt() > activeStart {
				activeStart = s.GetStartedAt()
				active = true
			}
		}
	}
	if !active {
		return false
	}
	// At-or-newer, not strictly-newer: timestamps are Unix seconds, and a
	// save landing in the same second as a start cannot be proven to have
	// been built before that start's command build. Treating equality as
	// "already run" could clear the hint prematurely, which FR3 forbids.
	return patch.GetUpdatedAt() >= activeStart
}

// parsePropertiesContent parses properties-format patch content
// (KEY=VALUE lines, blank lines and # comments skipped) into a map.
// Invalid lines are tolerated on read: the layered view still renders the
// well-formed variables, and session-start rendering (task #2089) fails
// legibly if the content is actually broken.
func parsePropertiesContent(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		out[key] = strings.TrimSpace(value)
	}
	return out
}

// serializePropertiesContent renders an override map as properties-format
// patch content with keys in sorted order (deterministic patch content).
func serializePropertiesContent(overrides map[string]string) string {
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+overrides[k])
	}
	return strings.Join(lines, "\n")
}

// validateEnvOverrideKey rejects keys that cannot be represented as
// properties content or would break the KEY=VALUE format.
func validateEnvOverrideKey(key string) error {
	if key == "" {
		return fmt.Errorf("variable name is required")
	}
	if strings.ContainsAny(key, "=\n\r#") || strings.Contains(key, " ") {
		return fmt.Errorf("variable name %q must not contain '=', whitespace, or newlines", key)
	}
	return nil
}

// validateEnvOverrideValue rejects values that would break the
// KEY=VALUE line format.
func validateEnvOverrideValue(value string) error {
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("override value must not contain newlines")
	}
	return nil
}

// handleSGCEnvSet adds or updates one deployment-level env override
// (FR2). POST /sgc/{id}/env/set with key+value form fields.
func (app *App) handleSGCEnvSet(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil || sgcID <= 0 {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	value := r.FormValue("value")
	if err := validateEnvOverrideKey(key); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateEnvOverrideValue(value); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	sgc, err := app.fetchSGC(ctx, sgcID)
	if err != nil {
		http.Error(w, "SGC not found", http.StatusNotFound)
		return
	}
	gc, err := app.fetchGameConfig(ctx, sgc.GetGameConfigId())
	if err != nil {
		log.Printf("Error fetching game config %d for env set: %v", sgc.GetGameConfigId(), err)
		http.Error(w, "Failed to fetch game config", http.StatusInternalServerError)
		return
	}

	envPatch, err := app.sgcEnvOverridePatch(ctx, gc.GetGameId(), sgcID)
	if err != nil {
		log.Printf("Error fetching env override patch for sgc %d: %v", sgcID, err)
		http.Error(w, "Failed to fetch env overrides", http.StatusInternalServerError)
		return
	}

	overrides := map[string]string{}
	if envPatch != nil {
		overrides = parsePropertiesContent(envPatch.GetPatchContent())
	}
	overrides[key] = value
	content := serializePropertiesContent(overrides)

	if envPatch != nil {
		_, err = app.grpc.GetAPI().UpdateConfigurationPatch(ctx, &manmanpb.UpdateConfigurationPatchRequest{
			PatchId:      envPatch.PatchId,
			PatchContent: content,
			PatchFormat:  manman.PatchFormatProperties,
			PatchOrder:   envPatch.PatchOrder,
		})
		if err != nil {
			log.Printf("Error updating env override patch %d: %v", envPatch.PatchId, err)
			http.Error(w, "Failed to save override", http.StatusInternalServerError)
			return
		}
	} else {
		strategy, err := app.ensureEnvVarsStrategy(ctx, gc.GetGameId())
		if err != nil {
			log.Printf("Error ensuring env_vars strategy for game %d: %v", gc.GetGameId(), err)
			http.Error(w, "Failed to prepare env overrides", http.StatusInternalServerError)
			return
		}
		_, err = app.grpc.GetAPI().CreateConfigurationPatch(ctx, &manmanpb.CreateConfigurationPatchRequest{
			StrategyId:   strategy.StrategyId,
			PatchLevel:   manman.PatchLevelServerGameConfig,
			EntityId:     sgcID,
			PatchContent: content,
			PatchFormat:  manman.PatchFormatProperties,
			PatchOrder:   0,
		})
		if err != nil {
			log.Printf("Error creating env override patch for sgc %d: %v", sgcID, err)
			http.Error(w, "Failed to save override", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, fmt.Sprintf("/sgc/%d", sgcID), http.StatusSeeOther)
}

// handleSGCEnvRemove removes one deployment-level env override (FR4).
// Removing the last override deletes the patch so the effective value
// reverts to the GameConfig template value at next session start.
// POST /sgc/{id}/env/remove with a key form field.
func (app *App) handleSGCEnvRemove(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil || sgcID <= 0 {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))

	ctx := r.Context()

	sgc, err := app.fetchSGC(ctx, sgcID)
	if err != nil {
		http.Error(w, "SGC not found", http.StatusNotFound)
		return
	}
	gc, err := app.fetchGameConfig(ctx, sgc.GetGameConfigId())
	if err != nil {
		log.Printf("Error fetching game config %d for env remove: %v", sgc.GetGameConfigId(), err)
		http.Error(w, "Failed to fetch game config", http.StatusInternalServerError)
		return
	}

	envPatch, err := app.sgcEnvOverridePatch(ctx, gc.GetGameId(), sgcID)
	if err != nil {
		log.Printf("Error fetching env override patch for sgc %d: %v", sgcID, err)
		http.Error(w, "Failed to fetch env overrides", http.StatusInternalServerError)
		return
	}
	if envPatch == nil {
		// Nothing to remove; the view is already fully inherited.
		http.Redirect(w, r, fmt.Sprintf("/sgc/%d", sgcID), http.StatusSeeOther)
		return
	}

	overrides := parsePropertiesContent(envPatch.GetPatchContent())
	delete(overrides, key)

	if len(overrides) == 0 {
		_, err = app.grpc.GetAPI().DeleteConfigurationPatch(ctx, &manmanpb.DeleteConfigurationPatchRequest{
			PatchId: envPatch.PatchId,
		})
		if err != nil {
			log.Printf("Error deleting env override patch %d: %v", envPatch.PatchId, err)
			http.Error(w, "Failed to remove override", http.StatusInternalServerError)
			return
		}
	} else {
		_, err = app.grpc.GetAPI().UpdateConfigurationPatch(ctx, &manmanpb.UpdateConfigurationPatchRequest{
			PatchId:      envPatch.PatchId,
			PatchContent: serializePropertiesContent(overrides),
			PatchFormat:  manman.PatchFormatProperties,
			PatchOrder:   envPatch.PatchOrder,
		})
		if err != nil {
			log.Printf("Error updating env override patch %d: %v", envPatch.PatchId, err)
			http.Error(w, "Failed to remove override", http.StatusInternalServerError)
			return
		}
	}

	http.Redirect(w, r, fmt.Sprintf("/sgc/%d", sgcID), http.StatusSeeOther)
}

// handleSGCEnvEdit renders the prefilled edit form fragment for one
// override (targeted by the page's hx-get into #sgc-env-editor).
// GET /sgc/{id}/env/edit?key=NAME
func (app *App) handleSGCEnvEdit(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil || sgcID <= 0 {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		http.Error(w, "Missing key", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	sgc, err := app.fetchSGC(ctx, sgcID)
	if err != nil {
		http.Error(w, "SGC not found", http.StatusNotFound)
		return
	}

	// Current override value (empty when editing a template-only var into
	// its first override).
	value := ""
	gc, err := app.fetchGameConfig(ctx, sgc.GetGameConfigId())
	if err == nil && gc != nil {
		envPatch, patchErr := app.sgcEnvOverridePatch(ctx, gc.GetGameId(), sgcID)
		if patchErr == nil && envPatch != nil {
			if v, ok := parsePropertiesContent(envPatch.GetPatchContent())[key]; ok {
				value = v
			}
		}
	}

	w.Header().Set("Content-Type", "text/html")
	if err := pages.SGCEnvOverrideEditForm(sgcID, key, value).Render(r.Context(), w); err != nil {
		log.Printf("Error rendering env edit form: %v", err)
	}
}

// ensureEnvVarsStrategy returns the game's env_vars strategy, creating it
// if the game has none (first override on a game without one).
func (app *App) ensureEnvVarsStrategy(ctx context.Context, gameID int64) (*manmanpb.ConfigurationStrategy, error) {
	strategiesResp, err := app.grpc.GetAPI().ListConfigurationStrategies(ctx, &manmanpb.ListConfigurationStrategiesRequest{
		GameId: gameID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list configuration strategies: %w", err)
	}
	for _, s := range strategiesResp.GetStrategies() {
		if s.GetStrategyType() == manman.StrategyTypeEnvVars {
			return s, nil
		}
	}

	createResp, err := app.grpc.GetAPI().CreateConfigurationStrategy(ctx, &manmanpb.CreateConfigurationStrategyRequest{
		GameId:       gameID,
		Name:         "Environment Variables",
		Description:  "Deployment-level environment variable overrides",
		StrategyType: manman.StrategyTypeEnvVars,
		ApplyOrder:   0,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create env_vars strategy: %w", err)
	}
	return createResp.GetStrategy(), nil
}

// fetchSGC fetches one ServerGameConfig by id.
func (app *App) fetchSGC(ctx context.Context, sgcID int64) (*manmanpb.ServerGameConfig, error) {
	resp, err := app.grpc.GetAPI().GetServerGameConfig(ctx, &manmanpb.GetServerGameConfigRequest{
		ServerGameConfigId: sgcID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetConfig(), nil
}

// fetchGameConfig fetches one GameConfig by id.
func (app *App) fetchGameConfig(ctx context.Context, configID int64) (*manmanpb.GameConfig, error) {
	resp, err := app.grpc.GetAPI().GetGameConfig(ctx, &manmanpb.GetGameConfigRequest{
		ConfigId: configID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetConfig(), nil
}
