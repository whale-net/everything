package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/whagent_net/mcpidentity"
)

// mcpCallerResolver adapts `ui`'s existing Keycloak sign-in session
// (app.auth, //libs/go/htmxauth) to mcpauth.CallerResolver (FR9, issue
// #2245) so mcpauth's `/authorize` endpoint (mounted on `ui`, see
// setupMCPAuth below) can resolve the already-signed-in operator without
// ever rendering a login form or collecting credentials itself --
// mirrors audience_score_system/web/auth.Authenticator.MCPCallerResolver
// (audience_score_system/web/auth/mcpauth.go) exactly in shape, with a
// different resolved identity (see below).
//
// NFR7's hard constraint is that the resolved identity is the operator's
// LB2 (iss, sub) pair -- the same pair whagent_net/session.Subject and
// sessions.subject_iss/subject_sub already use -- encoded as a single
// opaque string, never a new whagent-net-only user/person id. That
// encoding lives in whagent_net/mcpidentity, a small package importable
// by both `ui` (encodes, here) and `mcp` (decodes, a dependent task) so
// the two sides cannot drift on the packed string's format.
//
// app.auth.CurrentUser reads app.auth's DB-backed session directly
// (htmxauth.DBSessionManager.GetUserInfo -- a single `ui_sessions` SELECT
// gated on `expires_at > NOW()`) exactly like RequireAuth's own session
// check, so a missing, tampered, or expired session all surface here the
// same way RequireAuth would reject them: CurrentUser returns an error and
// this resolver reports not-resolved. Unlike WithAccessToken's
// GetAccessToken, GetUserInfo never refreshes against Keycloak's token
// endpoint -- see db_session.go's GetUserInfo vs. GetAccessToken -- so this
// performs no IdP call of any kind, satisfying resolver.go's
// CallerResolver contract.
//
// In AuthModeNone (local dev, no Keycloak), CurrentUser returns the fixed
// dev user but cfg.OIDCIssuer is unset, so mcpidentity.Encode fails on the
// empty iss and this resolver reports not-resolved -- deliberately: there
// is no real (iss, sub) pair to reflect without a configured issuer, and
// inventing one for dev mode would be exactly the kind of MCP-only
// identity NFR7 rules out. The browser OAuth2 flow this resolver backs is
// only exercised against a real Keycloak realm (AuthModeOIDC).
func (app *App) mcpCallerResolver() mcpauth.CallerResolverFunc {
	return func(r *http.Request) (string, bool) {
		user, err := app.auth.CurrentUser(r)
		if err != nil {
			return "", false
		}

		// app.oidcIssuer (cfg.OIDCIssuer verbatim) rather than any
		// per-token claim -- see its doc comment on App for why this
		// mirrors isSessionOwner's own choice; the operator only ever
		// signs in against this one configured issuer.
		identity, err := mcpidentity.Encode(app.oidcIssuer, user.Sub)
		if err != nil {
			return "", false
		}
		return identity, true
	}
}

// setupMCPAuth constructs mcpauth's OAuth2 authorization-server front end
// (FR9/C27) -- RFC 9728/8414 discovery metadata, RFC 7591 dynamic client
// registration, and the authorization-code + PKCE `/authorize`/`/token`
// endpoints -- against the mcp_credential/mcp_oauth_client/mcp_auth_code
// tables migration 004_mcpauth_credential creates (whagent_net/migrate).
//
// mcpauth.NewCredentialStore is left at its generic default
// (StoreConfig.IdentityColumn == "identity", a plain TEXT column) rather
// than the ASS-shaped person_id/UUID variant: NFR7 stores the operator's
// encoded (iss, sub) pair as an opaque string, not a foreign key into any
// whagent-net-only identity table (there is none). Client registrations
// and pending authorization codes use the Postgres-backed
// ClientRegistry/AuthCodeStore, not mcpauth's in-memory defaults --
// `/authorize`, `/token`, and `/register` can land on different `ui`
// replicas (libs/go/mcpauth/README.md "OAuth2 client registry" /
// "authorization-code + PKCE flow").
func setupMCPAuth(ctx context.Context, pool *pgxpool.Pool, cfg config, resolver mcpauth.CallerResolverFunc) (*mcpauth.Provider, error) {
	credentials, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: pool})
	if err != nil {
		return nil, err
	}

	clients, err := mcpauth.NewPostgresClientRegistry(ctx, mcpauth.ClientRegistryConfig{Pool: pool})
	if err != nil {
		return nil, err
	}

	authCodes, err := mcpauth.NewPostgresAuthCodeStore(ctx, mcpauth.AuthCodeStoreConfig{Pool: pool})
	if err != nil {
		return nil, err
	}

	return mcpauth.NewProvider(mcpauth.ProviderConfig{
		Issuer:       cfg.UIPublicURL,
		Resource:     cfg.MCPPublicURL,
		ResourceName: "whagent-net MCP",
		Resolver:     resolver,
		Credentials:  credentials,
		Clients:      clients,
		AuthCodes:    authCodes,
		SignInURL:    "/login",
	})
}
