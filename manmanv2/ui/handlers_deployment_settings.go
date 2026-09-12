package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// Deployment Settings blade -- environment (root plan #2266, task #2274:
// FR11, NFR5, WD3, WD9) and ports (task #2275: FR12, the accumulating
// half of FR10, WD10). This file assembles both of the blade's sections
// (pages.DeploymentSettingsData for env, pages.DeploymentSettingsPortsData
// for ports) and answers their UI-only routes. Env writes still go via
// handlers_sgc_env.go's shipped per-key handleSGCEnvSet/handleSGCEnvRemove;
// ports writes go via the shipped UpdateServerGameConfig write path
// (handlers_sgc.go's handleSGCUpdatePorts uses the same call) -- NFR3: no
// wire-contract change, NFR4: no new API surface either section adds.

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
	// /deployment-settings/{id}/ports/save (FR12's one explicit Save --
	// checked before the bare "ports" branch below since both share
	// pathParts[2] == "ports").
	if len(pathParts) >= 4 && pathParts[2] == "ports" && pathParts[3] == "save" {
		app.handleDeploymentSettingsPortsSave(w, r, pathParts[1])
		return
	}
	// /deployment-settings/{id}/ports (lazy load, mirrors the env branch
	// above)
	if len(pathParts) >= 3 && pathParts[2] == "ports" {
		app.handleDeploymentSettingsPorts(w, r, pathParts[1])
		return
	}
	http.NotFound(w, r)
}

// buildDeploymentSettingsPortsData assembles the blade's ports section
// (FR12): the deployment's current bindings plus the FR13/FR14 guidance
// surface, reused unchanged from buildSGCPortContext
// (handlers_sgc_ports_guidance.go, task #2098) -- this renders that
// guidance, it does not re-derive it.
func (app *App) buildDeploymentSettingsPortsData(ctx context.Context, sgc *manmanpb.ServerGameConfig) pages.DeploymentSettingsPortsData {
	return pages.DeploymentSettingsPortsData{
		SGCID:        sgc.GetServerGameConfigId(),
		PortBindings: sgc.GetPortBindings(),
		PortContext:  buildSGCPortContext(ctx, app.grpc.GetAPI(), sgc.GetServerId(), sgc.GetServerGameConfigId()),
	}
}

// handleDeploymentSettingsPorts answers the blade's lazy ports-section
// fetch (FR12): GET /deployment-settings/{id}/ports, fired once by
// games.templ's DeploymentSettingsPortsPlaceholder (hx-trigger="load"),
// the same lazy-fetch shape handleDeploymentSettingsEnv already uses for
// the env section (NFR7: no per-deployment fetch at Games-page render
// time).
func (app *App) handleDeploymentSettingsPorts(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
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

	data := app.buildDeploymentSettingsPortsData(ctx, sgc)
	w.Header().Set("Content-Type", "text/html")
	if err := pages.DeploymentSettingsPortsSection(data).Render(ctx, w); err != nil {
		log.Printf("Error rendering deployment settings ports fragment: %v", err)
	}
}

// handleDeploymentSettingsPortsSave answers the blade's explicit ports
// Save (FR12): POST /deployment-settings/{id}/ports/save, the section's
// only write -- the whole accumulated binding array in one
// UpdateServerGameConfig call with update_paths=["port_bindings"], the
// same shipped write path handleSGCUpdatePorts (handlers_sgc.go) uses.
// NFR5's no-bulk rule is about the env section, not this one: one array
// field, one call is the intended shape for ports, not a bulk-write
// violation.
//
// On success the response is a clean fragment (Dirty: false, so
// data-blade-dirty goes back to "false" -- FR10 stops prompting on
// dismiss) plus an out-of-band swap of the Games page's connect-address
// display for this deployment (AC4). This handler adds no validation
// authority of its own (FR12): it saves whatever was submitted and lets
// the API's existing save-time checks be the backstop.
//
// On failure the response re-renders the section with the operator's
// just-submitted bindings preserved, still marked Dirty (so FR10 keeps
// prompting), plus an inline error -- entered bindings are never dropped.
func (app *App) handleDeploymentSettingsPortsSave(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
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

	portBindings, err := parsePortBindingsJSON(r.FormValue("port_bindings_json"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	sgc, err := app.fetchSGC(ctx, sgcID)
	if err != nil {
		http.Error(w, "SGC not found", http.StatusNotFound)
		return
	}

	_, updateErr := app.grpc.UpdateServerGameConfig(ctx, &manmanpb.UpdateServerGameConfigRequest{
		ServerGameConfigId: sgcID,
		PortBindings:       portBindings,
		UpdatePaths:        []string{"port_bindings"},
	})
	if updateErr != nil {
		log.Printf("Error updating port bindings for sgc %d via deployment settings: %v", sgcID, updateErr)
		data := pages.DeploymentSettingsPortsData{
			SGCID:        sgcID,
			PortBindings: portBindings,
			PortContext:  buildSGCPortContext(ctx, app.grpc.GetAPI(), sgc.GetServerId(), sgcID),
			Error:        "Failed to save port bindings. Your entered bindings are unchanged below.",
			Dirty:        true,
		}
		w.Header().Set("Content-Type", "text/html")
		if err := pages.DeploymentSettingsPortsSection(data).Render(ctx, w); err != nil {
			log.Printf("Error re-rendering deployment settings ports fragment: %v", err)
		}
		return
	}

	data := pages.DeploymentSettingsPortsData{
		SGCID:        sgcID,
		PortBindings: portBindings,
		PortContext:  buildSGCPortContext(ctx, app.grpc.GetAPI(), sgc.GetServerId(), sgcID),
	}
	w.Header().Set("Content-Type", "text/html")
	if err := pages.DeploymentSettingsPortsSection(data).Render(ctx, w); err != nil {
		log.Printf("Error rendering deployment settings ports fragment: %v", err)
	}

	// AC4: recompute the connect address from the newly saved bindings and
	// swap it into the Games page row out of band. A GetServer failure here
	// degrades to no swap (the section's own primary swap above already
	// landed) -- never an error response for a save that already
	// succeeded.
	serverResp, err := app.grpc.GetAPI().GetServer(ctx, &manmanpb.GetServerRequest{ServerId: sgc.GetServerId()})
	if err != nil {
		log.Printf("Warning: failed to fetch server %d for connect-address recompute: %v", sgc.GetServerId(), err)
		return
	}
	view := components.BuildConnectAddressView(serverResp.GetServer().GetHostPublicAddress(), portBindings)
	if err := pages.DeploymentConnectAddressOOB(sgcID, view).Render(ctx, w); err != nil {
		log.Printf("Error rendering connect-address OOB swap: %v", err)
	}
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
