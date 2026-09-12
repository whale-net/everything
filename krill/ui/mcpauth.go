package main

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/krill/identity"
	"github.com/whale-net/everything/libs/go/mcpauth"
)

// mcpCallerResolver adapts `ui`'s existing Keycloak sign-in session
// (app.auth, //libs/go/htmxauth) to mcpauth.CallerResolver so mcpauth's
// `/authorize` endpoint (mounted on `ui`, see setupMCPAuth below) can
// resolve the already-signed-in operator without ever rendering a login
// form or collecting credentials itself -- mirrors
// whagent_net/ui/mcpauth.go's mcpCallerResolver exactly in shape.
//
// The resolved identity is the operator's (iss, sub) pair, packed by
// //krill/identity into the single opaque string
// mcpauth.CredentialStore/AuthCodeStore store -- krill has no person/user
// table to key it to instead (NFR1 authorizes by persona, never by
// individual identity; see krill/mcp/server/auth.go's doc comment).
//
// app.auth.CurrentUser reads app.auth's DB-backed session directly, so a
// missing, tampered, or expired session surfaces here as "not resolved",
// exactly like RequireAuth's own session check.
//
// In AuthModeNone (local dev, no Keycloak), CurrentUser returns the fixed
// dev user but cfg.OIDCIssuer is unset, so identity.Encode fails on the
// empty iss and this resolver reports not-resolved -- deliberately: there
// is no real (iss, sub) pair to reflect without a configured issuer. The
// browser OAuth2 flow this resolver backs is only exercised against a
// real Keycloak realm (AuthModeOIDC).
func (app *App) mcpCallerResolver() mcpauth.CallerResolverFunc {
	return func(r *http.Request) (string, bool) {
		user, err := app.auth.CurrentUser(r)
		if err != nil {
			return "", false
		}

		// app.oidcIssuer (cfg.OIDCIssuer verbatim) rather than any
		// per-token claim -- the operator only ever signs in against this
		// one configured issuer.
		id, err := identity.Encode(app.oidcIssuer, user.Sub)
		if err != nil {
			return "", false
		}
		return id, true
	}
}

// setupMCPAuth constructs mcpauth's OAuth2 authorization-server front end
// -- RFC 9728/8414 discovery metadata, RFC 7591 dynamic client
// registration, and the authorization-code + PKCE `/authorize`/`/token`
// endpoints -- against the mcp_credential/mcp_oauth_client/mcp_auth_code
// tables migration 006_mcpauth_credential creates.
//
// Client registrations and pending authorization codes use the
// Postgres-backed ClientRegistry/AuthCodeStore, not mcpauth's in-memory
// defaults -- `/authorize`, `/token`, and `/register` can land on
// different `ui` replicas.
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
		ResourceName: "krill MCP",
		Resolver:     resolver,
		Credentials:  credentials,
		Clients:      clients,
		AuthCodes:    authCodes,
		SignInURL:    "/login",
	})
}
