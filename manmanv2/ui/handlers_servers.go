package main

import (
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
	"github.com/whale-net/everything/manmanv2/protos"
)

// ServersPageData holds data for the servers list page
type ServersPageData struct {
	Title   string
	Active  string
	User    *htmxauth.UserInfo
	Servers []*manmanpb.Server
}

// ServerDetailPageData holds data for server detail page
type ServerDetailPageData struct {
	Title        string
	Active       string
	User         *htmxauth.UserInfo
	Server       *manmanpb.Server
	Configs      []*manmanpb.ServerGameConfig
	ConfigGameID map[int64]int64 // map[config_id]game_id
}

func (app *App) handleServers(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()
	
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		http.Error(w, "Failed to fetch servers", http.StatusInternalServerError)
		return
	}
	
	breadcrumbs := []components.Breadcrumb{
		{Label: "Servers", URL: "/servers"},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Servers", "Servers", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Servers", pages.Servers(layoutData, servers)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleServerDetail(w http.ResponseWriter, r *http.Request) {
	// Extract server ID from URL path
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	// /servers/{id}/update-address
	if len(pathParts) >= 3 && pathParts[2] == "update-address" {
		app.handleServerUpdateAddress(w, r, pathParts[1])
		return
	}

	user := htmxauth.GetUser(r.Context())

	serverIDStr := pathParts[1]
	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}
	
	ctx := r.Context()
	
	// Fetch server details
	resp, err := app.grpc.GetAPI().GetServer(ctx, &manmanpb.GetServerRequest{
		ServerId: serverID,
	})
	if err != nil {
		log.Printf("Error fetching server: %v", err)
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}
	
	// Fetch server game configs (deployments)
	configsResp, err := app.grpc.GetAPI().ListServerGameConfigs(ctx, &manmanpb.ListServerGameConfigsRequest{
		ServerId: serverID,
		PageSize: 100,
	})
	if err != nil {
		log.Printf("Error fetching server configs: %v", err)
		configsResp = &manmanpb.ListServerGameConfigsResponse{Configs: []*manmanpb.ServerGameConfig{}}
	}
	
	breadcrumbs := []components.Breadcrumb{
		{Label: "Servers", URL: "/servers"},
		{Label: resp.Server.Name, URL: ""},
	}

	layoutData, err := app.buildTemplLayoutData(r, resp.Server.Name, "Servers", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, resp.Server.Name, pages.ServerDetail(layoutData, resp.Server, configsResp.Configs)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleServerUpdateAddress handles POST /servers/{id}/update-address,
// updating (or clearing) a host's public connect address (#1528, FR4).
// The update_paths field mask is always sent -- including on an empty
// submitted value -- so clearing the address does not fall back to
// update-all semantics (see #1527's clear-via-field-mask contract).
func (app *App) handleServerUpdateAddress(w http.ResponseWriter, r *http.Request, serverIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	hostPublicAddress := r.FormValue("host_public_address")

	ctx := r.Context()
	_, err = app.grpc.GetAPI().UpdateServer(ctx, &manmanpb.UpdateServerRequest{
		ServerId:          serverID,
		HostPublicAddress: hostPublicAddress,
		UpdatePaths:       []string{"host_public_address"},
	})
	if err != nil {
		log.Printf("Error updating public address for server %d: %v", serverID, err)
		http.Error(w, "Failed to update public address", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/servers/"+serverIDStr, http.StatusSeeOther)
}
