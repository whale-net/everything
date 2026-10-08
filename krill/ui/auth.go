package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/auth"
)

// mcpCallerResolver adapts ui's Keycloak sign-in session to auth.CallerResolver so
// /authorize resolves the signed-in operator's (iss, sub) identity and persona
// without rendering a login form; callers holding neither role are refused.
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

// setupMCPAuth builds the OAuth2 authorization-server front end on Postgres-backed
// stores, since /authorize, /token and /register can land on different replicas.
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
