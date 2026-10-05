package main

import (
	"context"
	"errors"
	"net/http"

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
//
// It also implements auth.PersonaCaller: the persona is resolved from the
// verified session's realm_access.roles, and an identity holding neither
// configured role is refused -- /authorize mints no credential for it.
// Under AUTH_MODE=none both halves resolve to the dev operator, the same
// pair and persona api resolves for its own dev token.
func (app *App) mcpCallerResolver() auth.CallerResolver {
	return mcpResolver{app: app}
}

type mcpResolver struct{ app *App }

func (m mcpResolver) ResolveCaller(r *http.Request) (string, bool) {
	return m.app.operatorEncodedIdentity(r)
}

func (m mcpResolver) ResolveCallerPersona(r *http.Request) (string, bool) {
	persona, ok := m.app.operatorPersona(r)
	return string(persona), ok
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
//
// NameColumn is set because the credentials page lists a Name per
// credential (migration 039 adds the column); the returned store is
// narrowed to auth.NamedCredentialStore, since Mint -- what
// /authorize's authorization-code path and the self-serve JSON API use --
// leaves the name NULL exactly as before.
func setupMCPAuth(ctx context.Context, pool *pgxpool.Pool, cfg config, resolver auth.CallerResolver) (*auth.Provider, auth.NamedCredentialStore, error) {
	provider, credentials, err := auth.NewPostgresProvider(ctx, auth.PostgresProviderConfig{
		Pool:         pool,
		Issuer:       cfg.UIPublicURL,
		Resource:     cfg.MCPPublicURL,
		ResourceName: "krill MCP",
		Resolver:     resolver,
		Credentials:  auth.StoreConfig{PersonaColumn: "persona", NameColumn: "name"},
	})
	if err != nil {
		return nil, nil, err
	}
	named, ok := credentials.(auth.NamedCredentialStore)
	if !ok {
		return nil, nil, errors.New("krill/ui: MCP credential store does not implement auth.NamedCredentialStore — apply migration 039 (mcp_credential.name) and set StoreConfig.NameColumn")
	}
	return provider, named, nil
}
