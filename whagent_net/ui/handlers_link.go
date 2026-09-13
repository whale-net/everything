// FR1/FR2/FR10/FR13/NFR2 (issue #2596): the Operator-facing half of the
// "Link ASS identity" flow -- handleLinkASSStart mints a linkassert.Key
// assertion for the signed-in Operator and redirects their browser to ASS
// `web`'s link-acceptance endpoint carrying it; handleLinkASSResult
// renders the outcome once ASS redirects the browser back to `ui`. Both
// routes sit behind app.auth.RequireAuthFunc only (FR13) -- there is no
// API, gRPC, or token-authenticated path to either, and handleLinkASSStart
// is the only call site that will ever invoke linkassert.Key.Mint (FR1's
// "never silently automatic").
//
// whagent-net records nothing durable about the link itself (NFR2): the
// linked state lives entirely in ASS's person_oidc_identity table, never
// a new whagent-net-local table, column, cache, or session field.
package main

import (
	"net/http"

	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// handleLinkASSStart is POST /link/ass (FR1/FR2). Scaffold phase: always
// answers 501 -- Implementation phase fills in the mint + redirect below.
//
// TODO(Implementation phase): when app.assLinkURL == "" (WHAGENT_UI_ASS_
// LINK_URL unset), respond with a plain "link flow not configured"
// message -- never a 500, never a redirect to an empty host. Otherwise:
// read the signed-in Operator's (iss, sub) from app.oidcIssuer/
// htmxauth.GetUser(ctx).Sub (mirrors handlers_grants.go's own precedent),
// call app.linkAssertKey.Mint with a return URL of
// "{WHAGENT_UI_PUBLIC_URL}/link/ass/result", and issue a 303 redirect to
// app.assLinkURL's link-acceptance endpoint carrying the resulting
// assertion.
func (app *App) handleLinkASSStart(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

// handleLinkASSResult is GET /link/ass/result (FR10). Scaffold phase:
// renders pages.LinkASSResult's skeleton with no outcome resolved yet.
//
// TODO(Implementation phase): resolve the outcome query parameter ASS's
// redirect carries into one of pages.LinkASSResult's four distinct
// messages (linked / already linked / conflict / rejected assertion --
// see #2600 for the exact query-parameter contract to mirror). An
// unrecognized or absent value must render the generic failure, never a
// success.
func (app *App) handleLinkASSResult(w http.ResponseWriter, r *http.Request) {
	if err := RenderTempl(w, r, "Link ASS identity", pages.LinkASSResult(pages.LinkASSResultData{})); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
