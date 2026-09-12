// Command ui is krill's barebones operator web UI: just enough of a shell
// (Keycloak sign-in + a landing page) to give //libs/go/mcpauth's
// `/authorize` endpoint (mounted here) somewhere to redirect a
// not-yet-signed-in caller, per ProviderConfig.SignInURL. This is what
// unblocks the mcpauth (human) front door on krill/mcp
// (krill/mcp/server/auth.go): before this binary existed, `/authorize`
// had no SignInURL configured and just 401ed on an unresolved caller (see
// ARCHITECTURE.md "krill/ui and the mcpauth front door").
//
// There is no session list, no spec-entity browsing, and no admin
// surface here -- krill's own PRODUCT.md roadmap defers a real web UI to
// "Later" (C19). This binary exists solely to make the OAuth2
// authorization-code + PKCE flow (discovery -> registration -> sign-in ->
// `/authorize` -> `/token`) actually completable, mirroring
// whagent_net/ui's own mcpauth wiring (whagent_net/ui/mcpauth.go)
// stripped down to that one job.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpauth"
)

// config holds `ui`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// Addr is the address this binary's HTTP surface listens on.
	Addr string

	// AuthMode is the HTTP-facing auth mode: "none" (dev-only, synthetic
	// dev-user -- see htmxauth.AuthModeNone) or "oidc" (real Keycloak
	// sign-in).
	AuthMode string

	// OIDC configuration (required when AuthMode == "oidc").
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	// SessionSecret encrypts the DB-backed session store's access/refresh
	// tokens (htmxauth.NewDBSessionManager).
	SessionSecret string

	// DatabaseURL backs both htmxauth's DB-backed session manager
	// (ui_sessions table, migration 007) and mcpauth's Postgres-backed
	// credential/client/auth-code stores (mcp_credential/mcp_oauth_client/
	// mcp_auth_code, migration 006) -- always required, never falls back
	// to cookie-only sessions, mirroring whagent_net/ui/main.go's config.
	DatabaseURL string

	// UIPublicURL is this binary's own externally-reachable base URL --
	// mcpauth.ProviderConfig.Issuer, the base every mcpauth endpoint URL
	// (`/authorize`, `/token`, `/register`,
	// `/.well-known/oauth-authorization-server`) is built from.
	UIPublicURL string

	// MCPPublicURL is `mcp`'s own externally-reachable base URL --
	// mcpauth.ProviderConfig.Resource, the OAuth2 `resource` identifier.
	// Must be byte-identical to what `mcp` itself advertises
	// (KRILL_MCP_PUBLIC_URL, see krill/mcp/main.go) -- a mismatch breaks
	// an MCP client's RFC 9728 discovery chain.
	MCPPublicURL string
}

