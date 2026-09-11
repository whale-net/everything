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
// redesigned fleet host list. Task #2372 (M6 navigation/disposition)
// retired /servers into a redirect onto this page (FR16), so this is now
// the sole nav-facing surface for the fleet host list.
//
// Renders the FR2 floor (name + drain-state badge) plus health indicators
// bounded by NFR5: Server's own status/last_seen/host_public_address
// fields need no extra fetch, and ListAllocatedPorts is fetched per host
// below (an existing RPC, not a new backend data path). manageServerID,
// when nonzero, opens that one host's "Manage" panel (public address +
// allowed port ranges, folded in by #2372 -- see
// pages.InfrastructureHost's doc comment); portsNotice/portsEdit apply
// only to that same host, driving handleServerPortRangeEdit's
// (handlers_server_ports.go) in-place edit-row rendering.
func (app *App) handleInfrastructure(w http.ResponseWriter, r *http.Request) {
	manageServerID, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("manage")), 10, 64)
	app.renderInfrastructure(w, r, manageServerID, "", nil)
}

// renderInfrastructure is handleInfrastructure's shared renderer, also
// used by handleServerPortRangeEdit (handlers_server_ports.go) to re-render
// the list with one host's allowed-port-ranges card put into edit mode
// (portsNotice/portsEdit apply only to the host identified by
// manageServerID; every other host's InfrastructureHost carries the zero
// value for both).
func (app *App) renderInfrastructure(w http.ResponseWriter, r *http.Request, manageServerID int64, portsNotice string, portsEdit *manmanpb.PortRange) {
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
		if manageServerID != 0 && server.ServerId == manageServerID {
			host.ManageOpen = true
			host.PortsNotice = portsNotice
			host.PortsEdit = portsEdit
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

// handleInfrastructureAction serves the "/infrastructure/{id}/..." routes:
// FR3 DrainServer, FR4 UndrainServer (task #2369, one action per host, no
// reason/justification field, no confirmation form beyond the page's
// hx-confirm affordance -- both underlying RPCs are idempotent, #2366, so
// the only error branch is a genuine transport/lookup failure), plus task
// #2372's folded-in host management sub-routes: "update-address" and the
// "ports/{set,remove,edit}" trio (moved here from the retired
// "/servers/{id}/..." equivalents, handlers_server_ports.go, when
// pages/server_detail.templ retired -- see pages.InfrastructureHost's doc
// comment).
func (app *App) handleInfrastructureAction(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 3 || pathParts[0] != "infrastructure" {
		http.NotFound(w, r)
		return
	}

	serverID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil || serverID <= 0 {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	// "/infrastructure/{id}/ports/{set,remove,edit}" (task #2372).
	if pathParts[2] == "ports" {
		if len(pathParts) != 4 {
			http.NotFound(w, r)
			return
		}
		switch pathParts[3] {
		case "set":
			app.handleServerPortRangeSet(w, r, pathParts[1])
		case "remove":
			app.handleServerPortRangeRemove(w, r, pathParts[1])
		case "edit":
			app.handleServerPortRangeEdit(w, r, pathParts[1])
		default:
			http.NotFound(w, r)
		}
		return
	}

	if len(pathParts) != 3 {
		http.NotFound(w, r)
		return
	}

	// "/infrastructure/{id}/update-address" (task #2372).
	if pathParts[2] == "update-address" {
		app.handleInfrastructureUpdateAddress(w, r, pathParts[1])
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

// handleInfrastructureUpdateAddress handles POST
// /infrastructure/{id}/update-address, updating (or clearing) a host's
// public connect address (#1528, FR4). Moved here from the retired
// handleServerUpdateAddress (handlers_servers.go, task #2372) when
// pages/server_detail.templ retired -- see pages.InfrastructureHost's doc
// comment. The update_paths field mask is always sent -- including on an
// empty submitted value -- so clearing the address does not fall back to
// update-all semantics (see #1527's clear-via-field-mask contract).
func (app *App) handleInfrastructureUpdateAddress(w http.ResponseWriter, r *http.Request, serverIDStr string) {
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

	http.Redirect(w, r, infrastructureManageRedirectTarget(serverID), http.StatusSeeOther)
}

// infrastructureManageRedirectTarget is the shared post-action redirect
// target for every host-management sub-route this file and
// handlers_server_ports.go own: back to the Infrastructure list with this
// host's Manage panel reopened (query param) and scrolled to (fragment).
func infrastructureManageRedirectTarget(serverID int64) string {
	return "/infrastructure?manage=" + strconv.FormatInt(serverID, 10) + "#host-manage-" + strconv.FormatInt(serverID, 10)
}
