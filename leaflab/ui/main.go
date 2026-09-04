// Command leaflab-ui is LeafLab's HTMX/templ web app. It signs a user in
// via OIDC, resolves them to a local leaflab_user row (FR2), keeps them
// signed in across visits with a server-side session (FR3), and calls
// leaflab-api on their behalf, forwarding the signed-in user's own access
// token (NFR2) — this package's only direct database use is htmxauth's
// session storage plus the leaflab_user upsert, never leaflab domain
// tables (board, sensor, sensor_reading, or any v_* view).
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
)

// sessionName is used as both the DB session store's name (the row-scoping
// key htmxauth.NewDBSessionManager persists sessions under) and the browser
// cookie name it derives from that argument.
const sessionName = "leaflab-ui"

// Config holds the application configuration, loaded entirely from
// environment variables — no config files (see ENV.md).
type Config struct {
	Host     string
	Port     string
	AuthMode string

	// OIDC configuration (required when AuthMode == "oidc"). This is the
	// UI's own OIDC client, distinct from leaflab-api's
	// (leaflab/api/ENV.md's GRPC_OIDC_CLIENT_ID) — see ENV.md's "Two OIDC
	// clients" note.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	// Session
	SessionSecret string

	// DatabaseURL backs htmxauth's DB-backed session manager and the
	// leaflab_user upsert (FR2). Always required (NFR3) — this UI never
	// falls back to cookie-only sessions; see NewApp.
	DatabaseURL string

	// leaflab-api gRPC endpoint.
	APIURL string
	// gRPC auth mode for forwarding the user's own token to leaflab-api.
	GRPCAuthMode string
}

// LoadConfig loads configuration from environment variables.
func LoadConfig() *Config {
	return &Config{
		Host:             getEnv("HOST", "0.0.0.0"),
		Port:             getEnv("PORT", "8000"),
		AuthMode:         strings.ToLower(getEnv("AUTH_MODE", "none")),
		OIDCIssuer:       getEnv("OIDC_ISSUER", ""),
		OIDCClientID:     getEnv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getEnv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getEnv("OIDC_REDIRECT_URI", "http://localhost:8000/auth/callback"),
		SessionSecret:    getEnv("SECRET_KEY", "dev-secret-key-change-in-production"),
		// PG_DATABASE_URL matches the variable every other LeafLab
		// component reads (see ../api/ENV.md) — deliberately not
		// "DATABASE_URL".
		DatabaseURL:  getEnv("PG_DATABASE_URL", ""),
		APIURL:       getEnv("LEAFLAB_API_URL", "leaflab-api:50051"),
		GRPCAuthMode: strings.ToLower(getEnv("GRPC_AUTH_MODE", "none")),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// App holds the application state.
type App struct {
	config      *Config
	auth        *htmxauth.Authenticator
	api         *LeafLabClient
	userAuthOpt grpc.DialOption
	sessionMgr  *htmxauth.DBSessionManager
	// pool backs the FR2 leaflab_user upsert in handlers_auth.go — the only
	// other direct database use besides htmxauth's own session storage
	// (NFR2: this UI never queries board/sensor/sensor_reading/v_* itself).
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewApp creates a new application instance.
func NewApp(ctx context.Context, config *Config) (*App, error) {
	logger := logging.Get("ui")

	var authMode htmxauth.AuthMode
	switch config.AuthMode {
	case "none", "":
		authMode = htmxauth.AuthModeNone
		logger.Warn("running in NO-AUTH mode (development only)")
	case "oidc":
		authMode = htmxauth.AuthModeOIDC
		logger.Info("running in OIDC auth mode")
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE: %s (must be 'none' or 'oidc')", config.AuthMode)
	}

	// This UI always uses DB-backed sessions (NFR3): without server-side
	// session storage, authenticated gRPC calls cannot outlive the first
	// token expiry, and a silent downgrade to cookie-only sessions would
	// turn a startup error into intermittent mid-session failures. Copied
	// from tools/app_registry/ui/main.go's NewApp, not manmanv2/ui's
	// DatabaseURL-optional fallback — see libs/go/htmxauth/README.md
	// § Convergence spike, Point 2.
	if config.DatabaseURL == "" {
		return nil, fmt.Errorf("PG_DATABASE_URL is required: leaflab-ui always uses DB-backed sessions and never falls back to cookie sessions")
	}

	authConfig := htmxauth.Config{
		Mode:             authMode,
		SessionSecret:    config.SessionSecret,
		SessionName:      sessionName,
		OIDCIssuer:       config.OIDCIssuer,
		OIDCClientID:     config.OIDCClientID,
		OIDCClientSecret: config.OIDCClientSecret,
		OIDCRedirectURL:  config.OIDCRedirectURL,
	}

	pool, err := db.NewPool(ctx, config.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session DB: %w", err)
	}
	// NewDBSessionManager probes the ui_sessions table before returning; a
	// missing table (migration 014 not yet applied) fails boot here with a
	// message naming the table and migration.
	store, err := htmxauth.NewDBSessionManager(ctx, pool, config.SessionSecret, sessionName)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize session store: %w", err)
	}
	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, authConfig, store)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
	}

	// Forwards the logged-in user's own access token on every leaflab-api
	// call (NFR2) — no service account credentials of the UI's own.
	userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(config.GRPCAuthMode))

	api, err := NewLeafLabClient(ctx, config.APIURL, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize leaflab-api gRPC client: %w", err)
	}

	return &App{
		config:      config,
		auth:        auth,
		api:         api,
		userAuthOpt: userAuthOpt,
		sessionMgr:  store,
		pool:        pool,
		logger:      logger,
	}, nil
}

