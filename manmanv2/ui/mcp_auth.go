package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/auth"
	"github.com/whale-net/everything/libs/go/grpcauth/grantflow"
	"github.com/whale-net/everything/libs/go/whagentlink"
	"github.com/whale-net/everything/manmanv2/identitylink"
)

const (
	mcpConsentCallback  = "/mcp/consent/callback"
	mcpSignInPath       = "/auth/login"
	mcpGrantRedirectEnv = grantflow.EnvRedirectURI
)

// mountMCPAuth hosts the MCP's OAuth authorization server (/register,
// /authorize, /token) on this UI, exactly like krill and ASS: sign-in is the
// UI's own Keycloak session, and the credential it issues is verified by the
// MCP. A one-time Keycloak consent stores the user's offline grant so the MCP
// can call the control API as that user. Returns mux unchanged when
// MCP_PUBLIC_URL is unset.
func (app *App) mountMCPAuth(ctx context.Context, mux *http.ServeMux) (http.Handler, error) {
	cfg := app.config
	if cfg.MCPPublicURL == "" {
		return mux, nil
	}
	if app.pool == nil || cfg.UIPublicURL == "" || cfg.AuthMode != "oidc" {
		return nil, fmt.Errorf("MCP_PUBLIC_URL requires AUTH_MODE=oidc, PG_DATABASE_URL and UI_PUBLIC_URL")
	}
	grantCfg, ok := grantflow.ConfigFromEnv(func(k string) string { return os.Getenv(k) }, cfg.OIDCIssuer)
	if !ok {
		return nil, fmt.Errorf("MCP_PUBLIC_URL requires %s, %s, %s and %s", grantflow.EnvClientID, grantflow.EnvClientSecret, grantflow.EnvRedirectURI, grantflow.EnvEncryptionKey)
	}
	redirect, err := url.Parse(grantCfg.RedirectURI)
	if err != nil || !strings.HasSuffix(redirect.Path, mcpConsentCallback) {
		return nil, fmt.Errorf("%s must end in %s", mcpGrantRedirectEnv, mcpConsentCallback)
	}
	grants, err := grantflow.Build(ctx, grantCfg, app.pool)
	if err != nil {
		return nil, err
	}

	subject := func(r *http.Request) (string, bool) {
		user, err := app.auth.CurrentUser(r)
		if err != nil || user.Sub == "" {
			return "", false
		}
		return user.Sub, true
	}
	provider, _, err := auth.NewPostgresProvider(ctx, auth.PostgresProviderConfig{
		Pool:         app.pool,
		Issuer:       strings.TrimRight(cfg.UIPublicURL, "/"),
		Resource:     strings.TrimRight(cfg.MCPPublicURL, "/"),
		ResourceName: "manmanv2 MCP",
		Resolver:     auth.CallerResolverFunc(subject),
		SignInURL:    mcpSignInPath,
	})
	if err != nil {
		return nil, err
	}
	provider.Mount(mux)

	consent := &grantflow.Consent{
		Components:   grants,
		Grant:        grantflow.DefaultGrant,
		Subject:      subject,
		Secret:       cfg.SessionSecret,
		CallbackPath: mcpConsentCallback,
		SignInURL:    mcpSignInPath,
		// A link started from whagent-net resumes here after consent.
		ResumePrefixes: []string{linkCompletePath},
	}
	if err := app.mountWhagentLink(ctx, mux, consent); err != nil {
		return nil, err
	}
	log.Printf("MCP OAuth authorization server mounted (resource %s)", cfg.MCPPublicURL)
	return consent.Mount(mux), nil
}

// mountWhagentLink serves the whagent-net identity link and unlink endpoints. Disabled
// (no routes) unless both WHAGENT_UI_JWKS_URL and WHAGENT_UI_ISSUER are set.
func (app *App) mountWhagentLink(ctx context.Context, mux *http.ServeMux, consent *grantflow.Consent) error {
	cfg := app.config
	if cfg.WhagentUIJWKSURL == "" && cfg.WhagentUIIssuer == "" {
		return nil
	}
	if cfg.WhagentUIJWKSURL == "" || cfg.WhagentUIIssuer == "" {
		return fmt.Errorf("WHAGENT_UI_JWKS_URL and WHAGENT_UI_ISSUER must be set together")
	}
	verifier, err := whagentlink.NewVerifier(ctx, cfg.WhagentUIJWKSURL, cfg.WhagentUIIssuer)
	if err != nil {
		return err
	}
	h := &whagentLinkHandlers{
		verifier:   verifier,
		store:      identitylink.Store{DB: stdlib.OpenDBFromPool(app.pool)},
		grants:     consent,
		whagentURL: cfg.WhagentUIIssuer,
	}
	mux.HandleFunc("GET /link/whagent", app.auth.RequireAuthFunc(h.handleShow))
	mux.HandleFunc("POST /link/whagent/confirm", app.auth.RequireAuthFunc(h.handleConfirm))
	mux.HandleFunc("GET "+linkCompletePath, app.auth.RequireAuthFunc(h.handleComplete))
	mux.HandleFunc("GET /unlink/whagent", app.auth.RequireAuthFunc(h.handleUnlinkShow))
	mux.HandleFunc("POST /unlink/whagent/confirm", app.auth.RequireAuthFunc(h.handleUnlinkConfirm))
	log.Printf("whagent-net identity linking enabled (issuer %s)", cfg.WhagentUIIssuer)
	return nil
}
