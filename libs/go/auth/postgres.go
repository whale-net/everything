package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresProviderConfig configures NewPostgresProvider: the one way an app
// builds its MCP OAuth2 authorization server.
type PostgresProviderConfig struct {
	Pool *pgxpool.Pool

	// Issuer is the public URL of the app hosting /authorize, /token and
	// /register (normally its UI); Resource is the MCP's public URL.
	Issuer       string
	Resource     string
	ResourceName string

	// Resolver reads the caller already signed in to the hosting app.
	Resolver CallerResolver
	// SignInURL is where /authorize sends an unauthenticated caller.
	SignInURL string

	// Credentials tunes the credential and auth-code stores (identity and
	// persona columns). Pool is filled in; AuthCodes inherits PersonaColumn.
	Credentials StoreConfig
}

// NewPostgresProvider builds a Provider backed by Postgres client, auth-code
// and credential stores (safe across replicas) and returns the credential
// store the MCP resource server verifies bearer tokens against.
func NewPostgresProvider(ctx context.Context, cfg PostgresProviderConfig) (*Provider, CredentialStore, error) {
	storeCfg := cfg.Credentials
	storeCfg.Pool = cfg.Pool
	credentials, err := NewCredentialStore(ctx, storeCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp credential store: %w", err)
	}
	clients, err := NewPostgresClientRegistry(ctx, ClientRegistryConfig{Pool: cfg.Pool})
	if err != nil {
		return nil, nil, fmt.Errorf("mcp client registry: %w", err)
	}
	authCodes, err := NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig{Pool: cfg.Pool, PersonaColumn: storeCfg.PersonaColumn})
	if err != nil {
		return nil, nil, fmt.Errorf("mcp auth code store: %w", err)
	}
	signIn := cfg.SignInURL
	if signIn == "" {
		signIn = "/login"
	}
	provider, err := NewProvider(ProviderConfig{
		Issuer:       cfg.Issuer,
		Resource:     cfg.Resource,
		ResourceName: cfg.ResourceName,
		Resolver:     cfg.Resolver,
		Credentials:  credentials,
		Clients:      clients,
		AuthCodes:    authCodes,
		SignInURL:    signIn,
	})
	if err != nil {
		return nil, nil, err
	}
	return provider, credentials, nil
}
