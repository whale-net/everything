package main

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// FR12 (task #2095): per-server allowed host-port range configuration.
// All writes go through the public API (UpdateServerAllowedPortRanges,
// replace-all semantics) per NFR3; guidance/shape checks live here only
// (Decision 7: no save-time API validation beyond basics).
//
// Routed since task #2372 (M6 navigation/disposition) via
// "/infrastructure/{id}/ports/..." (handlers_infrastructure.go's
// handleInfrastructureAction dispatcher) rather than the retired
// "/servers/{id}/ports/..." -- these handler names and signatures are
// unchanged from before that task; only their registration and the
// renderer/redirect target they call changed (pages/server_detail.templ
// retired, folded into pages/infrastructure.templ's per-host Manage
// panel).

// parsePortRangeInput validates the start/end/protocol form trio.
// Returns (range, "") on success or (nil, message) on invalid input.
func parsePortRangeInput(startStr, endStr, protocol string) (*manmanpb.PortRange, string) {
	start, err1 := strconv.ParseInt(strings.TrimSpace(startStr), 10, 32)
	end, err2 := strconv.ParseInt(strings.TrimSpace(endStr), 10, 32)
	if err1 != nil || err2 != nil {
		return nil, "Start and end ports must be numbers."
	}
	if start < 1 || start > 65535 || end < 1 || end > 65535 {
		return nil, "Ports must be between 1 and 65535."
	}
	if end < start {
		return nil, fmt.Sprintf("End port %d must be greater than or equal to start port %d.", end, start)
	}
	proto := strings.ToUpper(strings.TrimSpace(protocol))
	if proto != "TCP" && proto != "UDP" {
		return nil, "Protocol must be TCP or UDP."
	}
	return &manmanpb.PortRange{Start: int32(start), End: int32(end), Protocol: proto}, ""
}

// computeUpdatedRanges applies an add/edit/remove against the server's
// current range set client-side, then the whole set is sent to the
// replace-all API. Matching is on the (start, end, protocol) identity.
func computeUpdatedRanges(current []*manmanpb.PortRange, orig *manmanpb.PortRange, next *manmanpb.PortRange) ([]*manmanpb.PortRange, string) {
	sameIdentity := func(a, b *manmanpb.PortRange) bool {
		return a.Start == b.Start && a.End == b.End && strings.EqualFold(a.Protocol, b.Protocol)
	}
	updated := make([]*manmanpb.PortRange, 0, len(current)+1)
	if orig == nil {
		// Add: reject an identity that already exists, then append.
		for _, pr := range current {
			if next != nil && sameIdentity(pr, next) {
				return nil, fmt.Sprintf("Range %d-%d/%s already exists.", next.Start, next.End, next.Protocol)
			}
			updated = append(updated, pr)
		}
		if next != nil {
			updated = append(updated, next)
		}
		return updated, ""
	}
	// Edit (next != nil) swaps in place; remove (next == nil) drops the row.
	for _, pr := range current {
		if sameIdentity(pr, orig) {
			if next != nil {
				updated = append(updated, next)
			}
			continue
		}
		if next != nil && sameIdentity(pr, next) {
			return nil, fmt.Sprintf("Range %d-%d/%s already exists.", next.Start, next.End, next.Protocol)
		}
		updated = append(updated, pr)
	}
	return updated, ""
}

// handleServerPortRangeSet handles POST /servers/{id}/ports/set: add a new
// range, or (editing=true) replace the range identified by orig_* with the
// submitted one.
func (app *App) handleServerPortRangeSet(w http.ResponseWriter, r *http.Request, serverIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	app.handleServerPortRangeChange(w, r, serverIDStr, false)
}

// handleServerPortRangeRemove handles POST /servers/{id}/ports/remove.
func (app *App) handleServerPortRangeRemove(w http.ResponseWriter, r *http.Request, serverIDStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	app.handleServerPortRangeChange(w, r, serverIDStr, true)
}

// handleServerPortRangeEdit handles GET /infrastructure/{id}/ports/edit by
// re-rendering the Infrastructure list with the matching host's Manage
// panel open and its allowed-port-ranges row in edit mode.
func (app *App) handleServerPortRangeEdit(w http.ResponseWriter, r *http.Request, serverIDStr string) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}
	orig, notice := parsePortRangeInput(r.URL.Query().Get("start"), r.URL.Query().Get("end"), r.URL.Query().Get("protocol"))
	if orig == nil {
		notice = "Invalid range to edit."
	}
	app.renderInfrastructure(w, r, serverID, notice, orig)
}

// handleServerPortRangeChange performs the shared add/edit/remove flow:
// fetch current ranges via the public API, compute the updated set, then
// replace-all. On failure it re-renders the detail page with a notice.
func (app *App) handleServerPortRangeChange(w http.ResponseWriter, r *http.Request, serverIDStr string, removing bool) {
	serverID, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}
	ctx := r.Context()

	var orig, next *manmanpb.PortRange
	if removing {
		orig, _ = parsePortRangeInput(r.FormValue("start"), r.FormValue("end"), r.FormValue("protocol"))
		if orig == nil {
			http.Error(w, "Invalid range to remove.", http.StatusBadRequest)
			return
		}
	} else if r.FormValue("editing") == "true" {
		orig, _ = parsePortRangeInput(r.FormValue("orig_start"), r.FormValue("orig_end"), r.FormValue("orig_protocol"))
		next, _ = parsePortRangeInput(r.FormValue("start"), r.FormValue("end"), r.FormValue("protocol"))
		if orig == nil || next == nil {
			http.Error(w, "Invalid range submitted.", http.StatusBadRequest)
			return
		}
	} else {
		next, _ = parsePortRangeInput(r.FormValue("start"), r.FormValue("end"), r.FormValue("protocol"))
		if next == nil {
			http.Error(w, "Invalid range submitted.", http.StatusBadRequest)
			return
		}
	}

	getResp, err := app.grpc.GetAPI().GetServer(ctx, &manmanpb.GetServerRequest{ServerId: serverID})
	if err != nil || getResp.GetServer() == nil {
		log.Printf("port ranges: failed to fetch server %d: %v", serverID, err)
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	updated, notice := computeUpdatedRanges(getResp.Server.AllowedPortRanges, orig, next)
	if notice != "" {
		http.Error(w, notice, http.StatusBadRequest)
		return
	}

	_, err = app.grpc.GetAPI().UpdateServerAllowedPortRanges(ctx, &manmanpb.UpdateServerAllowedPortRangesRequest{
		ServerId: serverID,
		Ranges:   updated,
	})
	if err != nil {
		log.Printf("port ranges: update failed for server %d: %v", serverID, err)
		http.Error(w, fmt.Sprintf("Failed to update allowed port ranges: %v", err), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, infrastructureManageRedirectTarget(serverID), http.StatusSeeOther)
}
