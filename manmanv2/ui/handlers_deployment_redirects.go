package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Deployment-first redirects (task #2279, FR3/FR16, amendments A1-A3). The
// two pages FR16 retires -- the deployment list at "/sgc/" and the SGC
// detail page at "/sgc/<id>" -- are what M5's Games page (#2270, #2272)
// fully replaced, so neither renders anything of its own anymore: both
// permanently redirect to the equivalent new view (NFR8). "/deployments"
// and "/deployments/<id>" (A3) are the new deployment-first names for those
// same two retired pages and carry identical behaviour -- registered
// separately so a bookmark or runbook written against either name resolves
// the same way.
//
// Do not add a third code path here: A2 is explicit that the three
// non-page "/sgc/..." routes (add-library, remove-library,
// api/available-libraries) are untouched by this retirement -- they are
// registered directly in setupRoutes (main.go) and never reach this file.

// handleDeploymentsListRedirect retires the "/sgc/" deployment-list page
// (FR16): a permanent redirect to the Games page, which is what replaced
// it. There never was a real "list" page here to preserve an identifier
// for (unlike the detail redirect below) -- Games is simply the equivalent
// new view (NFR8).
func (app *App) handleDeploymentsListRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/games", http.StatusMovedPermanently)
}

// handleDeploymentDetailRedirectRoute is "/deployments/"'s handler: it
// parses the deployment (ServerGameConfig) id out of the path and defers to
// handleSGCDetailRedirect below -- the same lookup-and-redirect "/sgc/<id>"
// uses via handleSGCRoutes (main.go). A bare "/deployments/" with no id
// segment falls back to the list redirect, mirroring handleSGCRoutes' own
// bare-"/sgc/" case.
func (app *App) handleDeploymentDetailRedirectRoute(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(pathParts) < 2 || pathParts[1] == "" {
		app.handleDeploymentsListRedirect(w, r)
		return
	}
	app.handleSGCDetailRedirect(w, r, pathParts[1])
}

// handleSGCDetailRedirect retires the "/sgc/<id>" SGC detail page (FR16,
// amendment A1): a permanent redirect to the Games page with that
// deployment's game expanded, resolved through the required two-hop lookup
// -- ServerGameConfig -> GameConfig -> Game, because ServerGameConfig
// itself carries no game_id (only GameConfig does).
//
// Per A1, a permanent redirect must never depend on a lookup succeeding:
// an unparseable id, a missing/errored SGC fetch, a missing/errored
// GameConfig fetch, or a missing/errored Game fetch all fall back to a
// plain "/games" redirect with no expand parameter -- never an error
// response. An unknown or stale expand value is the Games page's problem
// to silently ignore (#2270), not this handler's to avoid producing.
func (app *App) handleSGCDetailRedirect(w http.ResponseWriter, r *http.Request, sgcIDStr string) {
	target := "/games"

	if sgcID, err := strconv.ParseInt(sgcIDStr, 10, 64); err == nil {
		ctx := r.Context()
		if sgc, err := app.fetchSGC(ctx, sgcID); err == nil && sgc != nil {
			if gc, err := app.fetchGameConfig(ctx, sgc.GetGameConfigId()); err == nil && gc != nil {
				if game, err := app.grpc.GetGame(ctx, gc.GetGameId()); err == nil && game != nil {
					target = fmt.Sprintf("/games?expand=%d", game.GetGameId())
				}
			}
		}
	}

	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
