package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleArtifactDetail is screen 21-artifact-detail (FR-23): one artifact by
// digest -- resolve, not lookup-by-owner -- with build provenance and, for
// an image artifact, the "pinned by" chart list. {digest} carries the raw
// "sha256:..." value; every digest link elsewhere in this UI that points
// here builds its href as "/artifacts/" + digest (see chart_data.go /
// artifact_data.go). See artifact_data.go's buildArtifactDetail.
func (app *App) handleArtifactDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	digest := r.PathValue("digest")
	if digest == "" {
		http.NotFound(w, r)
		return
	}

	data, err := app.buildArtifactDetail(r.Context(), digest)
	if err != nil {
		log.Printf("buildArtifactDetail(%q) failed: %v", digest, err)
		http.Error(w, "Failed to load artifact "+digest+" from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}
	if data == nil {
		http.Error(w, "No artifact recorded for digest "+digest, http.StatusNotFound)
		return
	}

	if err := RenderTempl(w, r, "Artifact: "+digest, pages.ArtifactDetail(user, data)); err != nil {
		log.Printf("Failed to render artifact detail page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
