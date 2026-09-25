package main

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/auth"
)

// mcpCallerResolver adapts `ui`'s existing Keycloak sign-in session
// (app.auth, //libs/go/htmxauth) to auth.CallerResolver so auth's
// `/authorize` endpoint (mounted on `ui`, see setupMCPAuth below) can
// resolve the already-signed-in operator without ever rendering a login
// form or collecting credentials itself -- mirrors
// whagent_net/ui/auth.go's mcpCallerResolver exactly in shape.
//
// The resolved identity is the operator's (iss, sub) pair, packed by
// //krill/identity into the single opaque string
// auth.CredentialStore/AuthCodeStore store -- krill has no person/user
// table to key it to instead (NFR1 authorizes by persona, never by
// individual identity; see krill/mcp/server/auth.go's doc comment).
//
// The pair itself is resolved by identity.go's operatorIdentity, shared
// with this binary's own app write path so both front doors attribute a
// caller to the exact same (iss, sub) values.
func (app *App) mcpCallerResolver() auth.CallerResolverFunc {
	return app.operatorEncodedIdentity
}

// setupMCPAuth constructs auth's OAuth2 authorization-server front end
// -- RFC 9728/8414 discovery metadata, RFC 7591 dynamic client
// registration, and the authorization-code + PKCE `/authorize`/`/token`
// endpoints -- against the mcp_credential/mcp_oauth_client/mcp_auth_code
// tables migration 006_mcpauth_credential creates.
//
// Client registrations and pending authorization codes use the
// Postgres-backed ClientRegistry/AuthCodeStore, not auth's in-memory
// defaults -- `/authorize`, `/token`, and `/register` can land on
// different `ui` replicas.
func setupMCPAuth(ctx context.Context, pool *pgxpool.Pool, cfg config, resolver auth.CallerResolverFunc) (*auth.Provider, error) {
	credentials, err := auth.NewCredentialStore(ctx, auth.StoreConfig{Pool: pool})
	if err != nil {
		return nil, err
	}

	clients, err := auth.NewPostgresClientRegistry(ctx, auth.ClientRegistryConfig{Pool: pool})
	if err != nil {
		return nil, err
	}

	authCodes, err := auth.NewPostgresAuthCodeStore(ctx, auth.AuthCodeStoreConfig{Pool: pool})
	if err != nil {
		return nil, err
	}

	return auth.NewProvider(auth.ProviderConfig{
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
