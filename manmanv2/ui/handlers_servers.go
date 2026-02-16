package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manman/protos"
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
	Title   string
	Active  string
	User    *htmxauth.UserInfo
	Server  *manmanpb.Server
	Configs []*manmanpb.ServerGameConfig
}

func (app *App) handleServers(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := context.Background()
	
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		http.Error(w, "Failed to fetch servers", http.StatusInternalServerError)
		return
	}
	
	data := ServersPageData{
		Title:   "Servers",
		Active:  "servers",
		User:    user,
		Servers: servers,
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "servers_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func (app *App) handleServerDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	
	// Extract server ID from URL path
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}
	
	serverIDStr := pathParts[1]
	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}
	
	ctx := context.Background()
	
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
	
	data := ServerDetailPageData{
		Title:   resp.Server.Name,
		Active:  "servers",
		User:    user,
		Server:  resp.Server,
		Configs: configsResp.Configs,
	}

	layoutData := LayoutData{
		Title:  data.Title,
		Active: data.Active,
		User:   data.User,
	}

	if err := renderPage(w, "server_detail_content", data, layoutData); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
