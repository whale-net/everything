package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// Deployment Settings blade -- environment half (root plan #2266, task
// #2274: FR11, NFR5, WD3, WD9). This file assembles the blade's
// environment section (pages.DeploymentSettingsData) and answers its one
// UI-only route -- everything it writes through still goes via
// handlers_sgc_env.go's shipped per-key handleSGCEnvSet/handleSGCEnvRemove
// (NFR3: no wire-contract change, NFR4: no new API surface).

// buildDeploymentSettingsData assembles the Deployment Settings blade's
// environment section: buildSGCEnvOverridesData's layering (reused, not
// recomputed) plus FR2's "<config> on <server>" display name and the same
// pendingEnvOverrideHint clearing rule the standalone SGC page already
// shows (task #2096). Purely a read-side assembly -- no new store, no new
// write path (NFR5).
func (app *App) buildDeploymentSettingsData(ctx context.Context, sgc *manmanpb.ServerGameConfig, gc *manmanpb.GameConfig) (pages.DeploymentSettingsData, error) {
	envData, envPatch, err := app.buildSGCEnvOverridesData(ctx, sgc, gc)
	if err != nil {
		return pages.DeploymentSettingsData{}, err
	}

	sessions, err := app.grpc.ListSessionsWithFilters(ctx, &manmanpb.ListSessionsRequest{
		ServerGameConfigId: sgc.GetServerGameConfigId(),
		PageSize:           50,
	})
	if err != nil {
		log.Printf("Warning: failed to list sessions for sgc %d: %v", sgc.GetServerGameConfigId(), err)
		sessions = nil
	}
	envData.PendingEditHint = pendingEnvOverrideHint(envPatch, sessions)

	var serverName string
	if sgc.GetServerId() != 0 {
		if resp, err := app.grpc.GetAPI().GetServer(ctx, &manmanpb.GetServerRequest{ServerId: sgc.GetServerId()}); err == nil {
			serverName = resp.GetServer().GetName()
		}
	}
	if serverName == "" {
		serverName = fmt.Sprintf("server %d", sgc.GetServerId())
	}
	configName := gc.GetName()
	if configName == "" {
		configName = fmt.Sprintf("config %d", sgc.GetGameConfigId())
	}

	return pages.DeploymentSettingsData{
		SGCID: sgc.GetServerGameConfigId(),
		// "<config> on <server>" (FR2): same naming convention as
		// buildGameDeploymentRow's DisplayName, which is what
		// components.BladeData.Subtitle already renders above this
		// section -- so the blade header and this fragment never
		// disagree, and neither ever names the deployment by a raw SGC
		// identifier.
		DisplayName:  fmt.Sprintf("%s on %s", configName, serverName),
		EnvOverrides: envData,
	}, nil
}

// renderDeploymentSettingsEnvFragment renders the env section fragment
// (both the initial lazy load and every post-commit refresh render this
// same templ, so the two can never structurally drift).
func (app *App) renderDeploymentSettingsEnvFragment(w http.ResponseWriter, r *http.Request, sgc *manmanpb.ServerGameConfig, gc *manmanpb.GameConfig) {
	data, err := app.buildDeploymentSettingsData(r.Context(), sgc, gc)
	if err != nil {
		log.Printf("Error building deployment settings fragment for sgc %d: %v", sgc.GetServerGameConfigId(), err)
		http.Error(w, "Failed to load deployment settings", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	if err := pages.DeploymentSettingsEnvSection(data).Render(r.Context(), w); err != nil {
		log.Printf("Error rendering deployment settings env fragment: %v", err)
	}
}

// handleDeploymentSettingsRoutes dispatches /deployment-settings/* routes (task
// #2274). A deliberately separate top-level prefix from handleSGCRoutes's
// /sgc/*: this URL is embedded directly in the Games page's initial
// render (DeploymentSettingsEnvPlaceholder's hx-get), and FR2 forbids
// "SGC" appearing in any display text that page renders -- including a
// URL a user could read in devtools. The path's {id} segment is still
// the deployment's server_game_config id; only the word "sgc" itself is
// avoided.
func (app *App) handleDeploymentSettingsRoutes(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /deployment-settings/{id}/env
	if len(pathParts) >= 3 && pathParts[2] == "env" {
		app.handleDeploymentSettingsEnv(w, r, pathParts[1])
		return
	}
	http.NotFound(w, r)
}

// handleDeploymentSettingsEnv answers the Deployment Settings blade's lazy
// env-section fetch (FR11): GET /deployment-settings/{id}/env, fired once by
// games.templ's DeploymentSettingsEnvPlaceholder (hx-trigger="load")
// after the blade shell itself was cloned client-side with no request
// (games.templ's data-open-blade-template click handler). A UI route
// only (NFR4) -- no new gRPC/API surface, no wire-contract change (NFR3):
// it only assembles already-public reads via buildDeploymentSettingsData.
func (app *App) handleDeploymentSettingsEnv(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64)
	if err != nil || sgcID <= 0 {
		http.Error(w, "Invalid SGC ID", http.StatusBadRequest)
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
		log.Printf("Error fetching game config %d for deployment settings: %v", sgc.GetGameConfigId(), err)
		http.Error(w, "Failed to fetch game config", http.StatusInternalServerError)
		return
	}

	app.renderDeploymentSettingsEnvFragment(w, r, sgc, gc)
}
