package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/whagent_net/delegatedgrant"
)

// initializeDelegatedGrant builds `ui`'s share of the single shared
// DelegatedGrantSource/Store/Index triple (issue #2426, FR10/FR13/NFR5/
// NFR6 of plan #2421) against the same pool `ui` already opened for
// ui_sessions/mcp_credential (NewApp's caller). Construction is non-fatal
// when the feature is entirely unconfigured
// (delegatedgrant.ErrNotConfigured -- whagent_net/Tiltfile's local-dev
// default, mirroring WHAGENT_MCP_KEYCLOAK_*'s own degrade precedent,
// ../ENV.md's "`mcp` server" section) but fatal for any other error,
// including a *partially* configured client -- see
// delegatedgrant.Build's doc comment for why.
//
// Live as of issue #2430 (FR8/FR9): `ui`'s /authorize handler
// (handlers_consent.go) drives the per-scope consent flow through the
// Components this builds -- the opaque mcpauth-credential path it
// previously minted unconditionally is gone.
func initializeDelegatedGrant(ctx context.Context, cfg config, pool *pgxpool.Pool, logger *slog.Logger) (delegatedgrant.Components, error) {
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
			logger.Warn("WHAGENT_GRANT_* not configured; delegated-grant client unavailable (FR9's /authorize consent flow, issue #2430)", "config", grantCfg)
			return delegatedgrant.Components{}, nil
		}
		return delegatedgrant.Components{}, err
	}

	logger.Info("delegated-grant client constructed (FR10/FR13, live on FR9's /authorize consent flow, issue #2430)", "config", grantCfg)
	return components, nil
}