// log returns app.logger, falling back to slog.Default() for an App built
// directly as a struct literal (e.g. in unit tests) rather than via NewApp.
func (app *App) log() *slog.Logger {
	if app.logger != nil {
		return app.logger
	}
	return slog.Default()
}

// Close cleans up application resources.
func (app *App) Close() error {
	if app.pool != nil {
		app.pool.Close()
	}
	if app.api != nil {
		return app.api.Close()
	}
	return nil
}

func main() {
	config := LoadConfig()
	ctx := context.Background()

	logging.Configure(logging.Config{
		ServiceName:   "leaflab-ui",
		Domain:        "leaflab",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")
	logger.Info("starting leaflab-ui")

	app, err := NewApp(ctx, config)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Close() //nolint:errcheck

	mux := http.NewServeMux()
	app.setupRoutes(mux)

	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      otelhttp.NewHandler(mux, "leaflab-ui"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown: stop accepting new connections and drain
	// in-flight requests on SIGTERM/SIGINT instead of dropping them.
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server listening", "addr", addr, "leaflab_api", config.APIURL)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown signal received, draining in-flight requests")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(drainCtx); err != nil {
		logger.Warn("graceful shutdown did not complete cleanly", "err", err)
	}
}

func (app *App) setupRoutes(mux *http.ServeMux) {
	// Public routes — must sit outside the auth wrapper, or the sign-in
	// page itself would redirect to sign-in.
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	// handleAuthCallback wraps htmxauth's HandleCallback with the FR2
	// leaflab_user upsert-on-sign-in — see handlers_auth.go.
	mux.HandleFunc("/auth/callback", app.handleAuthCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Protected routes. Being authenticated is the only requirement for M1
	// (FR1) — no role check, no leaflab_user_role lookup, no owner filter
	// gates any route here; authorization lands in M2.
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHome)))
	mux.HandleFunc("/boards", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoards)))
	// handleBoardDetail (#1503: FR6, FR7) -- every sensor on one board.
	mux.HandleFunc("/boards/{board_id}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBoardDetail)))
	// handleClaimBoard (#1765: FR1, FR2) -- the Claim button's POST target.
	mux.HandleFunc("POST /boards/{board_id}/claim", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleClaimBoard)))
	// handleRenameBoard (#1767: FR3) -- owner-only inline board rename,
	// re-renders the "#board-header" fragment via HTMX.
	mux.HandleFunc("POST /boards/{board_id}/rename", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleRenameBoard)))
	// handleRenameSensor (#1770: FR4) -- the per-sensor inline rename
	// form's POST target, rendered only for the board's owner.
	mux.HandleFunc("POST /sensors/{sensor_id}/rename", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleRenameSensor)))
	// handleSensorHistory (#1504: FR8, FR9, FR10) -- one sensor's reading
	// history chart. handleSensorHistoryData is the small JSON endpoint
	// its chart fetches range data from on every preset click or
	// drag-select (never on a timer, NFR1).
	mux.HandleFunc("/sensors/{sensor_id}/history", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSensorHistory)))
	mux.HandleFunc("/sensors/{sensor_id}/history/data", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSensorHistoryData)))
	// handleAdminBoards/handleReassignBoardOwner/handleClearBoardOwner
	// (#1777: FR11-FR14) -- the admin-only ownership correction screen.
	// Wrapped in the same auth chain as every other route here; the real
	// admin-only enforcement is server-side requireAdmin on the four
	// backing RPCs (NFR1), not this wrapper.
	mux.HandleFunc("/admin/boards", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAdminBoards)))
	mux.HandleFunc("POST /admin/boards/{board_id}/reassign", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleReassignBoardOwner)))
	mux.HandleFunc("POST /admin/boards/{board_id}/clear", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleClearBoardOwner)))
}

func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}
