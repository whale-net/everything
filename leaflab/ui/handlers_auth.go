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

// handleHome is the Phase 1 (FR13) protected landing page: it proves the
// login -> DB-backed-session -> protected-route path renders real content,
// never a blank body. Wrapped by app.auth.RequireAuthFunc (redirects an
// unauthenticated/expired-session request to the login flow) and
// app.auth.WithAccessToken (injects the user's own token for gRPC calls;
// NFR18.1) in main.go's setupRoutes.
//
// Before rendering the placeholder dashboard content, it probes
// leaflab-api's anonymous GetHealth RPC (FR63) so a dial failure or a
// HEALTH_DEGRADED response resolves to the honest "our problem" page
// (NFR14) instead of a stack trace, a JSON body, or this handler simply
// failing further down once real screens call authenticated RPCs. The
// device/region/reading dashboard content itself is later scaffolding on
// this plan, not this issue's scope.
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

	if err := RenderTempl(w, r, "LeafLab", components.HomePage(user)); err != nil {
		log.Printf("ERROR: failed to render home page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
