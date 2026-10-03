// Package grantflow is the shared plumbing for an MCP that calls a backend
// as the signed-in user while authenticating its own clients with the
// opaque credentials of libs/go/auth.
//
// The hosting UI runs the one-time Keycloak consent (Consent) that stores
// the user's offline grant; the MCP later turns a verified credential into
// a fresh user access token (Exchanger). Both halves share Components.
package grantflow

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcauth/pgstore"
)

// DefaultGrant is the grant key an MCP stores each user's grant under; the
// UI (Consent.Grant) and MCP (Exchanger.Grant) must agree on it.
const DefaultGrant = "mcp"

// Config is the Keycloak client and secret that protect stored grants. An
// app normally reuses its UI's existing confidential client: RedirectURI
// only needs adding to that client.
type Config struct {
	Issuer              string
	ClientID            string
	ClientSecret        string
	RedirectURI         string
	EncryptionKeySecret string
}

// Components is the grant source and store built from a Config.
type Components struct {
	Source *grpcauth.DelegatedGrantSource
	Store  grpcauth.Store
}

type deferredRevoker struct{ source *grpcauth.DelegatedGrantSource }

func (r *deferredRevoker) RevokeRefreshToken(ctx context.Context, refreshToken string) error {
	return r.source.RevokeRefreshToken(ctx, refreshToken)
}

// Build constructs Components over pool. The grpcauth_delegated_grant table
// must already exist (the app's migrations own it).
func Build(ctx context.Context, cfg Config, pool *pgxpool.Pool) (Components, error) {
	if cfg.EncryptionKeySecret == "" {
		return Components{}, errors.New("grantflow: EncryptionKeySecret is required")
	}
	encKey := sha256.Sum256([]byte(cfg.EncryptionKeySecret))

	revoker := &deferredRevoker{}
	store, err := pgstore.NewGrantStore(ctx, pgstore.StoreConfig{Pool: pool, EncryptionKey: encKey[:], Revoker: revoker})
	if err != nil {
		return Components{}, fmt.Errorf("grantflow: store: %w", err)
	}
	source, err := grpcauth.NewDelegatedGrantSource(ctx, grpcauth.DelegatedGrantConfig{
		Issuer:        cfg.Issuer,
		ClientID:      cfg.ClientID,
		ClientSecret:  cfg.ClientSecret,
		RedirectURI:   cfg.RedirectURI,
		Store:         store,
		EncryptionKey: encKey[:],
	})
	if err != nil {
		return Components{}, fmt.Errorf("grantflow: source: %w", err)
	}
	revoker.source = source
	return Components{Source: source, Store: store}, nil
}
