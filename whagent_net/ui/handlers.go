package main

import (
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// handleIndex is a placeholder authenticated index page (this task's
// scope, issue #2236): it renders the shared shell and the signed-in
// operator's identity. Real session pages (FR1-FR4) replace this in
// later tasks under plan #2233.
func (app *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())

	layoutData := components.LayoutData{
		Title:  "whagent-net",
		Active: "Home",
		User:   user,
	}

	if err := RenderTempl(w, r, "whagent-net", pages.Index(layoutData)); err != nil {
		logging.Get("main").Error("failed to render index page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
