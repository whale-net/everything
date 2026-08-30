package main

import (
	"log"
	"net/http"

	"github.com/whale-net/everything/leaflab/ui/components"
	"github.com/whale-net/everything/leaflab/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxauth"
)

// handleHome is the minimal authenticated landing page (Scaffold's "Home
// page" requirement): it proves sign-in end to end by showing the
// signed-in user and linking to the boards list. The boards list itself is
// #1497's screen task.
func (app *App) handleHome(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	layoutData := components.LayoutData{
		Title: "LeafLab",
		User:  user,
	}

	if err := RenderTempl(w, r, "LeafLab", pages.Home(layoutData)); err != nil {
		log.Printf("Error rendering home page: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
