// Package main: per-domain delegated-grant consent flow (FR2, FR3, FR5,
// FR6, FR9, the write half of FR12; issue #2428, plan #2421).
//
// Two entry points exist, per the Open Question this task resolved on
// #2421 before writing any of this (see that issue comment for the full
// investigation):
//
//   - The standalone GET/POST /mcp/consent(?domain=<d>) route below is the
//     general-purpose per-domain consent flow: the domain is always
//     supplied explicitly by whatever redirected the operator here (a
//     downstream dispatch-time interrupt, issue #2430's FR7/FR8, when `mcp`
//     finds no active grant for a domain a real call is targeting -- FR5's
//     "first access to a new domain"; or issue #2431's mid-call reauth
//     routing). This route never infers or guesses a domain itself.
//   - authorizeConsentGate wraps GET /authorize (mcpauth's own OAuth2
//     endpoint for the MCP client, mounted by app.mcpProvider.Mount in
//     main.go's setupRoutes) with a domain-agnostic prerequisite: the
//     operator must have completed consent for this deployment's one
//     statically-configured default domain (WHAGENT_UI_DEFAULT_DOMAIN)
//     before a credential is minted. libs/go/mcpauth is deliberately
//     domain-agnostic (its own package doc: "NFR2 boundary -- zero
//     domain-specific types") and its /authorize implementation reads no
//     resource/scope parameter that could carry a domain (confirmed by
//     reading libs/go/mcpauth/authorize.go before writing this), and
//     mcp/server's own RFC 9728 resource identifier
//     (whagent_net/mcp/server.ResourceMetadataConfig.Resource) is one
//     single, instance-wide URL, not one per domain -- there is no wire
//     signal /authorize could read to learn which domain an MCP client is
//     really after. Per-domain resolution structurally happens later, at
//     MCP tool-dispatch time via agent_id (FR7, issue #2427, already
//     merged) -- downstream of /authorize entirely. This gate therefore
//     does not attempt to name every possible domain up front; it names
//     the one this deployment is seeded with today, and defers everything
//     else to the standalone route above.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/sessions"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/grantkey"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// pendingConsentCookieName is the signed, httpOnly cookie
// savePendingConsent/loadPendingConsent round-trip a pendingConsent through
// the browser in, between handleMCPConsentConfirm's
// DelegatedGrantSource.BeginAuthorization call and Keycloak's redirect back
// to handleMCPConsentCallback.
const pendingConsentCookieName = "whagent_net_ui_consent"

// pendingConsentMaxAge bounds the cookie's lifetime to grpcauth's own
// pendingAuthorizationTTL (10 minutes, libs/go/grpcauth/
// delegatedgrant_authcode.go) -- an abandoned consent flow's cookie expires
// no later than CompleteAuthorization would have rejected it anyway.
const pendingConsentMaxAge = 10 * 60 // seconds

// pendingConsent is what handleMCPConsentConfirm stores between
// BeginAuthorization and the Keycloak redirect returning to
// handleMCPConsentCallback: grpcauth.PendingAuthorization itself, plus the
// two pieces of caller-side bookkeeping grpcauth does not carry -- which
// domain this flow is for (Grant already IS the domain-derived key,
// grantkey.ForDomain being the identity mapping, but keeping Domain
// explicit here is more legible than re-deriving it every time it is
// read) and where to send the browser once consent completes.
type pendingConsent struct {
	grpcauth.PendingAuthorization
	Domain   string
	ReturnTo string
}

