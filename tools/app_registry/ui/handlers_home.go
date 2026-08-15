package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleHome is the placeholder authenticated screen that proves the seam
// this scaffold exists to prove: session -> roles -> forwarded user token
// -> a real gRPC read. It must never render an unknown/partial state as
// healthy (NFR-6) — a failed registry read surfaces as an error page, not
// a silently empty list.
func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	resp, err := app.registry.Environment.ListEnvironments(r.Context(), &pb.ListEnvironmentsRequest{})
	if err != nil {
		log.Printf("ListEnvironments failed: %v", err)
		http.Error(w, "Failed to load environments from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	if err := RenderTempl(w, r, "App Registry", pages.Home(user, resp.GetEnvironments())); err != nil {
		log.Printf("Failed to render home page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
