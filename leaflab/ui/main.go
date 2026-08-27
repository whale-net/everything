// Command leaflab-ui is LeafLab's HTMX browser surface (A8, FR13). gRPC
// stays the programmatic surface (leaflab-api); this is a second,
// independent deployable that talks to leaflab-api over gRPC and forwards
// the logged-in user's own access token — it holds no service account
// credentials of its own and mints no tokens (NFR18.1: this layer is
// transport, not policy). Structure mirrors tools/app_registry/ui and
// manmanv2/ui, the only apps in this repo with HTMX BFF wiring precedent.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/logging"
)

//go:embed favicon.ico
var faviconIco []byte

// Config holds the application configuration, loaded entirely from
// environment variables — no config files (see ENV.md).
type Config struct {
	Host     string
	Port     string
	AuthMode string

	// OIDC configuration (required when AuthMode == "oidc"), validated
	// against the same Keycloak realm leaflab-api validates tokens
	// against.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// OIDCPostLogoutRedirectURL overrides where RP-initiated logout sends
	// the browser back to. Optional — htmxauth derives a default from
	// OIDCRedirectURL's origin when this is empty.
	OIDCPostLogoutRedirectURL string

	// Session
	SessionSecret string

	// leaflab-api gRPC endpoint.
	LeafLabAPIURL string
	// gRPC auth mode for forwarding the user's own token to leaflab-api
	// (grpcauth.WithUserToken / NewUserTokenDialOption — never a service
	// account, never a re-minted token; NFR18.1).
	GRPCAuthMode string

	// DatabaseURL backs htmxauth's DB-backed session manager (FR13: a
	// session must survive both a browser restart and a BFF pod restart).
	// Always required — this UI never falls back to cookie-only sessions,
	// matching tools/app_registry/ui's NewApp policy (see that file's
	// comment for why this stays app-owned, not a shared htmxauth
	// default).
	DatabaseURL string
}

// LoadConfig loads configuration from environment variables.
func LoadConfig() *Config {
	return &Config{
		Host:                      getEnv("HOST", "0.0.0.0"),
		Port:                      getEnv("PORT", "8000"),
		AuthMode:                  strings.ToLower(getEnv("AUTH_MODE", "none")),
		OIDCIssuer:                getEnv("OIDC_ISSUER", ""),
		OIDCClientID:              getEnv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:          getEnv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:           getEnv("OIDC_REDIRECT_URI", "http://localhost:8000/auth/callback"),
		OIDCPostLogoutRedirectURL: getEnv("OIDC_POST_LOGOUT_REDIRECT_URI", ""),
		SessionSecret:             getEnv("SECRET_KEY", "dev-secret-key-change-in-production"),
		LeafLabAPIURL:             getEnv("LEAFLAB_API_URL", "leaflab-api:50051"),
		GRPCAuthMode:              strings.ToLower(getEnv("GRPC_AUTH_MODE", "none")),
		// PG_DATABASE_URL matches leaflab-api and leaflab-migrate's own
		// variable name (see ../api/main.go, ../migrate).
		DatabaseURL: getEnv("PG_DATABASE_URL", ""),
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
	api         *APIClient
	userAuthOpt grpc.DialOption
}

// NewApp creates a new application instance.
func NewApp(ctx context.Context, config *Config) (*App, error) {
	var authMode htmxauth.AuthMode
	switch config.AuthMode {
	case "none", "":
		authMode = htmxauth.AuthModeNone
		log.Println("⚠️  Running in NO-AUTH mode (development only) — developer holds all roles")
	case "oidc":
		authMode = htmxauth.AuthModeOIDC
		log.Println("Running in OIDC mode")
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE: %s (must be 'none' or 'oidc')", config.AuthMode)
	}

	// This UI always uses DB-backed sessions (FR13: survives both a
	// browser restart and a BFF pod restart) so the boot preflight from
	// libs/go/htmxauth runs and a missing session table fails fast at
	// startup instead of silently degrading to cookie sessions (which
	// cannot refresh access tokens or survive a pod restart).
	if config.DatabaseURL == "" {
		return nil, fmt.Errorf("PG_DATABASE_URL is required: leaflab-ui always uses DB-backed sessions and never falls back to cookie sessions")
	}

	authConfig := htmxauth.Config{
		Mode:                      authMode,
		SessionSecret:             config.SessionSecret,
		SessionName:               "leaflab_ui_session",
		OIDCIssuer:                config.OIDCIssuer,
		OIDCClientID:              config.OIDCClientID,
		OIDCClientSecret:          config.OIDCClientSecret,
		OIDCRedirectURL:           config.OIDCRedirectURL,
		OIDCPostLogoutRedirectURL: config.OIDCPostLogoutRedirectURL,
	}

	pool, err := db.NewPool(ctx, config.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session DB: %w", err)
	}
	// NewDBSessionManager probes the ui_sessions table before returning; a
	// missing table (e.g. leaflab-migrate hasn't run the session migration
	// yet) fails boot here with a message naming the table and the
	// migration that owns it.
	store, err := htmxauth.NewDBSessionManager(ctx, pool, config.SessionSecret, "leaflab_ui_session")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize session store: %w", err)
	}
	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, authConfig, store)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
	}

	// Forwards the logged-in user's own access token on every leaflab-api
	// call — no service account credentials of the UI's own (NFR18.1).
	userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(config.GRPCAuthMode))

	api, err := NewAPIClient(ctx, config.LeafLabAPIURL, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize leaflab-api gRPC client: %w", err)
	}

	return &App{
		config:      config,
		auth:        auth,
		api:         api,
		userAuthOpt: userAuthOpt,
	}, nil
}

// Close cleans up application resources.
func (app *App) Close() error {
	if app.api != nil {
		return app.api.Close()
	}
	return nil
}

func main() {
	log.Println("Starting LeafLab UI...")

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
		log.Printf("Server listening on %s", addr)
		log.Printf("LeafLab API: %s", config.LeafLabAPIURL)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-shutdownCtx.Done()
	log.Println("Shutdown signal received, draining in-flight requests...")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(drainCtx); err != nil {
		log.Printf("Graceful shutdown did not complete cleanly: %v", err)
	}
}

func (app *App) setupRoutes(mux *http.ServeMux) {
	// Public routes — must sit outside the auth wrapper, or the sign-in
	// page itself would redirect to sign-in.
	mux.HandleFunc("/favicon.ico", htmxbase.FaviconHandler(faviconIco))
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Protected routes. Only "/" is scaffolded here (Phase 1, FR13) — the
	// device/region/reading screens are later tasks on this plan.
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHome)))
}

func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}
