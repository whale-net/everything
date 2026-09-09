// Command ui is whagent-net's standalone agent web UI (M2, issue #2236):
// a Go html/template + templ + HTMX surface, distinct from `api`'s gRPC
// surface and `mcp`'s MCP surface. Every app route requires a signed-in
// Keycloak operator (NFR1) via //libs/go/htmxauth -- there is no
// whagent-net-specific login mechanism and no local user table -- and
// every outbound call to `api` forwards that operator's own access token
// (//libs/go/grpcauth), never a shared service account (mirrors `mcp`'s
// FR10 stance, ARCHITECTURE.md "Identity and auth chaining"). The session
// detail page and its live transcript over an htmxsse.Hub (FR2, issue
// #2242), the ownership-gated start/turn/stop lifecycle controls (FR1,
// issue #2246), and the filtered/paginated session list (FR3/C15, issue
// #2247) that is also the authenticated landing page, are the real pages
// so far; the usage panel (FR4) and the MCP OAuth2 provider (FR9) mount
// onto what this task builds in later tasks under plan #2233.
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
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpauth"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/whagent_net/events"
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

	// RabbitMQURL backs the live session detail page's htmxsse.Hub (FR2,
	// NFR2, issue #2242) -- ENV.md's "RabbitMQ (event bus)" section
	// already documents RABBITMQ_URL as applying to `ui`. Unset or an
	// unreachable broker must not fail boot (NFR2's degrade-and-retry
	// stance, mirrored from manmanv2/ui and tools/app_registry/ui's own
	// initializeSSEHub) -- see initializeSSEHub's doc comment.
	RabbitMQURL string

	// UIPublicURL is this binary's own externally-reachable base URL
	// (e.g. https://whagent.example.com) -- FR9/issue #2245's
	// mcpauth.ProviderConfig.Issuer, the base every mcpauth endpoint URL
	// `ui` advertises (`/authorize`, `/token`, `/register`,
	// `/.well-known/oauth-authorization-server`) is built from. Mirrors
	// audience_score_system's ASS_OAUTH_REDIRECT_BASE_URL doubling as
	// mcpauth's issuer (see audience_score_system/ENV.md).
	UIPublicURL string

	// MCPPublicURL is `mcp`'s own externally-reachable base URL -- FR9's
	// mcpauth.ProviderConfig.Resource, the OAuth2 `resource` identifier.
	// Must be byte-identical to what `mcp` itself advertises in its own
	// protected-resource metadata (mcp's dependent task, issue #2245's
	// Context section) -- a mismatch breaks an MCP client's RFC 9728
	// discovery chain.
	MCPPublicURL string
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
		RabbitMQURL:      getEnv("RABBITMQ_URL", ""),
		UIPublicURL:      getEnv("WHAGENT_UI_PUBLIC_URL", ""),
		MCPPublicURL:     getEnv("WHAGENT_MCP_PUBLIC_URL", ""),
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

	// oidcIssuer is the signed-in operator's iss for FR2's read-only
	// gating (isSessionOwner, handlers_session.go) -- cfg.OIDCIssuer
	// verbatim, never a per-token claim; see isSessionOwner's doc comment
	// for why.
	oidcIssuer string

	// sseHub backs the live session detail page (handlers_session_live.go,
	// FR2/NFR2). nil when RabbitMQURL is unset or the broker was
	// unreachable at startup (initializeSSEHub) -- handleSessionEvents
	// degrades to 503 in that case rather than the whole binary failing
	// to boot.
	sseHub *htmxsse.Hub

	// mcpProvider is mcpauth's OAuth2 authorization-server front end
	// (FR9/C27, issue #2245) -- constructed in NewApp, mounted on this
	// binary's mux in setupRoutes on unauthenticated routes (discovery
	// metadata and dynamic client registration must be reachable before
	// an MCP client has any credential at all). Its Resolver reads
	// `ui`'s own Keycloak session (mcpCallerResolver, mcpauth.go) --
	// `/authorize` mints a credential only once the operator is already
	// signed in via app.auth.
	mcpProvider *mcpauth.Provider
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
	if cfg.UIPublicURL == "" {
		return nil, fmt.Errorf("WHAGENT_UI_PUBLIC_URL is required")
	}
	if cfg.MCPPublicURL == "" {
		return nil, fmt.Errorf("WHAGENT_MCP_PUBLIC_URL is required")
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

	app := &App{
		auth:       auth,
		session:    sessionClient,
		oidcIssuer: cfg.OIDCIssuer,
		sseHub:     initializeSSEHub(cfg),
	}

	// mcpauth.NewCredentialStore/NewPostgresClientRegistry/
	// NewPostgresAuthCodeStore each preflight their own table (see
	// whagent_net/migrate/schema/migrations/004_mcpauth_credential) and
	// fail loudly, naming the table, if it hasn't been applied yet --
	// exactly like htmxauth.NewDBSessionManager's ui_sessions probe above.
	mcpProvider, err := setupMCPAuth(ctx, pool, cfg, app.mcpCallerResolver())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize mcpauth provider: %w", err)
	}
	app.mcpProvider = mcpProvider

	return app, nil
}

