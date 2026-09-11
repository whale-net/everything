package main

import (
	"net/http"
	"strconv"
	"strings"
)

// M6 navigation/disposition redirects (task #2372, root plan #2359, FR16/
// FR17). Follows the shipped M5 pattern
// (handlers_deployment_redirects.go, task #2279): every pre-redesign page
// this task's nav swap retires becomes a permanent redirect onto its
// replacement, never a stub that renders nothing (AC6).
//
// Three pre-redesign entry points retire here:
//   - "/servers" -> "/infrastructure" (task #2369's replacement page).
//   - "/servers/<id>" -> "/infrastructure?manage=<id>", preserving the
//     identifier (FR16 is explicit about this) -- Infrastructure has no
//     separate per-host route (it is a flat list, #2369), so the
//     identifier is carried as a query param that opens that host's
//     "Manage" panel in place, mirroring #2279's own precedent of
//     preserving "/sgc/<id>" as a query param ("/games?expand=<id>")
//     rather than a literal path segment, for the same reason: the
//     replacement page has no equivalent per-entity route of its own.
//   - "/workshop/library" -> "/workshop" (task #2362's replacement page,
//     which fully absorbed library management, collection-add,
//     batch-addon-create, and cache-backed install per that task's own
//     validation criteria -- so unlike "/workshop/cache" and every other
//     "/workshop/*" sub-route, this is the one Workshop entry point the
//     new page actually supersedes).
//   - "/sessions" -> "/activity" (the M5 Activity page, #2271, FR17).
//     "/sessions/<id>" and its sub-routes (stop, actions/execute,
//     logs/*, stdin) are NOT part of this disposition -- both Activity
//     and Games still link directly to "/sessions/<id>" as the session
//     detail/log-viewer page (manmanv2/docs/DESIGN_UI_REDESIGN.md's
//     "Session detail" TBD entry: reshaping that page is explicitly
//     deferred, "M6/C31 phase 2" per pages/activity.templ's
//     ActivityHistoryTable doc comment) -- redirecting it here would break
//     both pages' own links (NFR6).

// handleServersRedirect retires the "/servers" fleet-list page (FR16): a
// permanent redirect to Infrastructure, which replaced it (task #2369).
func (app *App) handleServersRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/infrastructure", http.StatusMovedPermanently)
}

// handleServerDetailRedirect is "/servers/"'s handler: it retires every
// "/servers/<id>..." route (the detail page itself, and its
// update-address/ports/* action sub-routes, both folded into
// Infrastructure's per-host Manage panel by this task -- see
// pages.InfrastructureHost's doc comment) into a single permanent redirect
// onto Infrastructure with the host id preserved as "?manage=<id>" (FR16).
// A bare "/servers/" with no id segment falls back to the plain list
// redirect, mirroring handleSGCRoutes' own bare-"/sgc/" case
// (handlers_deployment_redirects.go).
func (app *App) handleServerDetailRedirect(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 || pathParts[1] == "" {
		app.handleServersRedirect(w, r)
		return
	}
	serverID, err := strconv.ParseInt(pathParts[1], 10, 64)
	if err != nil {
		app.handleServersRedirect(w, r)
		return
	}
	http.Redirect(w, r, infrastructureManageRedirectTarget(serverID), http.StatusMovedPermanently)
}

// handleWorkshopLibraryRedirect retires the "/workshop/library" page
// (FR16): a permanent redirect to "/workshop", which fully absorbed it
// (task #2362).
func (app *App) handleWorkshopLibraryRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/workshop", http.StatusMovedPermanently)
}

// handleSessionsRedirect retires the "/sessions" list page (FR17): a
// permanent redirect to Activity, which replaced it (task #2271, M5).
// "/sessions/<id>" and its sub-routes are untouched -- see this file's doc
// comment above.
func (app *App) handleSessionsRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/activity", http.StatusMovedPermanently)
}
