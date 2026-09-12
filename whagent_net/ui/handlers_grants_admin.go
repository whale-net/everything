// FR14/FR15/NFR3 (issue #2433, plan #2421): the admin all-operators grant
// list and revoke page. GET /admin/grants lists every operator's
// delegated grants (grantindex.ListAll, #2425) with a live per-scope
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
// admin-offboarding story this plan exists for. isGrantsAdmin below
// instead gates on the roles carried by app.auth.GetAccessToken(r)'s
// result (htmxauth.DBSessionManager.GetAccessToken, which does refresh
// against Keycloak's token endpoint), so a revoked admin role stops
// working on the very next request.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantindex"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/grantkey"
	"github.com/whale-net/everything/whagent_net/ui/components"
	"github.com/whale-net/everything/whagent_net/ui/pages"
)

// devModeAccessToken is the literal sentinel htmxauth.Authenticator.
// GetAccessToken returns in AuthModeNone (see that method's own doc
// comment) -- never a real JWT. isGrantsAdmin branches on it to mirror
// RequireAuth's own AuthModeNone dev user (htmxauth.AllRoles, "treated as
// holding all roles") without reading htmxauth.GetUser(ctx).Roles, which
// this gate must never consult (see this file's package doc comment).
const devModeAccessToken = "dev-token"

// accessTokenRealmClaims is the one claim isGrantsAdmin needs out of a raw
// Keycloak access token JWT.
type accessTokenRealmClaims struct {
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// rolesFromAccessToken decodes (never verifies) an access token JWT's
// realm_access.roles claim.
//
// This deliberately does not verify the token's signature, mirroring
// //libs/go/grpcauth/delegatedgrant_authcode.go's subjectFromAccessToken:
// the token is not arriving here over an untrusted path -- it is exactly
// what app.auth.GetAccessToken(r) just read back from this operator's own
// signed-in session (refreshed against Keycloak's token endpoint when
// stale), never a bearer credential presented by some other caller. Do
// not reuse this helper for a token arriving over an untrusted path (e.g.
// an Authorization header on an inbound request) -- that needs a real
// verifying TokenVerifier.
func rolesFromAccessToken(token string) ([]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode access token payload: %w", err)
	}
	var claims accessTokenRealmClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse access token claims: %w", err)
	}
	return claims.RealmAccess.Roles, nil
}

// accessTokenReader is the one method of *htmxauth.Authenticator
// adminRoleGranted needs (NFR3's fresh-token accessor) -- declared
// locally, mirroring ../handlers_grants.go's grantIndexLister, so a test
// can drive adminRoleGranted against a hand-built access token without
// standing up a real Authenticator/session store behind a full OIDC
// sign-in.
type accessTokenReader interface {
	GetAccessToken(r *http.Request) (string, error)
}

var _ accessTokenReader = (*htmxauth.Authenticator)(nil)

// adminRoleGranted is FR15/NFR3's fresh-role gate: true only if the roles
// carried by tokens.GetAccessToken(r)'s current result (refreshed against
// Keycloak, never htmxauth.GetUser(ctx).Roles's 24h-cached snapshot)
// include adminRole. An empty adminRole (unconfigured deployment) never
// matches anything -- there is no "everyone is admin" default. Broken out
// from isGrantsAdmin so a test can exercise the role-decision itself
// (including the NFR3 stale-role case: a hand-built access token that
// omits the admin role) without any HTTP/session plumbing.
func adminRoleGranted(tokens accessTokenReader, adminRole string, r *http.Request) (bool, error) {
	token, err := tokens.GetAccessToken(r)
	if err != nil {
		return false, err
	}
	if token == devModeAccessToken {
		// AuthModeNone: RequireAuth already treats this session as
		// holding every role (htmxauth.AllRoles) -- mirror that here
		// rather than trying to parse a non-JWT sentinel as one.
		return true, nil
	}
	if adminRole == "" {
		return false, nil
	}
	roles, err := rolesFromAccessToken(token)
	if err != nil {
		return false, err
	}
	for _, role := range roles {
		if role == adminRole {
			return true, nil
		}
	}
	return false, nil
}

// isGrantsAdmin applies adminRoleGranted against this App's real
// authenticator and configured admin role (see that function's doc
// comment for the gate itself).
func (app *App) isGrantsAdmin(r *http.Request) (bool, error) {
	return adminRoleGranted(app.auth, app.adminRole, r)
}

// grantIndexAllLister is the subset of *grantindex.Index buildAdminGrantRows
// needs, declared locally (mirrors ../handlers_grants.go's grantIndexLister)
// so a test can substitute an in-memory fake instead of a real Postgres pool.
type grantIndexAllLister interface {
	ListAll(ctx context.Context) ([]grantindex.Entry, error)
}

var _ grantIndexAllLister = (*grantindex.Index)(nil)

