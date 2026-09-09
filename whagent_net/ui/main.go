// Command ui is whagent-net's standalone agent web UI (M2, issue #2236):
// a Go html/template + templ + HTMX surface, distinct from `api`'s gRPC
// surface and `mcp`'s MCP surface. Every app route requires a signed-in
// Keycloak operator (NFR1) via //libs/go/htmxauth -- there is no
// whagent-net-specific login mechanism and no local user table -- and
// every outbound call to `api` forwards that operator's own access token
// (//libs/go/grpcauth), never a shared service account (mirrors `mcp`'s
// FR10 stance, ARCHITECTURE.md "Identity and auth chaining"). Real
// session pages (FR1-FR4) and the MCP OAuth2 provider (FR9) mount onto
// what this task builds; today this binary only serves a placeholder
// authenticated index page.
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
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
)

// config holds `ui`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md). Variable names mirror
// whagent-net's existing conventions rather than manmanv2/ui's bare
// OIDC_* names: WHAGENT_OIDC_ISSUER/WHAGENT_OIDC_CLIENT_ID/
// WHAGENT_OIDC_CLIENT_SECRET and WHAGENT_API_URL were already documented
// in ENV.md's "Identity" and "Service wiring" sections (for `ui`/`mcp`)
// before this task existed; AUTH_MODE/GRPC_AUTH_MODE/SECRET_KEY/
// PG_DATABASE_URL match manmanv2/ui's and `api`'s own literal names.
type config struct {
	// Addr is the address this binary's HTTP surface listens on.
	Addr string

	// AuthMode is the HTTP-facing auth mode: "none" (dev-only, synthetic
	// dev-user -- see htmxauth.AuthModeNone) or "oidc" (real Keycloak
	// sign-in, NFR1).
	AuthMode string

	// OIDC configuration (required when AuthMode == "oidc").
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	// SessionSecret encrypts the DB-backed session store's access/refresh
	// tokens (htmxauth.NewDBSessionManager).
	SessionSecret string

	// DatabaseURL backs htmxauth's DB-backed session manager (ui_sessions
	// table) -- always required, never falls back to cookie-only sessions,
	// mirroring tools/app_registry/ui's NewApp (the converged pattern,
	// #998 FR9 point 1's follow-on): a signed-in operator's access token
	// must be able to refresh, or every session silently breaks 5-15
	// minutes after sign-in. This is a distinct Postgres *table* from
	// whagent_net/session's domain tables (`sessions`, `transcript_event`,
	// ...) even though it shares the same PG_DATABASE_URL connection
	// string as every other whagent-net binary (ENV.md "Database") -- `ui`
	// never queries the domain tables directly, only through `api`'s
	// gRPC surface (ARCHITECTURE.md "every UI is a stateless reader").
	DatabaseURL string

	// APIAddr is `api`'s gRPC address (WHAGENT_API_URL, ../ENV.md
	// "Service wiring") -- the only outbound dependency this binary
	// dials, mirroring `mcp`'s own config.
	APIAddr string

	// GRPCAuthMode gates whether the operator's access token is actually
	// forwarded to `api` on outbound calls (grpcauth.NewUserTokenDialOption).
	// Should match `api`'s own GRPC_AUTH_MODE (ENV.md "`api` server").
	GRPCAuthMode string
}

func loadConfig() config {
	return config{
		Addr:             getEnv("WHAGENT_UI_ADDR", ":8080"),
		AuthMode:         strings.ToLower(getEnv("AUTH_MODE", "none")),
		OIDCIssuer:       getEnv("WHAGENT_OIDC_ISSUER", ""),
		OIDCClientID:     getEnv("WHAGENT_OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getEnv("WHAGENT_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getEnv("WHAGENT_OIDC_REDIRECT_URI", "http://localhost:8080/auth/callback"),
		SessionSecret:    getEnv("SECRET_KEY", "dev-secret-key-change-in-production"),
		DatabaseURL:      getEnv("PG_DATABASE_URL", ""),
		APIAddr:          getEnv("WHAGENT_API_URL", ""),
		GRPCAuthMode:     strings.ToLower(getEnv("GRPC_AUTH_MODE", "none")),
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
	auth    *htmxauth.Authenticator
	session *SessionClient
}

// NewApp wires up Keycloak sign-in (NFR1) and the authenticated `api`
// client. A failure here is always a startup-fatal condition -- see
// run()'s logger.Error call at the call site -- never a degrade-and-serve
// path, unlike e.g. the SSE hub some sibling UIs build optimistically.
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
		return nil, fmt.Errorf("PG_DATABASE_URL is required: whagent-net-ui always uses DB-backed sessions and never falls back to cookie sessions")
	}
	if cfg.APIAddr == "" {
		return nil, fmt.Errorf("WHAGENT_API_URL is required")
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session DB: %w", err)
	}

	// NewDBSessionManager probes the ui_sessions table before returning; a
	// missing table fails boot here rather than at the first sign-in.
	store, err := htmxauth.NewDBSessionManager(ctx, pool, cfg.SessionSecret, "whagent_net_ui_session")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize session store: %w", err)
	}

	authConfig := htmxauth.Config{
		Mode:             authMode,
		SessionSecret:    cfg.SessionSecret,
		SessionName:      "whagent_net_ui_session",
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

	// Forwards the signed-in operator's own access token on every `api`
	// call (grpcauth.WithUserToken, set by htmxauth.WithAccessToken) --
	// never a shared service account. See config.GRPCAuthMode's doc
	// comment.
	userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(cfg.GRPCAuthMode))
	sessionClient, err := NewSessionClient(ctx, cfg.APIAddr, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to api at %s: %w", cfg.APIAddr, err)
	}

	return &App{
		auth:    auth,
		session: sessionClient,
	}, nil
}

// Close releases this App's resources.
func (app *App) Close() error {
	if app.session != nil {
		return app.session.Close()
	}
	return nil
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
		ServiceName:   "whagent-net-ui",
		Domain:        "whagent-net",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	if cfg.AuthMode == "none" || cfg.AuthMode == "" {
		logger.Warn("AUTH_MODE=none — whagent-net-ui is running without Keycloak authentication (development only)")
	} else {
		logger.Info("running in oidc mode")
	}

	app, err := NewApp(ctx, cfg)
	if err != nil {
		logger.Error("failed to initialize application", "error", err)
		return err
	}
	defer app.Close() //nolint:errcheck

	mux := http.NewServeMux()
	app.setupRoutes(mux)

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(mux, "whagent-net-ui"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown: stop accepting new connections and drain
	// in-flight requests on SIGTERM/SIGINT instead of dropping them
	// (mirrors whagent_net/mcp/main.go's run()).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.Addr, "api_addr", cfg.APIAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
// only unauthenticated app route (used by the chart's health check and
// Tilt); "/login", "/auth/callback", and "/logout" are the Keycloak
// sign-in flow's own public routes (NFR1); every other route is wrapped
// in app.auth.RequireAuthFunc.
//
// "/login" is this task's chosen route name (see components/layout.templ's
// headerRight doc comment for the matching "/logout" decision) -- but
// libs/go/htmxauth.Authenticator's RequireAuth/WithAccessToken hardcode
// their own unauthenticated-redirect target to "/auth/login" (not
// configurable, see auth.go), so "/auth/login" is registered as an alias
// for the exact same handler rather than moved or duplicated in logic.
func (app *App) setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)

	mux.HandleFunc("/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin) // alias: see doc comment above.
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/logout", app.auth.HandleLogout)

	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleIndex)))
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}
