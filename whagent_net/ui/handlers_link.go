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

	// Outcomes of the manmanv2 unlink flow.
	outcomeUnlinked  = "unlinked"
	outcomeNotLinked = "not_linked"
)

// handleLinkASSStart is POST /link/ass (FR1/FR2). See startLink.
func (app *App) handleLinkASSStart(w http.ResponseWriter, r *http.Request) {
	app.startLink(w, r, app.assLinkURL, "ASS", "/link/whagent", "/link/ass/result")
}

// handleLinkManmanv2Start is POST /link/manmanv2: the same flow as
// handleLinkASSStart, redirecting to the manmanv2 UI's link endpoint.
func (app *App) handleLinkManmanv2Start(w http.ResponseWriter, r *http.Request) {
	app.startLink(w, r, app.manmanv2LinkURL, "manmanv2", "/link/whagent", "/link/manmanv2/result")
}

// handleUnlinkManmanv2Start is POST /unlink/manmanv2: mints the same
// assertion and redirects to the manmanv2 UI's unlink endpoint, which removes
// the mapping after the Operator confirms there.
func (app *App) handleUnlinkManmanv2Start(w http.ResponseWriter, r *http.Request) {
	app.startLink(w, r, app.manmanv2LinkURL, "manmanv2", "/unlink/whagent", "/link/manmanv2/result")
}

// startLink answers a plain "not configured" message when baseURL is empty
// -- never a 500, never a redirect to an empty host. Otherwise it mints a
// linkassert.Key assertion for the signed-in Operator (iss = app.publicURL,
// sub/sub_iss = the Operator's own Keycloak identity) and 303-redirects to
// baseURL+targetPath carrying it on the "token" query parameter. This
// is the only call site in this binary that invokes linkassert.Key.Mint
// (FR1's "never silently automatic").
func (app *App) startLink(w http.ResponseWriter, r *http.Request, baseURL, label, targetPath, resultPath string) {
	logger := logging.Get("main")

	if baseURL == "" {
		http.Error(w, "Linking your account to "+label+" is not configured on this deployment.", http.StatusServiceUnavailable)
		return
	}

	user := htmxauth.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	returnURL := app.publicURL + resultPath
	token, err := app.linkAssertKey.Mint(app.publicURL, user.Sub, app.oidcIssuer, returnURL, time.Now())
	if err != nil {
		logger.Error("failed to mint link assertion", "error", err, "target", label)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	dest := baseURL + targetPath + "?" + url.Values{"token": {token}}.Encode()
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

// linkManmanv2ResultMessage maps the "outcome" query parameter the manmanv2
// UI's redirect carries to a message; anything else is the generic failure.
func linkManmanv2ResultMessage(outcome string) pages.LinkManmanv2ResultData {
	switch outcome {
	case outcomeLinked:
		return pages.LinkManmanv2ResultData{
			Heading: "Linked",
			Message: "Your account is now linked. Agent calls made on your behalf now act as your manmanv2 user.",
			Success: true,
		}
	case outcomeAlreadyLinked:
		return pages.LinkManmanv2ResultData{
			Heading: "Already linked",
			Message: "Your account was already linked to this same manmanv2 user -- nothing changed.",
			Success: true,
		}
	case outcomeConflict:
		return pages.LinkManmanv2ResultData{
			Heading: "Already linked to a different user",
			Message: "Your account is already linked to a different manmanv2 user. There is no automatic re-link -- contact an administrator if this is unexpected.",
			Success: false,
		}
	case outcomeUnlinked:
		return pages.LinkManmanv2ResultData{
			Heading: "Unlinked",
			Message: "Your account is no longer linked. Agent calls made on your behalf can no longer act as your manmanv2 user.",
			Success: true,
		}
	case outcomeNotLinked:
		return pages.LinkManmanv2ResultData{
			Heading: "Not linked",
			Message: "Your account was not linked to manmanv2 -- nothing changed.",
			Success: true,
		}
	case outcomeRejected:
		return pages.LinkManmanv2ResultData{
			Heading: "Link request rejected",
			Message: "The link request was invalid, expired, or already used. Please retry the link from your grants page.",
			Success: false,
		}
	default:
		return pages.LinkManmanv2ResultData{
			Heading: "Link failed",
			Message: "Something went wrong linking your account. Please retry the link from your grants page.",
			Success: false,
		}
	}
}

// handleLinkManmanv2Result is GET /link/manmanv2/result.
func (app *App) handleLinkManmanv2Result(w http.ResponseWriter, r *http.Request) {
	data := linkManmanv2ResultMessage(r.URL.Query().Get("outcome"))
	data.Layout = components.LayoutData{
		Title:  "Link manmanv2 identity",
		Active: "Grants",
		User:   htmxauth.GetUser(r.Context()),
	}

	if err := RenderTempl(w, r, data.Layout.Title, pages.LinkManmanv2Result(data)); err != nil {
		logging.Get("main").Error("failed to render link manmanv2 result page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