// buildAdminGrantRows lists every operator's grant-index entries
// (grantindex.ListAll, #2425) and reads each one's LIVE status from store
// (grpcauth.Store.Status, never grantindex -- FR12, mirrors
// ../handlers_grants.go's buildGrantRows). iss is always app.oidcIssuer --
// this UI's one configured Keycloak issuer, never a per-entry claim --
// consistent with buildGrantRows' and isSessionOwner's own precedent that
// there is exactly one issuer in play.
func buildAdminGrantRows(ctx context.Context, index grantIndexAllLister, store grpcauth.Store, iss string, logger *slog.Logger) ([]pages.GrantRow, error) {
	entries, err := index.ListAll(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]pages.GrantRow, 0, len(entries))
	for _, e := range entries {
		grantKey, err := grantkey.ForScope(e.Domain)
		if err != nil {
			// Mirrors buildGrantRows: refuse to guess a grant key for a
			// malformed scope rather than risk mis-scoping a status read.
			return nil, err
		}

		subjectKey, err := grantSubjectKey(iss, e.SubjectSub)
		if err != nil {
			return nil, err
		}

		status, statusErr := store.Status(ctx, subjectKey, grantKey)
		display := grantUnknownStatus
		switch {
		case errors.Is(statusErr, grpcauth.ErrGrantNotFound):
			// The index and the store are never synced on write (FR12) --
			// an index row with no matching store row is a drift/edge
			// case, not a hard failure (AGENTS.md "had to adjust to keep
			// going" -- WARNING, page still renders).
			logger.Warn("grant recorded in index has no matching store row", "scope", e.Domain, "subject_sub", e.SubjectSub)
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
			SubjectSub:    e.SubjectSub,
		})
	}
	return rows, nil
}

// revokeGrantAsAdmin performs FR14/FR17's single-(subject, grant)-pair
// revoke on behalf of an admin acting on some *other* operator's grant --
// unlike revokeGrant (../handlers_grants.go), targetSub is never the
// caller's own session subject. iss is always app.oidcIssuer (see
// buildAdminGrantRows' doc comment). Broken out from
// handleGrantsAdminRevoke so a test can drive multiple distinct operators
// (FR17's scoping test) without real signed-in sessions, and so the NFR4
// audit log has exactly one call site for this path.
func revokeGrantAsAdmin(ctx context.Context, store grpcauth.Store, iss, adminSub, targetSub, scope string, logger *slog.Logger) error {
	grantKey, err := grantkey.ForScope(scope)
	if err != nil {
		return err
	}
	subjectKey, err := grantSubjectKey(iss, targetSub)
	if err != nil {
		return err
	}
	if err := store.Revoke(ctx, subjectKey, grantKey); err != nil {
		return err
	}

	// NFR4: every completed admin revoke logs at INFO -- who revoked (the
	// admin), whose grant, which scope. Never logs token material.
	logger.Info("delegated grant revoked by admin", "revoked_by_sub", adminSub, "subject_iss", iss, "subject_sub", targetSub, "scope", scope)
	return nil
}

// handleGrantsAdmin is GET /admin/grants (FR14, issue #2433): gated on
// isGrantsAdmin (FR15/NFR3) -- a non-admin gets 403, never a silently-
// empty list -- then lists every operator's grants
// (buildAdminGrantRows).
func (app *App) handleGrantsAdmin(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	isAdmin, err := app.isGrantsAdmin(r)
	if err != nil {
		logger.Error("failed to verify admin role", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !isAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	data := pages.GrantsAdminData{
		Layout: components.LayoutData{
			Title:  "All operator grants",
			Active: "Grants",
			User:   htmxauth.GetUser(ctx),
		},
	}

	if app.grant.Index == nil || app.grant.Store == nil {
		// WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER not configured on this
		// deployment (initializeDelegatedGrant's degrade path,
		// delegatedgrant.go) -- render the shell with an explanatory
		// error rather than a nil-pointer panic on Index/Store.
		data.Error = "Delegated-grant management is not configured on this deployment."
	} else {
		rows, err := buildAdminGrantRows(ctx, app.grant.Index, app.grant.Store, app.oidcIssuer, logger)
		if err != nil {
			logger.Error("failed to list grants", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		data.Rows = rows
	}

	if err := RenderTempl(w, r, data.Layout.Title, pages.GrantsAdmin(data)); err != nil {
		logger.Error("failed to render admin grants page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

// handleGrantsAdminRevoke is POST /admin/grants/revoke (FR14/FR17, issue
// #2433): gated on isGrantsAdmin (FR15/NFR3) -- a non-admin gets 403 --
// then revokes exactly the named (subject_sub, scope) pair, never the
// admin's own session subject (revokeGrantAsAdmin).
func (app *App) handleGrantsAdminRevoke(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	isAdmin, err := app.isGrantsAdmin(r)
	if err != nil {
		logger.Error("failed to verify admin role", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if !isAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
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
	subjectSub := strings.TrimSpace(r.FormValue("subject_sub"))
	if subjectSub == "" {
		http.Error(w, "subject_sub is required", http.StatusBadRequest)
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

	admin := htmxauth.GetUser(ctx)
	adminSub := ""
	if admin != nil {
		adminSub = admin.Sub
	}

	if err := revokeGrantAsAdmin(ctx, app.grant.Store, app.oidcIssuer, adminSub, subjectSub, scope, logger); err != nil {
		switch {
		case errors.Is(err, grpcauth.ErrGrantNotFound):
			http.Error(w, "grant not found", http.StatusNotFound)
		default:
			logger.Error("failed to revoke grant", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	http.Redirect(w, r, "/admin/grants", http.StatusSeeOther)
}
