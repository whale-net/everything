package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/tools/app_registry/ui/pages"
)

// handleChartDetail is screen 22-chart-detail (FR-24/FR-25): the chart's
// current version per environment and the apps published at that
// artifact's version, kept visually distinct from the chart's currently
// declared composition (chart_app) -- see chart_data.go's buildChartDetail.
// {id} is the chart's full_name, matching /apps/{id}'s and the apps
// catalog's rowDetailHref convention (pages/apps_catalog.templ).
func (app *App) handleChartDetail(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	fullName := r.PathValue("id")
	if fullName == "" {
		http.NotFound(w, r)
		return
	}

	data, err := app.buildChartDetail(r.Context(), fullName)
	if err != nil {
		log.Printf("buildChartDetail(%q) failed: %v", fullName, err)
		http.Error(w, "Failed to load chart "+fullName+" from app-registry-api: "+err.Error(), http.StatusBadGateway)
		return
	}
	if data == nil {
		http.Error(w, "No chart named "+fullName+" is registered", http.StatusNotFound)
		return
	}

	if err := RenderTempl(w, r, "Chart: "+fullName, pages.ChartDetail(user, data)); err != nil {
		log.Printf("Failed to render chart detail page: %v", err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
	}
}
