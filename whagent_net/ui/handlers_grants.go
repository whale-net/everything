// FR16/FR17 (issue #2432, plan #2421): the self-service per-domain grant
// list and revoke page. GET /grants lists the signed-in operator's own
// delegated grants (grantindex.ListBySubject, #2425) with a live per-
// domain status read (grpcauth.Store.Status, never grantindex -- FR12);
// POST /grants/revoke revokes exactly one (subject, grant) pair
// (grpcauth.Store.Revoke, FR17). Both handlers take the subject from the
// signed-in session (htmxauth.GetUser), never from a request parameter or
// form field.
package main

import (
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// handleGrants is GET /grants (FR16, issue #2432).
//
// Scaffold stub: always renders an empty row list. The Implementation
// phase wires this up to app.grant.Index.ListBySubject for the signed-in
// operator's own (iss, sub) plus a live app.grant.Store.Status read per
// domain (FR12: status is never read from the index, and is never
// cached).
func (app *App) handleGrants(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")

	data := pages.GrantsData{
		Layout: components.LayoutData{
			Title:  "My grants",
			Active: "Grants",
			User:   htmxauth.GetUser(r.Context()),
		},
	}

	if err := RenderTempl(w, r, data.Layout.Title, pages.Grants(data)); err != nil {
		logger.Error("failed to render grants page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleGrantsRevoke is POST /grants/revoke (FR17, issue #2432).
//
// Scaffold stub: validates the submitted domain field but does not yet
// call app.grant.Store.Revoke. The Implementation phase wires that call
// (subject taken from the session, never the request body/query -- FR16/
// FR17's "tampered request supplying another operator's subject" case)
// plus the required INFO audit log (NFR4: who revoked, whose grant, which
// domain -- never token material).
func (app *App) handleGrantsRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	domain := strings.TrimSpace(r.FormValue("domain"))
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/grants", http.StatusSeeOther)
}
