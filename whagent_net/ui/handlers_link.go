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
	"net/url"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// The four FR10 outcome values ASS `web`'s redirect back to
// GET /link/ass/result carries on its "outcome" query parameter -- mirrors
// audience_score_system/web/link/handlers.go's own outcome* constants
// verbatim (that package is the producer of these values; this package is
// only ever a consumer).
const (
	outcomeLinked        = "linked"
	outcomeAlreadyLinked = "already_linked"
	outcomeConflict      = "conflict"
	outcomeRejected      = "rejected"
)

// handleLinkASSStart is POST /link/ass (FR1/FR2). When app.assLinkURL is
// unconfigured, answers a plain "not configured" message -- never a 500,
// never a redirect to an empty host. Otherwise mints a linkassert.Key
// assertion for the signed-in Operator (iss = app.publicURL, sub/sub_iss =
// the Operator's own Keycloak identity, mirroring handlers_grants.go's
// handleGrants/handleGrantsRevoke precedent) and issues a 303 redirect to
// ASS `web`'s link-acceptance endpoint carrying it on the "token" query
// parameter. This is the only call site in this binary that invokes
// linkassert.Key.Mint (FR1's "never silently automatic").
func (app *App) handleLinkASSStart(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")

	if app.assLinkURL == "" {
		http.Error(w, "Linking your account to ASS is not configured on this deployment.", http.StatusServiceUnavailable)
		return
	}

	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	returnURL := app.publicURL + "/link/ass/result"
	token, err := app.linkAssertKey.Mint(app.publicURL, user.Sub, app.oidcIssuer, returnURL, time.Now())
	if err != nil {
		logger.Error("failed to mint link assertion", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	dest := app.assLinkURL + "/link/whagent?" + url.Values{"token": {token}}.Encode()
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// linkASSResultMessage is FR10's outcome-to-message mapping: exactly one
// of the four values ASS `web`'s redirect carries maps to its own
// distinct message; anything else (including an absent outcome) maps to
// the generic failure -- never a success.
func linkASSResultMessage(outcome string) pages.LinkASSResultData {
	switch outcome {
	case outcomeLinked:
		return pages.LinkASSResultData{
			Heading: "Linked",
			Message: "Your account is now linked. Agent calls made on your behalf now resolve to your existing ASS person.",
			Success: true,
		}
	case outcomeAlreadyLinked:
		return pages.LinkASSResultData{
			Heading: "Already linked",
			Message: "Your account was already linked to this same ASS person -- nothing changed.",
			Success: true,
		}
	case outcomeConflict:
		return pages.LinkASSResultData{
			Heading: "Already linked to a different person",
			Message: "Your account is already linked to a different ASS person. There is no automatic re-link or merge -- contact an administrator if this is unexpected.",
			Success: false,
		}
	case outcomeRejected:
		return pages.LinkASSResultData{
			Heading: "Link request rejected",
			Message: "The link request was invalid, expired, or already used. Please retry the link from your grants page.",
			Success: false,
		}
	default:
		return pages.LinkASSResultData{
			Heading: "Link failed",
			Message: "Something went wrong linking your account. Please retry the link from your grants page.",
			Success: false,
		}
	}
}

// handleLinkASSResult is GET /link/ass/result (FR10): resolves the
// "outcome" query parameter ASS `web`'s redirect carries into one of
// linkASSResultMessage's four distinct messages (or the generic failure
// for anything else) and renders it.
func (app *App) handleLinkASSResult(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	data := linkASSResultMessage(r.URL.Query().Get("outcome"))
	data.Layout = components.LayoutData{
		Title:  "Link ASS identity",
		Active: "Grants",
		User:   user,
	}

	if err := RenderTempl(w, r, data.Layout.Title, pages.LinkASSResult(data)); err != nil {
		logging.Get("main").Error("failed to render link ASS result page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
