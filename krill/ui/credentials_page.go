package main

import (
	"net/http"

	"github.com/whale-net/everything/krill/ui/pages"
)

// handleCredentials is the credential-widget page the original UI
// shipped with, now rendered inside the shell chrome at its own route
// (FR 85a8b33c keeps it reachable from the nav rather than replacing it).
func (app *App) handleCredentials(w http.ResponseWriter, r *http.Request) {
	renderShell(w, r, "Credentials", credentialsPath, pages.Credentials())
}