func loadConfig() config {
	return config{
		Addr:             getEnv("KRILL_UI_ADDR", ":8080"),
		AuthMode:         strings.ToLower(getEnv("AUTH_MODE", "none")),
		OIDCIssuer:       getEnv("KRILL_OIDC_ISSUER", ""),
		OIDCClientID:     getEnv("KRILL_OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getEnv("KRILL_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getEnv("KRILL_OIDC_REDIRECT_URI", "http://localhost:8080/auth/callback"),
		SessionSecret:    getEnv("SECRET_KEY", "dev-secret-key-change-in-production"),
		DatabaseURL:      getEnv("PG_DATABASE_URL", ""),
		UIPublicURL:      getEnv("KRILL_UI_PUBLIC_URL", ""),
		MCPPublicURL:     getEnv("KRILL_MCP_PUBLIC_URL", ""),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// App holds this binary's application state.
type App struct {
	auth *htmxauth.Authenticator

	// oidcIssuer is cfg.OIDCIssuer verbatim -- the fixed issuer every
	// signed-in operator's encoded identity carries (mcpauth.go's
	// mcpCallerResolver).
	oidcIssuer string

	// mcpProvider is mcpauth's OAuth2 authorization-server front end,
	// constructed in NewApp and mounted on this binary's mux in
	// setupRoutes on unauthenticated routes (discovery metadata and
	// dynamic client registration must be reachable before an MCP client
	// has any credential at all). Its Resolver reads this binary's own
	// Keycloak session (mcpCallerResolver, mcpauth.go) -- `/authorize`
	// mints a credential only once the operator is already signed in via
	// app.auth.
	mcpProvider *mcpauth.Provider
}

// NewApp wires up Keycloak sign-in and the mcpauth OAuth2 provider. A
// failure here is always a startup-fatal condition -- see run()'s
// logger.Error call at the call site -- never a degrade-and-serve path.
func NewApp(ctx context.Context, cfg config) (*App, error) {
	var authMode htmxauth.AuthMode
	switch cfg.AuthMode {
	case "none", "":
		authMode = htmxauth.AuthModeNone
	case "oidc":
		authMode = htmxauth.AuthModeOIDC
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE: %s (must be 'none' or 'oidc')", cfg.AuthMode)
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("PG_DATABASE_URL is required: krill-ui always uses DB-backed sessions and never falls back to cookie sessions")
	}
	if cfg.UIPublicURL == "" {
		return nil, fmt.Errorf("KRILL_UI_PUBLIC_URL is required")
	}
	if cfg.MCPPublicURL == "" {
		return nil, fmt.Errorf("KRILL_MCP_PUBLIC_URL is required")
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session DB: %w", err)
	}

	// NewDBSessionManager probes the ui_sessions table before returning; a
	// missing table (migration 007) fails boot here rather than at the
	// first sign-in.
	store, err := htmxauth.NewDBSessionManager(ctx, pool, cfg.SessionSecret, "krill_ui_session")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize session store: %w", err)
	}

	authConfig := htmxauth.Config{
		Mode:             authMode,
		SessionSecret:    cfg.SessionSecret,
		SessionName:      "krill_ui_session",
		OIDCIssuer:       cfg.OIDCIssuer,
		OIDCClientID:     cfg.OIDCClientID,
		OIDCClientSecret: cfg.OIDCClientSecret,
		OIDCRedirectURL:  cfg.OIDCRedirectURL,
	}

	// initOIDC (inside NewAuthenticatorWithDB, oidc.NewProvider) performs
	// Keycloak discovery -- a failure here means the UI cannot start at
	// all, so the caller logs it at ERROR (AGENTS.md "Logging Levels").
	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, authConfig, store)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator (keycloak discovery): %w", err)
	}

	app := &App{
		auth:       auth,
		oidcIssuer: cfg.OIDCIssuer,
	}

	// mcpauth.NewCredentialStore/NewPostgresClientRegistry/
	// NewPostgresAuthCodeStore each preflight their own table (migration
	// 006) and fail loudly, naming the table, if it hasn't been applied
	// yet -- exactly like htmxauth.NewDBSessionManager's ui_sessions probe
	// above.
	mcpProvider, err := setupMCPAuth(ctx, pool, cfg, app.mcpCallerResolver())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize mcpauth provider: %w", err)
	}
	app.mcpProvider = mcpProvider

	return app, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := loadConfig()

	logging.Configure(logging.Config{
		ServiceName:   "krill-ui",
		Domain:        "krill",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	if cfg.AuthMode == "none" || cfg.AuthMode == "" {
		logger.Warn("AUTH_MODE=none — krill-ui is running without Keycloak authentication (development only)")
	} else {
		logger.Info("running in oidc mode")
	}

	app, err := NewApp(ctx, cfg)
	if err != nil {
		logger.Error("failed to initialize application", "error", err)
		return err
	}

	mux := http.NewServeMux()
	app.setupRoutes(mux)

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(mux, "krill-ui"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed { //nolint:errorlint // net/http documents this exact sentinel
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown signal received, draining in-flight requests")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(drainCtx); err != nil {
		logger.Warn("graceful shutdown did not complete cleanly", "error", err)
	}
	return nil
}

// setupRoutes registers every route this binary serves. "/healthz" is the
// only unauthenticated app route; "/login", "/auth/callback", and
// "/logout" are the Keycloak sign-in flow's own public routes; every
// other app route requires a signed-in operator.
//
// "/login" is this binary's chosen route name, but
// libs/go/htmxauth.Authenticator's RequireAuth/WithAccessToken hardcode
// their own unauthenticated-redirect target to "/auth/login" (not
// configurable), so "/auth/login" is registered as an alias for the exact
// same handler rather than moved or duplicated in logic (mirrors
// whagent_net/ui/main.go's setupRoutes).
func (app *App) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)

	mux.HandleFunc("/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin) // alias: see doc comment above.
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/logout", app.auth.HandleLogout)

	// mcpauth's OAuth2 authorization-server endpoints (/authorize, /token,
	// /register, and both discovery metadata documents) are registered
	// directly on mux here, outside app.auth.RequireAuth -- discovery and
	// dynamic client registration must be reachable before an MCP client
	// has any credential at all; /authorize itself is where
	// app.mcpProvider's own Resolver + SignInURL gate access to a
	// signed-in operator, not RequireAuth.
	app.mcpProvider.Mount(mux)

	// The one shell page this barebones binary has: confirms sign-in
	// worked and links to where an MCP client would actually connect.
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.handleIndex))
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}

// handleIndex is the barebones authenticated landing page -- just enough
// shell to prove a sign-in round-tripped correctly. No session list, no
// spec browsing: see this file's package doc comment for why.
func (app *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	user := htmxauth.GetUser(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>krill</title></head>
<body>
<h1>krill</h1>
<p>Signed in as %s.</p>
<p><a href="/logout">Sign out</a></p>
</body>
</html>`, htmlEscape(user.PreferredUsername))
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return replacer.Replace(s)
}
