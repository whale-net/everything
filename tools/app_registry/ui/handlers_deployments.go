package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// asOfQueryFormat matches the <input type="datetime-local"> value format
// pages.Deployments renders — the inverse of pages.asOfInputValue.
const asOfQueryFormat = "2006-01-02T15:04"

// parseAsOf parses the FR-3/FR-7 "at" URL query parameter. An empty value
// means "current state" (0). A non-empty, unparseable value is a client
// error — never silently fall back to current state, which would make the
// URL lie about what's on screen.
func parseAsOf(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	t, err := time.ParseInLocation(asOfQueryFormat, raw, time.UTC)
	if err != nil {
		return 0, fmt.Errorf("expected %s (UTC), got %q: %w", asOfQueryFormat, raw, err)
	}
	return t.Unix(), nil
}

// handleDeployments is screen 10-environment-status ("Deployments" in nav,
// FR-4/FR-5/FR-6/FR-7/FR-8/FR-9/FR-43/NFR-4/NFR-12). See
// deployments_data.go for how the matrix is assembled.
func (app *App) handleDeployments(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	at, err := parseAsOf(r.URL.Query().Get("at"))
	if err != nil {
		http.Error(w, "invalid 'at' parameter: "+err.Error(), http.StatusBadRequest)
		return
	}

	m, err := app.buildDeploymentMatrix(r.Context(), at)
	if err != nil {
		log.Printf("buildDeploymentMatrix failed: %v", err)
		http.Error(w, "Failed to load deployment state from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	if err := RenderTempl(w, r, "Deployments", pages.Deployments(user, m, at != 0)); err != nil {
		log.Printf("Failed to render deployments page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