// newConsentStore builds the signed, httpOnly cookie store pendingConsent is
// round-tripped through -- mirrors htmxauth.NewDBSessionManager's own
// oauthStore construction exactly (libs/go/htmxauth/db_session.go) so this
// carries the same security properties: a third party cannot forge or read
// a value without secret (SECRET_KEY, the same value htmxauth's own session
// encryption uses). That alone is necessary but not sufficient: per this
// task's Open Question note ("bound to the signed-in operator's ui session,
// never to a cookie a third party can set"), handleMCPConsentCallback below
// additionally requires the *current*, live ui session's own resolved
// subject to match pending.Subject before ever calling
// CompleteAuthorization -- see its "bound-to-session check" comment.
func newConsentStore(secret string) *sessions.CookieStore {
	key := sha256.Sum256([]byte(secret))
	store := sessions.NewCookieStore(key[:])
	store.Options = &sessions.Options{
		// "/mcp/consent" covers both this path and "/mcp/consent/callback"
		// (browsers match cookie paths as a prefix), the only two routes
		// that ever read or write this cookie.
		Path:     "/mcp/consent",
		MaxAge:   pendingConsentMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	return store
}

// savePendingConsent JSON-encodes pc into a single session value rather
// than relying on gorilla/sessions' default gob encoding of an arbitrary
// struct type, so no gob.Register call is needed for pendingConsent.
func (app *App) savePendingConsent(w http.ResponseWriter, r *http.Request, pc pendingConsent) error {
	session, _ := app.consentStore.New(r, pendingConsentCookieName)
	payload, err := json.Marshal(pc)
	if err != nil {
		return fmt.Errorf("encode pending consent: %w", err)
	}
	session.Values["pending"] = string(payload)
	return session.Save(r, w)
}

// loadPendingConsent reads back what savePendingConsent wrote. A missing,
// expired, or tampered cookie is reported as an error -- never a zero-value
// pendingConsent silently treated as legitimate.
func (app *App) loadPendingConsent(r *http.Request) (pendingConsent, error) {
	session, err := app.consentStore.Get(r, pendingConsentCookieName)
	if err != nil {
		return pendingConsent{}, fmt.Errorf("read pending consent cookie: %w", err)
	}
	raw, ok := session.Values["pending"].(string)
	if !ok || raw == "" {
		return pendingConsent{}, fmt.Errorf("no pending consent in session")
	}
	var pc pendingConsent
	if err := json.Unmarshal([]byte(raw), &pc); err != nil {
		return pendingConsent{}, fmt.Errorf("decode pending consent: %w", err)
	}
	return pc, nil
}

// clearPendingConsent expires the cookie so a completed or abandoned
// authorization code / callback pair can never be replayed to
// CompleteAuthorization a second time.
func (app *App) clearPendingConsent(w http.ResponseWriter, r *http.Request) {
	session, err := app.consentStore.Get(r, pendingConsentCookieName)
	if err != nil {
		return
	}
	session.Options.MaxAge = -1
	_ = session.Save(r, w)
}

// authorizeConsentGate wraps mux (main.go's run()) so a GET /authorize
// request first passes through this domain-agnostic delegated-grant
// prerequisite before ever reaching mcpauth.Provider's own /authorize
// handler (mounted directly on mux by app.mcpProvider.Mount,
// setupMCPAuth/mcpauth.go) -- see this file's package doc comment for why
// the gate lives here rather than inside libs/go/mcpauth.
//
// Falls through unchanged (never intercepts) when: the request is not a GET
// /authorize; delegated-grant is unconfigured on this deployment
// (app.grant.Source == nil, initializeDelegatedGrant's degrade path,
// mirroring every other WHAGENT_GRANT_*-gated behavior in this binary); no
// WHAGENT_UI_DEFAULT_DOMAIN is configured; the operator is not resolvable
// (mcpauth's own handleAuthorize already redirects an unauthenticated
// request to SignInURL, so this gate only ever adds a step for an operator
// who IS already resolvable); or the operator already holds an active
// grant for the default domain.
func (app *App) authorizeConsentGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/authorize" {
			next.ServeHTTP(w, r)
			return
		}
		if app.defaultDomain == "" || app.grant.Source == nil || app.grant.Store == nil {
			next.ServeHTTP(w, r)
			return
		}

		user, err := app.auth.CurrentUser(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		subject, err := mcpidentity.Encode(app.oidcIssuer, user.Sub)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		grant, err := grantkey.ForDomain(app.defaultDomain)
		if err != nil {
			logging.Get("main").Error("authorizeConsentGate: WHAGENT_UI_DEFAULT_DOMAIN is malformed", "domain", app.defaultDomain, "error", err)
			next.ServeHTTP(w, r)
			return
		}

		grantStatus, err := app.grant.Store.Status(r.Context(), subject, grant)
		if err == nil && grantStatus == grpcauth.GrantStatusActive {
			next.ServeHTTP(w, r)
			return
		}

		// No active grant yet for the default domain (grpcauth.
		// ErrGrantNotFound, or a needs_reauth/revoked status) -- FR2/FR9:
		// consent before any credential is minted. Preserve the exact
		// original /authorize request so the operator lands back on it
		// once consent completes.
		app.redirectToConsent(w, r, app.defaultDomain, r.URL.RequestURI())
	})
}

