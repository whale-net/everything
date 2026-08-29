package main

import (
	"context"
	"log"
	"net/http"
	"time"

	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// healthCheckTimeout bounds handleHome's GetHealth probe so a stalled
// leaflab-api dial never hangs the page load -- a slow or unreachable API
// must still resolve to the degraded page (NFR14) within a human-scale
// wait, not block indefinitely.
const healthCheckTimeout = 3 * time.Second

// handleHome is the Phase 1 (FR13) protected landing route ("/"): it
// proves the login -> DB-backed-session -> protected-route path renders
// real content, never a blank body, before delegating anywhere else.
// Wrapped by app.auth.RequireAuthFunc (redirects an unauthenticated/
// expired-session request to the login flow) and app.auth.WithAccessToken
// (injects the user's own token for gRPC calls; NFR18.1) in main.go's
// setupRoutes.
//
// It probes leaflab-api's anonymous GetHealth RPC (FR63) so a dial
// failure or a HEALTH_DEGRADED response resolves to the honest "our
// problem" page (NFR14) instead of a stack trace, a JSON body, or a real
// screen simply failing further down once it calls an authenticated RPC.
// Once the API reports healthy, "/" renders no placeholder content of its
// own -- it delegates straight to handleBoards, the Phase 1 read-only
// boards screen (NFR18.2, NFR19, FR64 -- #1330), making that screen the
// post-login landing route. handleBoards is reachable directly at
// "/boards" too (e.g. a bookmark, or FR61's "load more" continuation base
// path); it does not repeat this health probe -- a healthy "/" already
// proved reachability for this request, and a mid-request dependency
// failure past that point degrades to handleBoards' own
// StatusBadGateway response rather than this page's styled NFR14 screen.
func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	ctx, cancel := context.WithTimeout(r.Context(), healthCheckTimeout)
	defer cancel()
	resp, err := app.api.LeafLab.GetHealth(ctx, &pb.GetHealthRequest{})
	if err != nil || resp.GetStatus() != pb.HealthStatus_HEALTH_UP {
		if err != nil {
			// Logged server-side only -- FR63.2/NFR14: the rendered page
			// never carries this detail, only "our problem, not yours".
			log.Printf("leaflab-api health probe failed: %v", err)
		} else {
			log.Printf("leaflab-api reported HEALTH_DEGRADED")
		}
		if renderErr := RenderTempl(w, r, "LeafLab", components.DegradedPage(user)); renderErr != nil {
			log.Printf("ERROR: failed to render degraded page: %v", renderErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	app.handleBoards(w, r)
}