// initializeSSEHub dials RabbitMQ and builds the htmxsse.Hub backing the
// live session detail page (FR2, NFR2, LB7, issue #2242). Mirrors
// manmanv2/ui/main.go's initializeSSEHub (itself mirroring
// tools/app_registry/ui/main.go's) and events.RoutingKey's scheme
// (ARCHITECTURE.md "Event bus").
//
// RabbitMQURL unset or the broker unreachable must not prevent `ui` from
// starting or serving any other route (NFR2's degrade-and-retry stance):
// this returns nil in that case, logged as a WARNING, and
// handleSessionEvents responds 503 so the client's reconnect loop
// retries. Attach is lazy in htmxsse (Hub.Subscribe triggers it), so an
// unreachable-but-configured broker already degrades correctly on its
// own once a connection is returned here.
func initializeSSEHub(cfg config) *htmxsse.Hub {
	logger := logging.Get("main")

	if cfg.RabbitMQURL == "" {
		logger.Warn("RABBITMQ_URL not set; live session updates (/sessions/{id}/events) disabled")
		return nil
	}

	conn, err := rmq.NewConnectionFromURL(cfg.RabbitMQURL)
	if err != nil {
		logger.Warn("failed to connect to RabbitMQ; live session updates (/sessions/{id}/events) disabled", "error", err)
		return nil
	}

	// events.DeclareArgs() matches htmxsse.DefaultAttachFunc's own
	// hardcoded declare call byte-for-byte (topic/durable=true/
	// autoDelete=false/internal=false/noWait=false/args=nil) -- see
	// events.go's doc comment -- so the library's default attach func is
	// used directly rather than a local copy that could drift.
	attachFunc := htmxsse.DefaultAttachFunc(events.ExchangeName, conn)

	hubConfig := htmxsse.DefaultConfig()
	hubConfig.ExchangeName = events.ExchangeName

	return htmxsse.NewHub(attachFunc, hubConfig)
}

// Close releases this App's resources.
func (app *App) Close() error {
	if app.sseHub != nil {
		if err := app.sseHub.Close(); err != nil {
			logging.Get("main").Warn("error closing SSE hub", "error", err)
		}
	}
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

	// mcpauth's OAuth2 authorization-server endpoints (/authorize, /token,
	// /register, and both discovery metadata documents, FR9/issue #2245)
	// are registered directly on mux here, outside app.auth.RequireAuth --
	// unlike "/", none of setupRoutes' other registrations wrap these in
	// RequireAuthFunc, so there is no blanket auth middleware for Mount to
	// be caught under. Discovery and dynamic client registration must be
	// reachable before an MCP client has any credential at all; /authorize
	// itself is where app.mcpProvider's own Resolver + SignInURL gate
	// access to a signed-in operator, not RequireAuth.
	app.mcpProvider.Mount(mux)

	// Session list (FR3/C15, NFR3, issue #2247): the authenticated landing
	// page, mounted at both "/" and "/sessions" -- replacing issue #2236's
	// placeholder index -- so a bare sign-in and an explicit nav click both
	// land here.
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionList)))
	mux.HandleFunc("GET /sessions", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionList)))

	// Session lifecycle controls (FR1, issue #2246): start form, turn
	// composer, stop control. Registered ahead of "GET /sessions/{id}"
	// below -- Go 1.22 ServeMux's exact-literal-over-wildcard precedence
	// means "/sessions/new" always wins over "/sessions/{id}" regardless
	// of registration order, but the two are still grouped here so the
	// whole session route family reads top-to-bottom as one block.
	mux.HandleFunc("GET /sessions/new", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleNewSession)))
	mux.HandleFunc("POST /sessions", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleStartSession)))
	mux.HandleFunc("POST /sessions/{id}/turns", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSendTurn)))
	mux.HandleFunc("POST /sessions/{id}/stop", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleStopSession)))

	// Session detail (FR2, NFR2, NFR3, NFR4, issue #2242/#2248): full page
	// and its two SSE streams (transcript+state, usage panel). Both SSE
	// routes are wrapped with RequireAuthFunc only, never WithAccessToken
	// -- see handleSessionEvents' doc comment.
	mux.HandleFunc("GET /sessions/{id}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionDetail)))
	mux.HandleFunc("GET /sessions/{id}/events", app.auth.RequireAuthFunc(app.handleSessionEvents))
	mux.HandleFunc("GET /sessions/{id}/usage-events", app.auth.RequireAuthFunc(app.handleSessionUsageEvents))
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}
