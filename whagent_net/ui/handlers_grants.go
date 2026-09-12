// FR16/FR17 (issue #2432, plan #2421): the self-service per-scope grant
// list and revoke page. GET /grants lists the signed-in operator's own
// delegated grants (grantindex.ListBySubject, #2425) with a live per-
// scope status read (grpcauth.Store.Status, never grantindex -- FR12);
// POST /grants/revoke revokes exactly one (subject, grant) pair
// (grpcauth.Store.Revoke, FR17). Both handlers take the subject from the
// signed-in session (htmxauth.GetUser), never from a request parameter or
// form field.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/grantkey"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// grantIndexLister is the subset of *grantindex.Index (a concrete
// pgx-backed type with no interface of its own, unlike grpcauth.Store)
// buildGrantRows needs, declared locally so a test can substitute an
// in-memory fake instead of a real Postgres pool.
type grantIndexLister interface {
	ListBySubject(ctx context.Context, subjectIss, subjectSub string) ([]grantindex.Entry, error)
}

var _ grantIndexLister = (*grantindex.Index)(nil)

// grantUnknownStatus is the one GrantRow.Status value grpcauth.GrantStatus
// itself never produces -- see buildGrantRows' ErrGrantNotFound branch.
const grantUnknownStatus = "unknown"

// grantSubjectKey packs (iss, sub) into the single opaque string
// grpcauth.Store's "subject" parameter holds, reusing
// whagent_net/mcpidentity's existing (iss, sub) packing --
// whagent_net/ui/mcpauth.go's CallerResolver already encodes the exact
// same signed-in-operator pair the same way for mcpauth's Identity
// column. This is deliberate, not incidental: FR9's still-pending
// /authorize swap (issue #2421) must derive the identical subject key for
// BeginAuthorization/Persist that this page reads back via Status/Revoke,
// or a self-service revoke here would silently target a key
// TokenSource(...).Token(ctx) never actually looks up. Introducing a
// second (iss, sub) packing format for the same underlying pair, instead
// of reusing mcpidentity, is exactly the drift that package's own doc
// comment warns against.
func grantSubjectKey(iss, sub string) (string, error) {
	return mcpidentity.Encode(iss, sub)
}

// buildGrantRows lists iss/sub's own grant-index entries (grantindex.
// ListBySubject, #2425) and reads each one's LIVE status from store
// (grpcauth.Store.Status, never grantindex -- FR12: the index has no
// status column and is never cached or treated as a second source of
// truth for status). Broken out from handleGrants so a test can drive it
// against an in-memory fake index/store instead of a real Postgres pool,
// and so two distinct operators (FR16's scoping test) can be exercised
// without needing two real signed-in sessions.
//
// grantindex.Entry's Domain field keeps its library-defined name (that
// package is domain-neutral and configured, not renamed by this repo) --
// each entry's Domain value is this deployment's scope, carried into
// pages.GrantRow's own Scope field below.
func buildGrantRows(ctx context.Context, index grantIndexLister, store grpcauth.Store, iss, sub string, logger *slog.Logger) ([]pages.GrantRow, error) {
	entries, err := index.ListBySubject(ctx, iss, sub)
	if err != nil {
		return nil, err
	}

	subjectKey, err := grantSubjectKey(iss, sub)
	if err != nil {
		return nil, err
	}

	rows := make([]pages.GrantRow, 0, len(entries))
	for _, e := range entries {
		grantKey, err := grantkey.ForScope(e.Domain)
		if err != nil {
			// A malformed scope recorded in the index would mean some
			// earlier consent flow let an invalid scope through --
			// refuse to guess a grant key for it rather than risk
			// mis-scoping a status read (or, later, a revoke) against
			// the wrong key.
			return nil, err
		}

		status, statusErr := store.Status(ctx, subjectKey, grantKey)
		display := grantUnknownStatus
		switch {
		case errors.Is(statusErr, grpcauth.ErrGrantNotFound):
			// The index and the store are never synced on write (FR12) --
			// an index row with no matching store row is a drift/edge
			// case, not a hard failure. Surface it plainly (WARNING: the
			// page still renders, just with an "unknown" row, AGENTS.md's
			// "had to adjust to keep going") rather than failing the
			// whole page over one row.
			logger.Warn("grant recorded in index has no matching store row", "scope", e.Domain)
		case statusErr != nil:
			return nil, statusErr
		default:
			display = status.String()
		}

		rows = append(rows, pages.GrantRow{
			OperatorLabel: e.PreferredUsername,
			Scope:         e.Domain,
			Status:        display,
			GrantedAt:     e.GrantedAt,
		})
	}
	return rows, nil
}

