package main

import (
	"context"
	"net/http"

	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleDeploymentAction routes POST /sessions/deployments/{sgcID}/{start|stop|restart}.
//
// Registered ahead of the pre-existing "/sessions/" catch-all in main.go;
// Go's ServeMux longest-pattern-wins means this handler claims
// "/sessions/deployments/" before handleSessionDetail's "/sessions/" ever
// sees it, so handleSessionDetail's positional path-segment parsing never
// mistakes "deployments" for a session id.
//
// TODO(#1627 Implementation): parse {sgcID}/{verb}, method-guard non-POST
// (405), reject unparseable sgcID (400) and unknown verb (404), then
// dispatch to the Start/Stop/Restart flows described on #1627.
func (app *App) handleDeploymentAction(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Not implemented", http.StatusNotImplemented)
}

// renderDeploymentRow re-fetches a single deployment's state and writes the
// pages.DeploymentRow fragment, with actionErr surfaced inline (FR8).
//
// TODO(#1627 Implementation): for HTMX requests, render pages.DeploymentRow
// directly (200, fragment only, no layout -- mirrors handlers_home.go's
// components.DashboardSummary(data).Render(r.Context(), w) pattern); for
// non-HTMX requests (no HX-Request header), redirect back to /sessions
// preserving server_id instead of returning a bare fragment.
func (app *App) renderDeploymentRow(w http.ResponseWriter, r *http.Request, sgcID int64, actionErr string) {
	_ = sgcID
	_ = actionErr
	http.Error(w, "Not implemented", http.StatusNotImplemented)
}

// buildDeploymentRowData resolves one deployment's row view model from the
// API.
//
// TODO(#1627 Implementation): fetch the SGC (GetServerGameConfig), its
// sessions (ListSessionsWithFilters scoped to sgcID), and derive
// LatestSession/LiveSession/Actions the same way handleSessions does today
// (components.LatestSession + components.ComputeDeploymentActions), plus
// the display-name resolution handleSessions already performs.
func (app *App) buildDeploymentRowData(ctx context.Context, sgcID int64) (pages.DeploymentRowData, error) {
	_ = ctx
	return pages.DeploymentRowData{ServerGameConfigID: sgcID}, nil
}
