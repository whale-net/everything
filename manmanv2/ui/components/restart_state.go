package components

import (
	"github.com/whale-net/everything/libs/go/htmxui"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// RestartBadge maps a PendingRestartState onto the FR12 deployment-row
// restart badge: distinguishing "in progress" from "stalled"/"failed" is
// the literal requirement (#1735), so each non-silent status gets its own
// label/variant/title rather than one generic "restart" indicator.
//
//   - nil (no restart record in the visibility window): no badge.
//   - "pending": in progress -- the Stop dispatched by RestartDeployment
//     hasn't reached a terminal status yet, so the deferred Start hasn't
//     been claimed. Rendered as an in-progress ("Restarting") badge; the
//     row's existing session-status badge (stopping/stopped) still tells
//     the rest of the story.
//   - "started": no badge. The pending record was claimed and the deferred
//     Start was dispatched -- the normal session-status badge
//     (starting/running) already communicates this, so a second badge here
//     would be redundant.
//   - "failed": terminal failure -- the deferred Start itself failed (or
//     the initial Stop dispatch failed). Rendered as an error badge with
//     the failure reason as the caller-attached title/tooltip.
//   - "expired": terminal stall -- the gating Stop never reached a terminal
//     status before the stall deadline (#1732's reaper), so the Start was
//     never dispatched. Rendered as a warning badge; the returned title
//     explains why no Start happened, since state.FailureReason for an
//     expired record is the reaper's own internal message, not something
//     an operator needs verbatim.
//
// title is always a static string derived from state -- callers must not
// substitute a relative/formatted timestamp into it (NFR11, #1735's byte-
// stability requirement): this content renders inside the SSE-pushed row
// fragment, where any per-render-varying content defeats the no-swap
// change detector.
func RestartBadge(state *manmanpb.PendingRestartState) (label string, variant htmxui.BadgeVariant, title string, show bool) {
	if state == nil {
		return "", htmxui.BadgeNeutral, "", false
	}
	switch state.GetStatus() {
	case "pending":
		return "Restarting", htmxui.BadgeInfo, "", true
	case "failed":
		return "Restart failed", htmxui.BadgeError, state.GetFailureReason(), true
	case "expired":
		return "Restart stalled", htmxui.BadgeWarning, "Stop never completed, so the Start was not dispatched.", true
	default:
		// "started" and any unrecognised future status: fail closed to no
		// badge rather than guessing at styling for a status this mapping
		// doesn't know about.
		return "", htmxui.BadgeNeutral, "", false
	}
}
