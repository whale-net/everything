package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"github.com/whale-net/everything/manmanv2/ui/pages"
)

// handleWorkshopPage renders the redesigned Workshop top-level page
// (root plan #2359, task #2362 -- FR6/FR7), registered at "/workshop" in
// main.go alongside the other authenticated routes. It is additive: the
// pre-existing "/workshop/library" page and every "/workshop/*"
// sub-route (handlers_workshop.go) are untouched by this handler and
// stay reachable (NFR6).
//
// Scaffold only: this handler builds layout data and renders the page
// skeleton (pages.WorkshopPage). Fetching libraries/addons/batch
// jobs/cache stats and wiring the FR7 collection-add, batch-addon-create,
// and cache-backed install Blade flows land in the Implementation phase
// of this same task.
func (app *App) handleWorkshopPage(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	breadcrumbs := []components.Breadcrumb{
		{Label: "Workshop", URL: "/workshop"},
	}
	// "Workshop" matches navItems' nav.templ label exactly (see
	// components/layout.templ's navLink) so the nav entry highlights when
	// this page is active. The dependent navigation/disposition task
	// swaps that nav link's target from "/workshop/library" to
	// "/workshop" -- this handler does not touch layout.templ.
	layoutData, err := app.buildTemplLayoutData(r, "Workshop", "Workshop", user, breadcrumbs)
	if err != nil {
		log.Printf("Error building layout data: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	pageData := pages.WorkshopPageData{
		Layout: layoutData,
	}

	if err := RenderTempl(w, r, "Workshop", pages.WorkshopPage(pageData)); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
