package main

import (
	"log"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleInfrastructure serves GET /infrastructure (FR1/FR2, C29): the
// redesigned fleet host list, additive alongside /servers (see #2369's
// "Design note" -- the nav swap and /servers redirect are the dependent
// navigation/disposition task, not this one). Renders the FR2 floor (name
// + drain-state badge) plus health indicators bounded by NFR5: Server's
// own status/last_seen/host_public_address fields need no extra fetch,
// and ListAllocatedPorts is fetched per host below (an existing RPC, not
// a new backend data path).
func (app *App) handleInfrastructure(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		http.Error(w, "Failed to fetch servers", http.StatusInternalServerError)
		return
	}

	hosts := make([]*pages.InfrastructureHost, 0, len(servers))
	for _, server := range servers {
		host := &pages.InfrastructureHost{Server: server}
		portsResp, err := app.grpc.GetAPI().ListAllocatedPorts(ctx, &manmanpb.ListAllocatedPortsRequest{ServerId: server.ServerId})
		if err != nil {
			// Allocated ports are a health indicator, not the FR2 floor --
			// degrade this one host's row to "not reported" rather than
			// failing the whole list.
			log.Printf("Infrastructure: failed to list allocated ports for server %d: %v", server.ServerId, err)
		} else {
			host.AllocatedPorts = portsResp.GetPorts()
		}
		hosts = append(hosts, host)
	}

	breadcrumbs := []components.Breadcrumb{
		{Label: "Infrastructure", URL: "/infrastructure"},
	}

	layoutData, err := app.buildTemplLayoutData(r, "Infrastructure", "Infrastructure", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := RenderTempl(w, r, "Infrastructure", pages.Infrastructure(layoutData, hosts)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleInfrastructureAction serves the "/infrastructure/{id}/{action}"
// routes (FR3 DrainServer, FR4 UndrainServer): one action per host, no
// reason/justification field, no confirmation form beyond the page's
// hx-confirm affordance. Both underlying RPCs are idempotent (#2366) --
// draining an already-draining host or undraining an already-schedulable
// one is a successful no-op, not a rejection -- so the only error branch
// here is a genuine transport/lookup failure, never a user-facing
// "rejected action" (that would be normal control flow, logged at Info,
// not Warning/Error; there is none to log today since nothing here
// rejects).
func (app *App) handleInfrastructureAction(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) != 3 || pathParts[0] != "infrastructure" {
		http.NotFound(w, r)
		return
	}

	serverID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil || serverID <= 0 {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	switch pathParts[2] {
	case "drain":
		server, err := app.grpc.DrainServer(ctx, serverID)
		if err != nil {
			log.Printf("Error draining server %d: %v", serverID, err)
			http.Error(w, "Failed to drain server", http.StatusInternalServerError)
			return
		}
		slog.Info("host drain requested from infrastructure page", "server_id", serverID, "drain_state", server.GetDrainState())
	case "undrain":
		server, err := app.grpc.UndrainServer(ctx, serverID)
		if err != nil {
			log.Printf("Error undraining server %d: %v", serverID, err)
			http.Error(w, "Failed to undrain server", http.StatusInternalServerError)
			return
		}
		slog.Info("host undrain requested from infrastructure page", "server_id", serverID, "drain_state", server.GetDrainState())
	default:
		http.NotFound(w, r)
		return
	}

	http.Redirect(w, r, "/infrastructure", http.StatusSeeOther)
}
