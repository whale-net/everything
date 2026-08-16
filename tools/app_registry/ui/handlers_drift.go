package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleDriftAudit is screen 40-drift-audit ("Drift & Audit" in nav,
// FR-32/FR-33/FR-9/NFR-6/NFR-10). See drift_data.go for how the two
// sections (drifted overrides, adopted artifacts) are assembled. This is a
// read-side audit screen only — the adopt action (screen 52) is deferred,
// so there is no write control here.
func (app *App) handleDriftAudit(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	owner := r.URL.Query().Get("owner")

	data, err := app.buildDriftAudit(r.Context(), owner)
	if err != nil {
		log.Printf("buildDriftAudit failed: %v", err)
		http.Error(w, "Failed to load drift/audit state from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	if err := RenderTempl(w, r, "Drift & Audit", pages.DriftAudit(user, data.Matrix, data.DriftRows, data.AdoptedArtifacts, data.AdoptedErr, owner)); err != nil {
		log.Printf("Failed to render drift audit page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
