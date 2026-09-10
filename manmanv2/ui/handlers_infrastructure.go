package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleInfrastructure serves GET /infrastructure (FR1/FR2, C29): the
// redesigned fleet host list, additive alongside /servers (see #2369's
// "Design note" -- the nav swap and /servers redirect are the dependent
// navigation/disposition task, not this one). Scaffold phase renders the
// FR2 floor (name + drain-state badge, see pages.Infrastructure); the
// Implementation phase of this same issue adds health indicators
// (NFR5-bounded), search/filter, and the FR3/FR4 drain/undrain actions
// wired through handleInfrastructureAction below.
func (app *App) handleInfrastructure(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	ctx := r.Context()

	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers: %v", err)
		http.Error(w, "Failed to fetch servers", http.StatusInternalServerError)
		return
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

	if err := RenderTempl(w, r, "Infrastructure", pages.Infrastructure(layoutData, servers)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleInfrastructureAction serves the "/infrastructure/" prefix
// registered alongside handleInfrastructure (scaffold, #2369): today it
// carries no per-host detail or drain/undrain action route yet -- those
// (FR3 DrainServer, FR4 UndrainServer, one action per host) land in this
// same issue's Implementation phase. Any request under this prefix is a
// 404 until then, matching the "not yet built" state rather than silently
// succeeding.
func (app *App) handleInfrastructureAction(w http.ResponseWriter, r *http.Request) {
	http.NotFound(w, r)
}
