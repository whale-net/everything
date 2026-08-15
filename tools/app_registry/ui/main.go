// Command app-registry-ui is the App Registry's HTMX admin UI. It records
// promotions; it never deploys anything (NFR-1). All registry domain data
// comes over gRPC from app-registry-api, forwarding the logged-in user's
// own access token (FR-40) — this package's only direct database use is
// htmxauth's session storage (NFR-3), never registry domain tables.
package main

import (
	"context"
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

	// Session
	SessionSecret string

	// app-registry-api gRPC endpoint
	RegistryAPIURL string
	// gRPC auth mode for forwarding the user's own token to app-registry-api
	GRPCAuthMode string

	// DatabaseURL backs htmxauth's DB-backed session manager. Always
	// required — unlike manmanv2/ui, this UI never falls back to
	// cookie-only sessions (see NewApp).
	DatabaseURL string
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
		RegistryAPIURL:   getEnv("REGISTRY_API_URL", "app-registry-api:50051"),
		GRPCAuthMode:     strings.ToLower(getEnv("GRPC_AUTH_MODE", "none")),
		// PG_DATABASE_URL matches the variable every other App Registry
		// component reads (see ../ENV.md) — deliberately not "DATABASE_URL".
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
	registry    *RegistryClient
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

	// This UI always uses DB-backed sessions so the boot preflight from
	// libs/go/htmxauth (FR-58) runs and a missing session table fails fast
	// at startup instead of silently degrading to cookie sessions (which
	// cannot refresh access tokens).
	if config.DatabaseURL == "" {
		return nil, fmt.Errorf("PG_DATABASE_URL is required: app-registry-ui always uses DB-backed sessions and never falls back to cookie sessions")
	}

	authConfig := htmxauth.Config{
		Mode:             authMode,
		SessionSecret:    config.SessionSecret,
		SessionName:      "app_registry_ui_session",
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
	// missing table (e.g. app-registry-migration hasn't run migration 011
	// yet) fails boot here with a message naming the table and the
	// migration that owns it, per FR-58.
	store, err := htmxauth.NewDBSessionManager(ctx, pool, config.SessionSecret, "app_registry_ui_session")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize session store: %w", err)
	}
	auth, err := htmxauth.NewAuthenticatorWithDB(ctx, authConfig, store)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
	}

	// Forwards the logged-in user's own access token on every registry
	// call (FR-40) — no service account credentials of the UI's own.
	userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(config.GRPCAuthMode))

	registry, err := NewRegistryClient(ctx, config.RegistryAPIURL, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize registry gRPC client: %w", err)
	}

	return &App{
		config:      config,
		auth:        auth,
		registry:    registry,
		userAuthOpt: userAuthOpt,
	}, nil
}

// Close cleans up application resources.
func (app *App) Close() error {
	if app.registry != nil {
		return app.registry.Close()
	}
	return nil
}

func main() {
	log.Println("Starting App Registry UI...")

	config := LoadConfig()
	ctx := context.Background()

	logging.Configure(logging.Config{
		ServiceName:   "app-registry-ui",
		Domain:        "app-registry",
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
		Handler:      otelhttp.NewHandler(mux, "app-registry-ui"),
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
		log.Printf("Registry API: %s", config.RegistryAPIURL)
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

// withAccessToken wraps a protected handler to inject the user's access
// token into the request context for gRPC forwarding. If the token is
// unavailable or expired, the user is redirected to re-authenticate. HTMX
// partial requests receive an HX-Redirect header so the full page reloads
// to the login URL.
func (app *App) withAccessToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := app.auth.GetAccessToken(r)
		if err != nil {
			loginURL := fmt.Sprintf("/auth/login?next=%s", r.URL.RequestURI())
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		r = r.WithContext(grpcauth.WithUserToken(r.Context(), token))
		next(w, r)
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
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleDashboard)))
	mux.HandleFunc("/deployments", app.auth.RequireAuthFunc(app.withAccessToken(app.handleDeployments)))
}

func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}