// revokeGrant performs FR17's single-(subject, grant)-pair revoke:
// grantkey.ForScope(scope) validates/derives the grant key, then
// store.Revoke(ctx, subject, grantKey) unconditionally revokes exactly
// that pair -- grpcauth.Store's own contract guarantees no other
// (subject, grant) row is touched (libs/go/grpcauth/pgstore's Revoke doc
// comment). Broken out from handleGrantsRevoke so a test can drive two
// distinct operators (FR17's scoping test) without two real signed-in
// sessions, and so the NFR4 audit log has exactly one call site.
func revokeGrant(ctx context.Context, store grpcauth.Store, iss, sub, scope string, logger *slog.Logger) error {
	grantKey, err := grantkey.ForScope(scope)
	if err != nil {
		return err
	}
	subjectKey, err := grantSubjectKey(iss, sub)
	if err != nil {
		return err
	}
	if err := store.Revoke(ctx, subjectKey, grantKey); err != nil {
		return err
	}

	// NFR4: every completed revoke logs at INFO -- who revoked, whose
	// grant, which scope. This is a self-service revoke, so the revoker
	// and the grant's own subject are always the same operator (FR16/
	// FR17: subject is session-derived, never request-supplied). Never
	// logs token material.
	logger.Info("delegated grant revoked", "revoked_by_sub", sub, "subject_iss", iss, "subject_sub", sub, "scope", scope)
	return nil
}

// handleGrants is GET /grants (FR16, issue #2432): lists the signed-in
// operator's own grants, deriving iss from app.oidcIssuer (the UI's one
// configured Keycloak issuer, never a per-token claim -- mirrors
// isSessionOwner's own precedent, handlers_session.go) and sub from the
// session (htmxauth.GetUser), never from any request parameter.
func (app *App) handleGrants(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()
	user := htmxauth.GetUser(ctx)

	data := pages.GrantsData{
		Layout: components.LayoutData{
			Title:  "My grants",
			Active: "Grants",
			User:   user,
		},
	}

	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if app.grant.Index == nil || app.grant.Store == nil {
		// WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER not configured on this
		// deployment (initializeDelegatedGrant's degrade path,
		// delegatedgrant.go) -- render the shell with an explanatory
		// error rather than a nil-pointer panic on Index/Store.
		data.Error = "Delegated-grant management is not configured on this deployment."
	} else {
		rows, err := buildGrantRows(ctx, app.grant.Index, app.grant.Store, app.oidcIssuer, user.Sub, logger)
		if err != nil {
			logger.Error("failed to list grants", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		data.Rows = rows
	}

	if err := RenderTempl(w, r, data.Layout.Title, pages.Grants(data)); err != nil {
		logger.Error("failed to render grants page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleGrantsRevoke is POST /grants/revoke (FR17, issue #2432). subject
// is always app.oidcIssuer/htmxauth.GetUser(ctx).Sub -- the signed-in
// session's own (iss, sub) -- never read from the request body or query,
// however named: a tampered request supplying some other operator's
// subject in a form field is silently ignored (there is no code path that
// reads such a field at all), so the revoke can only ever target the
// caller's own grant.
func (app *App) handleGrantsRevoke(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()
	user := htmxauth.GetUser(ctx)

	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	scope := strings.TrimSpace(r.FormValue("scope"))
	if scope == "" {
		http.Error(w, "scope is required", http.StatusBadRequest)
		return
	}

	if app.grant.Store == nil {
		http.Error(w, "delegated-grant management is not configured on this deployment", http.StatusServiceUnavailable)
		return
	}

	if _, err := grantkey.ForScope(scope); err != nil {
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return
	}

	if err := revokeGrant(ctx, app.grant.Store, app.oidcIssuer, user.Sub, scope, logger); err != nil {
		switch {
		case errors.Is(err, grpcauth.ErrGrantNotFound):
			// Either a malformed scope (grantkey.ForScope rejected it)
			// or no persisted row for (this operator, this scope) --
			// including the case a tampered request named a scope/
			// subject combination that is not this operator's own. Never
			// reveals whether some *other* subject holds that grant.
			http.Error(w, "grant not found", http.StatusNotFound)
		default:
			logger.Error("failed to revoke grant", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	http.Redirect(w, r, "/grants", http.StatusSeeOther)
}
