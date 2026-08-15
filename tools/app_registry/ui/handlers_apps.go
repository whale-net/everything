package main

import (
	"log"
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleAppsCatalog is screen 20-apps-catalog (FR-22): every registered
// app/chart, its domain, deploy unit, and current promotability per
// environment, without drilling in. The search/domain/deploy-unit filters
// are plain GET query params applied server-side — this UI has no client
// JS framework to filter with (NFR-20).
func (app *App) handleAppsCatalog(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	environments, rows, err := app.buildAppsCatalog(r.Context())
	if err != nil {
		log.Printf("buildAppsCatalog failed: %v", err)
		http.Error(w, "Failed to load apps catalog from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	filter := pages.AppsCatalogFilter{
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Domain:     r.URL.Query().Get("domain"),
		DeployUnit: r.URL.Query().Get("unit"),
	}

	if err := RenderTempl(w, r, "Apps Catalog", pages.AppsCatalog(user, environments, rows, filter)); err != nil {
		log.Printf("Failed to render apps catalog page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}

// handleAppDetail is screen 11-app-detail (FR-19/FR-21 link/FR-25): one app
// across every environment plus its promotion timeline. {id} is the app's
// full_name ("<domain>-<name>") — the same identity the catalog links with
// and the CLI's owner_full_name accepts, so a URL here is copy/paste
// compatible with `app-registry-cli` invocations.
func (app *App) handleAppDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	fullName := r.PathValue("id")
	if fullName == "" {
		http.NotFound(w, r)
		return
	}

	data, err := app.buildAppDetail(r.Context(), fullName)
	if err != nil {
		log.Printf("buildAppDetail(%q) failed: %v", fullName, err)
		http.Error(w, "Failed to load app "+fullName+" from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}

	if err := RenderTempl(w, r, "App: "+fullName, pages.AppDetail(user, data)); err != nil {
		log.Printf("Failed to render app detail page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
