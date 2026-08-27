package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handleHome is the Phase 1 (FR13) protected landing page: it proves the
// login -> DB-backed-session -> protected-route path renders real content,
// never a blank body. Wrapped by app.auth.RequireAuthFunc (redirects an
// unauthenticated/expired-session request to the login flow) and
// app.auth.WithAccessToken (injects the user's own token for gRPC calls;
// NFR18.1) in main.go's setupRoutes.
//
// The device/region/reading dashboard content itself, the degraded-page
// rendering for a leaflab-api dial failure or HEALTH_DEGRADED (NFR14), and
// the session-expiry-mid-request HTML response (FR13) are Implementation-
// phase work on this issue, not scaffolded here.
func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	if err := RenderTempl(w, r, "LeafLab", components.HomePage(user)); err != nil {
		log.Printf("ERROR: failed to render home page: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
