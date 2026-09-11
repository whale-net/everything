package pages

import (
	"fmt"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// InfrastructureHost is the Infrastructure page's per-host view model
// (task #2369, manmanv2 M6, FR2). It exists because the health
// indicators the issue calls for -- allocated ports -- come from a
// separate RPC (ListAllocatedPorts) than the Server itself, and the page
// data needs somewhere to carry that per-host fan-out result. AllocatedPorts
// is nil (not an empty non-nil slice) when the fetch failed or a host has
// none; the FR2 floor (name + drain state) never depends on it.
//
// ManageOpen/PortsNotice/PortsEdit (task #2372, M6 navigation/disposition)
// back the per-host "Manage" panel folded into this page when
// pages/server_detail.templ retired -- see this file's serverStatusVariant
// doc comment and ServerPortRangesSection's doc comment for why that
// capability moved here rather than disappearing (NFR6).
type InfrastructureHost struct {
	Server         *manmanpb.Server
	AllocatedPorts []*manmanpb.AllocatedPort

	// ManageOpen is true when this host's "Manage" <details> panel (public
	// address edit + allowed port ranges) should render open on page load:
	// either the request carried "?manage=<this host's id>" (FR16's
	// /servers/<id> redirect target, preserving the identifier -- see
	// handlers_navigation_redirects.go) or a ports/address action just
	// round-tripped through this host and needs its panel to stay open to
	// show the result.
	ManageOpen bool
	// PortsNotice/PortsEdit mirror ServerPortRangesSection's own
	// parameters, populated only for the one host handleServerPortRangeEdit
	// (handlers_server_ports.go) is currently editing; every other host's
	// values are the zero value ("" / nil).
	PortsNotice string
	PortsEdit   *manmanpb.PortRange
}

// drainStateLabel renders Server.drain_state (#2360) as display text.
// The field is empty for hosts that existed before drain_state was
// introduced -- those are schedulable, so an empty value renders as such
// rather than blank.
func drainStateLabel(state string) string {
	if state == "" {
		return "schedulable"
	}
	return state
}

// drainStateVariant maps drain_state to the shared status-badge vocabulary
// (statusBadgeVariant/components.StatusBadgeVariant), not a new colour
// table: schedulable reads as "success" (fully in service), draining as
// "warning" (in-flight transition, FR2's transient state), drained as
// "danger" (out of service).
func drainStateVariant(state string) string {
	switch state {
	case "draining":
		return "warning"
	case "drained":
		return "danger"
	default:
		return "success"
	}
}

// canDrain reports whether the Drain action (FR3) should be offered for a
// host's current drain state. Only a schedulable host can be drained --
// draining and drained hosts already have an in-flight or completed drain,
// so Drain and Undrain are mutually exclusive per host state.
func canDrain(state string) bool {
	return state == "" || state == "schedulable"
}

// canUndrain reports whether the Undrain action (FR4) should be offered.
// Per the issue body, undrain is offered for a "drained/draining" host --
// draining is included so an Admin who changes their mind mid-drain is not
// stuck waiting for eviction to finish before they can back out.
func canUndrain(state string) bool {
	return state == "draining" || state == "drained"
}

// drainConfirmMessage is FR3's confirm-affordance copy (US2): the Admin
// must be told the one action both stops new placement AND stops every
// session currently running on the host, not just that it "drains" it.
func drainConfirmMessage(hostName string) string {
	return fmt.Sprintf("Drain %s? This stops new placement on this host and stops every session currently running on it.", hostName)
}

// undrainConfirmMessage is FR4's confirm-affordance copy: it must state
// plainly that undraining does not restart anything that was drain-stopped
// -- a Server Manager restarts manually if wanted.
func undrainConfirmMessage(hostName string) string {
	return fmt.Sprintf("Undrain %s? This does not restart anything that was stopped by draining -- restart deployments manually if wanted.", hostName)
}

// serverStatusVariant maps Server.status to the shared status-badge
// vocabulary. Moved here from the retired pages/servers.templ (task #2372,
// M6 navigation/disposition) -- this page is now its sole caller.
func serverStatusVariant(status string) string {
	switch status {
	case "online":
		return "success"
	case "offline":
		return "secondary"
	default:
		return "secondary"
	}
}

// allocatedPortsSummary renders a host's FR2 allocated-ports health
// indicator, bounded by NFR5 (ListAllocatedPorts already exists; this adds
// no new collection). Nil/empty renders as "not reported" per the issue's
// explicit floor for hosts with no such data.
func allocatedPortsSummary(ports []*manmanpb.AllocatedPort) string {
	if len(ports) == 0 {
		return "None allocated"
	}
	return fmt.Sprintf("%d port(s) allocated", len(ports))
}

// fleetStatusLabel renders a FleetGameStatus row's running/total deployment
// counts (#2371, manmanv2 M6, FR5) as "running/total" -- the running-count-
// over-total-count proxy this task uses in place of literal player counts
// (see the issue's "Scope note carried from the spec").
func fleetStatusLabel(runningCount, totalCount int32) string {
	return fmt.Sprintf("%d / %d", runningCount, totalCount)
}

// fleetStatusVariant maps a FleetGameStatus row onto the shared status-badge
// vocabulary (statusBadgeVariant): a game with no deployments anywhere in
// the fleet reads as "secondary" (neutral, not a problem), every deployment
// running reads as "success", none running (but at least one deployment
// exists) reads as "danger", and a partial running/total reads as
// "warning" -- a genuinely different state from either extreme.
func fleetStatusVariant(runningCount, totalCount int32) string {
	if totalCount == 0 {
		return "secondary"
	}
	if runningCount == totalCount {
		return "success"
	}
	if runningCount == 0 {
		return "danger"
	}
	return "warning"
}