// redirectToConsent 302s the browser to the standalone consent route for
// domain, carrying returnTo (the request to resume once consent completes)
// as a query parameter.
func (app *App) redirectToConsent(w http.ResponseWriter, r *http.Request, domain, returnTo string) {
	dest := url.URL{Path: "/mcp/consent"}
	q := dest.Query()
	q.Set("domain", domain)
	if returnTo != "" {
		q.Set("return_to", returnTo)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// handleMCPConsent renders the FR2/FR6 consent page naming domain (query
// param `domain`, e.g. GET /mcp/consent?domain=audience_score_system) --
// the standalone consent entry point this task's Open Question resolved
// to. Registered behind app.auth.RequireAuthFunc (main.go), so an
// unauthenticated request is already redirected to sign-in before this
// handler ever runs.
func (app *App) handleMCPConsent(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")

	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if _, err := grantkey.ForDomain(domain); err != nil {
		http.Error(w, "invalid or missing domain", http.StatusBadRequest)
		return
	}
	returnTo := strings.TrimSpace(r.URL.Query().Get("return_to"))

	data := pages.MCPConsentData{
		Layout: components.LayoutData{
			Title: "Grant access",
			User:  htmxauth.GetUser(r.Context()),
		},
		Domain:   domain,
		ReturnTo: returnTo,
	}
	if err := RenderTempl(w, r, data.Layout.Title, pages.MCPConsent(data)); err != nil {
		logger.Error("failed to render mcp consent page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleMCPConsentConfirm is the consent page's POST /mcp/consent submit:
// starts DelegatedGrantSource.BeginAuthorization for (subject, domain),
// persists the resulting PendingAuthorization (see pendingConsent above),
// and redirects the browser to Keycloak's authorization endpoint.
func (app *App) handleMCPConsentConfirm(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	domain := strings.TrimSpace(r.FormValue("domain"))
	returnTo := strings.TrimSpace(r.FormValue("return_to"))

	grant, err := grantkey.ForDomain(domain)
	if err != nil {
		http.Error(w, "invalid or missing domain", http.StatusBadRequest)
		return
	}
	if app.grant.Source == nil {
		http.Error(w, "delegated-grant consent is not configured on this deployment", http.StatusServiceUnavailable)
		return
	}

	user, err := app.auth.CurrentUser(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	subject, err := mcpidentity.Encode(app.oidcIssuer, user.Sub)
	if err != nil {
		logger.Error("handleMCPConsentConfirm: failed to encode subject identity", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	authURL, pending, err := app.grant.Source.BeginAuthorization(ctx, subject, grant)
	if err != nil {
		logger.Error("handleMCPConsentConfirm: BeginAuthorization failed", "domain", domain, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if err := app.savePendingConsent(w, r, pendingConsent{
		PendingAuthorization: pending,
		Domain:               domain,
		ReturnTo:             returnTo,
	}); err != nil {
		logger.Error("handleMCPConsentConfirm: failed to persist pending authorization", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleMCPConsentCallback is Keycloak's redirect target
// (WHAGENT_GRANT_REDIRECT_URI must point at GET /mcp/consent/callback):
// verifies the callback, calls CompleteAuthorization, and on success
// records the FR12 grant-bookkeeping index entry before returning the
// operator to pending.ReturnTo.
//
// No credential is minted, and nothing is persisted, unless
// CompleteAuthorization itself succeeds (FR2's "before minting any
// credential" / "no credential is minted" on decline or failure).
func (app *App) handleMCPConsentCallback(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	pending, err := app.loadPendingConsent(r)
	if err != nil {
		// Expected control flow (an expired, already-used, or forged
		// callback) -- not an ERROR (AGENTS.md "Logging Levels").
		logger.Info("mcp consent callback with no usable pending authorization", "error", err)
		app.renderConsentFailure(w, r, "", "This consent link has expired or was already used. Please start over.")
		return
	}
	app.clearPendingConsent(w, r)

	user, err := app.auth.CurrentUser(r)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	subject, err := mcpidentity.Encode(app.oidcIssuer, user.Sub)
	if err != nil || subject != pending.Subject {
		// Bound-to-session check (this task's Open Question note): a
		// pending authorization only ever completes under the exact ui
		// session that started it -- never under a different signed-in
		// operator's session, whatever the stored (tamper-proof, but not
		// on its own session-bound) PendingAuthorization itself claims.
		logger.Info("mcp consent callback subject does not match the signed-in session; refusing", "domain", pending.Domain)
		app.renderConsentFailure(w, r, pending.Domain, "This consent link does not belong to your signed-in session.")
		return
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		// A decline or Keycloak-side failure is expected control flow, not
		// an ERROR (AGENTS.md "Logging Levels").
		logger.Info("mcp consent declined or failed at Keycloak", "domain", pending.Domain, "error", errParam)
		app.renderConsentFailure(w, r, pending.Domain, "Consent was not completed: "+errParam)
		return
	}

	if err := app.grant.Source.CompleteAuthorization(ctx, pending.PendingAuthorization, r.URL.Query().Get("state"), r.URL.Query().Get("code")); err != nil {
		logger.Info("mcp consent CompleteAuthorization failed", "domain", pending.Domain, "error", err)
		app.renderConsentFailure(w, r, pending.Domain, "Consent could not be completed. Please try again.")
		return
	}

	app.recordConsentBookkeeping(ctx, pending)

	logger.Info("delegated grant consented", "domain", pending.Domain)

	returnTo := pending.ReturnTo
	if returnTo == "" || !strings.HasPrefix(returnTo, "/") {
		// Never redirect to an attacker- or cookie-supplied absolute URL
		// (open-redirect prevention) -- only a same-origin, absolute path
		// is honored; anything else falls back to the authenticated
		// landing page.
		returnTo = "/sessions"
	}
	http.Redirect(w, r, returnTo, http.StatusFound)
}

// recordConsentBookkeeping writes the FR12 grant-bookkeeping index entry
// for a just-completed, successful consent. preferred_username is only
// resolvable for the consenting operator's own request, right now
// (grantindex.Entry's doc comment) -- captured here, never looked up
// later. Failure here is logged but does not undo or fail the
// already-successful consent (the delegated grant itself, via
// CompleteAuthorization's Store.Persist, is already durable) -- a missing
// bookkeeping row degrades FR14/FR16's display, not access.
func (app *App) recordConsentBookkeeping(ctx context.Context, pending pendingConsent) {
	logger := logging.Get("main")

	if app.grant.Index == nil {
		return
	}

	iss, sub, err := mcpidentity.Decode(pending.Subject)
	if err != nil {
		logger.Error("mcp consent: failed to decode subject for bookkeeping index", "error", err)
		return
	}

	preferredUsername := ""
	if info := htmxauth.GetUser(ctx); info != nil {
		preferredUsername = info.PreferredUsername
	}

	if err := app.grant.Index.Record(ctx, grantindex.Entry{
		SubjectIss:        iss,
		SubjectSub:        sub,
		Domain:            pending.Domain,
		PreferredUsername: preferredUsername,
	}); err != nil {
		logger.Error("mcp consent: failed to record grant-bookkeeping index entry", "domain", pending.Domain, "error", err)
	}
}

// renderConsentFailure renders FR2's "clear failure page" for a declined or
// failed consent. domain may be empty (e.g. no pending authorization was
// even found) -- MCPConsent's "Try again" link degrades to a bare
// "/mcp/consent" in that case, which handleMCPConsent then 400s until the
// operator is redirected here again with a real domain.
func (app *App) renderConsentFailure(w http.ResponseWriter, r *http.Request, domain, message string) {
	logger := logging.Get("main")
	data := pages.MCPConsentData{
		Layout: components.LayoutData{
			Title: "Grant access",
			User:  htmxauth.GetUser(r.Context()),
		},
		Domain:  domain,
		Failure: message,
	}
	if err := RenderTempl(w, r, data.Layout.Title, pages.MCPConsent(data)); err != nil {
		logger.Error("failed to render mcp consent failure page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
