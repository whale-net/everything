// FR14/FR15/NFR3 (issue #2433, plan #2421): the admin all-operators grant
// list and revoke page. GET /admin/grants lists every operator's
// delegated grants (grantindex.ListAll, #2425) with a live per-domain
// status read (grpcauth.Store.Status, never grantindex -- FR12, mirrors
// ../handlers_grants.go's buildGrantRows). POST /admin/grants/revoke
// revokes exactly one (subject, grant) pair belonging to whichever
// operator the request names -- unlike the self-service page
// (handlers_grants.go), the caller here is an admin, never the grant's
// own subject, so the target subject necessarily comes from the request,
// not the session.
//
// Reachable only to an operator whose token carries the designated admin
// realm role. NFR3 is the reason this page cannot reuse
// htmxauth.GetUser(ctx).Roles for that check the way a simpler gate
// might: DBSessionManager.GetUserInfo caches Roles at sign-in for the
// full 24h session TTL with no refresh, so a revoked admin role would
// still pass that check for up to a day -- directly undercutting the
// admin-offboarding story this plan exists for. The Implementation phase
// instead gates on the roles carried by app.auth.GetAccessToken(r)'s
// result (htmxauth.DBSessionManager.GetAccessToken, which does refresh
// against Keycloak's token endpoint), so a revoked admin role stops
// working on the very next request. See this issue's Implementation
// section for the exact gate.
package main

import (
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// handleGrantsAdmin is GET /admin/grants (FR14, issue #2433).
//
// Scaffold stub: always renders an empty row list and does not yet gate
// on the admin role (FR15/NFR3) or list anything. The Implementation
// phase adds the fresh-role gate (see this file's package doc comment,
// returning 403 -- never a silently-empty list -- for a non-admin) and
// wires app.grant.Index.ListAll plus a live app.grant.Store.Status read
// per row (reusing ../handlers_grants.go's buildGrantRows shape).
func (app *App) handleGrantsAdmin(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")

	data := pages.GrantsAdminData{
		Layout: components.LayoutData{
			Title:  "All operator grants",
			Active: "Grants",
			User:   htmxauth.GetUser(r.Context()),
		},
	}

	if err := RenderTempl(w, r, data.Layout.Title, pages.GrantsAdmin(data)); err != nil {
		logger.Error("failed to render admin grants page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleGrantsAdminRevoke is POST /admin/grants/revoke (FR14/FR17, issue
// #2433).
//
// Scaffold stub: validates the submitted domain and target-subject fields
// but does not yet gate on the admin role (FR15/NFR3) or call
// app.grant.Store.Revoke. The Implementation phase adds the fresh-role
// gate, the real revoke call against the named (subject, domain) pair
// (never the admin's own session subject -- this is the one handler in
// this binary that revokes on someone else's behalf), and the required
// INFO audit log naming both the admin and the grant's own subject
// (NFR4).
func (app *App) handleGrantsAdminRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	domain := strings.TrimSpace(r.FormValue("domain"))
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	subjectSub := strings.TrimSpace(r.FormValue("subject_sub"))
	if subjectSub == "" {
		http.Error(w, "subject_sub is required", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/admin/grants", http.StatusSeeOther)
}
