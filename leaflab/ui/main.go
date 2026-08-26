// Command leaflab-ui is the LeafLab's HTMX browser UI. It provides an interactive
// interface for managing sensor boards. All leaflab domain data comes over gRPC
// from leaflab-api, forwarding the logged-in user's own access token (FR-40) —
// this package's only direct database use is htmxauth's session storage (NFR-3),
// never leaflab domain tables.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/logging"
)

// Config holds the application configuration, loaded entirely from
// environment variables — no config files (see ENV.md).
type Config struct {
	Host     string
	Port     string
	AuthMode string

	// OIDC configuration (required when AuthMode == "oidc")
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// OIDCPostLogoutRedirectURL overrides where RP-initiated logout (issue
	// #763) sends the browser back to. Optional — htmxauth derives a
	// default from OIDCRedirectURL's origin when this is empty; set it only
	// if the derived value isn't a registered post-logout redirect URI with
	// the OIDC provider.
	OIDCPostLogoutRedirectURL string

	// Session
	SessionSecret string

	// leaflab-api gRPC endpoint
	LeafLabAPIURL string
	// gRPC auth mode for forwarding the user's own token to leaflab-api
	GRPCAuthMode string

	// DatabaseURL backs htmxauth's DB-backed session manager. Always
	// required — this UI never falls back to cookie-only sessions.
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
		// PG_DATABASE_URL matches the variable every other component reads.
		DatabaseURL: getEnv("PG_DATABASE_URL", ""),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	b, _ := strconv.ParseBool(value)
	return b
}

// App holds the application state.
type App struct {
	config   *Config
	auth     *htmxauth.Authenticator
	apiClient *LeafLabClient
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

	// This UI always uses DB-backed sessions so the boot preflight from
	// libs/go/htmxauth runs and a missing session table fails fast
	// at startup instead of silently degrading to cookie sessions
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
	// missing table fails boot here with a message naming the table and the
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
	// call — no service account credentials of the UI's own.
	userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(config.GRPCAuthMode))

	apiClient, err := NewLeafLabClient(ctx, config.LeafLabAPIURL, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize leaflab gRPC client: %w", err)
	}

	return &App{
		config:    config,
		auth:      auth,
		apiClient: apiClient,
	}, nil
}

// Close cleans up application resources.
func (app *App) Close() error {
	if app.apiClient != nil {
		return app.apiClient.Close()
	}
	return nil
}

func main() {
	log.Println("Starting LeafLab UI...")

	config := LoadConfig()
	ctx := context.Background()

	logging.Configure(logging.Config{
		ServiceName: "leaflab-ui",
		Domain:      "leaflab",
		JSONFormat:  true,
		EnableOTLP:  true,
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
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Protected routes.
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDashboard)))
}

// handleHealth returns a health check response.
func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status":"ok"}`)
}

// handleDashboard is a placeholder dashboard handler.
func (app *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	
	data := htmxbase.LayoutData{
		Title:   "LeafLab",
		Content: `<div class="container"><h1>LeafLab Dashboard</h1><p>Welcome to LeafLab sensor board management.</p></div>`,
	}
	
	if err := htmxbase.Render(w, data); err != nil {
		log.Printf("Error rendering template: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
