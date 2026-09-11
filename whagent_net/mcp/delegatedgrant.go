package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/whagent_net/delegatedgrant"
)

// initializeDelegatedGrant builds `mcp`'s share of the single shared
// DelegatedGrantSource/Store/Index triple (issue #2426, FR10/FR13/NFR5/
// NFR6 of plan #2421) against pool -- the same *pgxpool.Pool
// initializeAuthDeps already opened for the FR9 mcpauth.CredentialStore,
// when PG_DATABASE_URL is set; pool is nil (and this function is not
// called) otherwise. Construction is non-fatal when the feature is
// entirely unconfigured (delegatedgrant.ErrNotConfigured --
// whagent_net/Tiltfile's local-dev default, ../ENV.md's "`mcp` server"
// section) but fatal for any other error, including a *partially*
// configured client -- see delegatedgrant.Build's doc comment for why.
//
// The Components this builds are what issue #2430's Implementation phase
// wires into every ../mcp/tools RegisterXxx call as GrantSource
// (grant.go): `mcp`'s per-call token acquisition now goes exclusively
// through Components.Source.TokenSource(subject, grant).Token(ctx) at
// tool-dispatch time -- the RFC 8693 impersonation-exchange mechanism it
// replaced is deleted outright (its own former file, package server),
// not left dormant (FR19).
func initializeDelegatedGrant(ctx context.Context, cfg config, pool *pgxpool.Pool, logger *slog.Logger) (delegatedgrant.Components, error) {
	if pool == nil {
		logger.Warn("PG_DATABASE_URL not set; delegated-grant client unavailable (not yet on any request path, issue #2426)")
		return delegatedgrant.Components{}, nil
	}

	grantCfg := delegatedgrant.Config{
		Issuer:              cfg.OIDCIssuer,
		ClientID:            cfg.GrantClientID,
		ClientSecret:        cfg.GrantClientSecret,
		RedirectURI:         cfg.GrantRedirectURI,
		EncryptionKeySecret: cfg.GrantEncryptionKey,
	}

	components, err := delegatedgrant.Build(ctx, grantCfg, pool)
	if err != nil {
		if errors.Is(err, delegatedgrant.ErrNotConfigured) {
			logger.Warn("WHAGENT_GRANT_*/WHAGENT_OIDC_ISSUER not configured; delegated-grant client unavailable (not yet on any request path, issue #2426)", "config", grantCfg)
			return delegatedgrant.Components{}, nil
		}
		return delegatedgrant.Components{}, err
	}

	logger.Info("delegated-grant client constructed (FR10/FR13, not yet on any request path)", "config", grantCfg)
	return components, nil
}
