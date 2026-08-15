// Package ui implements the App Registry admin UI — an HTMX + daisyUI interface
// for environment and promotion triage.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	appregistrypb "github.com/whale-net/everything/tools/app_registry/protos"
)

// Config holds the application configuration. All fields come from environment
// variables — no flags, no config files, no code-only options.
type Config struct {
	Host     string
	Port     string
	AuthMode string

	// OIDC Configuration (optional; only for oidc mode).
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	SessionName   string
	SessionSecret string

	// App Registry API (gRPC).
	RegistryAPIURL string

	GRPCTokenAuthMode grpcauth.AuthMode

	// Database for session storage. When non-empty the authenticator uses a
	// DB-backed session store that performs a boot-time preflight against the
	// ui_sessions table (panic if missing; never falls back to cookies).
	DatabaseURL string
}

// LoadConfig reads every field from environment variables and returns them.
func LoadConfig() *Config {
	return &Config{
		Host:              getEnv("HOST", "0.0.0.0"),
		Port:              getEnv("PORT", "8001"),
		AuthMode:          strings.ToLower(getEnv("AUTH_MODE", "none")),
		OIDCIssuer:        getEnv("OIDC_ISSUER", ""),
		OIDCClientID:      getEnv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret:  getEnv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:   getEnv("OIDC_REDIRECT_URI", "http://localhost:8001/auth/callback"),
		SessionName:       getEnv("SESSION_NAME", "app_registry_ui_session"),
		SessionSecret:     getEnv("SECRET_KEY", "dev-secret-key-change-in-production"),
		RegistryAPIURL:    getEnv("APP_REGISTRY_API_URL", "localhost:50051"),
		GRPCTokenAuthMode: grpcauth.AuthMode(strings.ToLower(getEnv("GRPC_AUTH_MODE", "none"))),
		DatabaseURL:       getEnv("PG_DATABASE_URL", ""),
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

// App holds the application state and wiring.
type App struct {
	config    *Config
	auth      *htmxauth.Authenticator
	registry  appregistrypb.EnvironmentRegistryClient
	userToken grpc.DialOption
}

// NewApp creates the full dependency graph from config.
func NewApp(ctx context.Context, cfg *Config) (*App, error) {
	// --- auth ---------------------------------------------------------------
	var mode htmxauth.AuthMode
	switch cfg.AuthMode {
	case "none", "":
		mode = htmxauth.AuthModeNone
		log.Println("Running in NO-AUTH mode (development only)")
	case "oidc":
		mode = htmxauth.AuthModeOIDC
		log.Println("Running in OIDC mode")
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE %q (must be 'none' or 'oidc')", cfg.AuthMode)
	}

	authCfg := htmxauth.Config{
		Mode:             mode,
		SessionName:      cfg.SessionName,
		SessionSecret:    cfg.SessionSecret,
		OIDCIssuer:       cfg.OIDCIssuer,
		OIDCClientID:     cfg.OIDCClientID,
		OIDCClientSecret: cfg.OIDCClientSecret,
		OIDCRedirectURL:  cfg.OIDCRedirectURL,
	}

	var auth *htmxauth.Authenticator
	if cfg.DatabaseURL != "" {
		log.Println("Using DB-backed sessions (token refresh enabled)")
		pool, err := db.NewPool(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("create session pool: %w", err)
		}
		store := htmxauth.NewDBSessionManager(ctx, pool, cfg.SessionSecret, cfg.SessionName)
		auth, err = htmxauth.NewAuthenticatorWithDB(ctx, authCfg, store)
		if err != nil {
			return nil, fmt.Errorf("init authenticator with DB: %w", err)
		}
	} else {
		log.Println("Using cookie-backed sessions (no DATABASE_URL set)")
		var err error
		auth, err = htmxauth.NewAuthenticator(ctx, authCfg)
		if err != nil {
			return nil, fmt.Errorf("init authenticator: %w", err)
		}
	}

	// --- gRPC client --------------------------------------------------------
	userTokenOpt := grpcauth.NewUserTokenDialOption(cfg.GRPCTokenAuthMode)
	conn, err := grpcclient.NewClient(ctx, cfg.RegistryAPIURL, userTokenOpt)
	if err != nil {
		return nil, fmt.Errorf("dial registry API: %w", err)
	}

	// No SQL against domain tables — the only DB work is session storage via htmxauth.

	return &App{
		config:    cfg,
		auth:      auth,
		registry:  appregistrypb.NewEnvironmentRegistryClient(conn.GetConnection()),
		userToken: userTokenOpt,
	}, nil
}

// Close releases resources held by the App.
func (a *App) Close() error {
	// Connection is managed via grpcclient; no per-request cleanup needed.
	return nil
}

// withUserToken injects the current session's access token into the request context
// for forwarding to gRPC calls. Returns an HTTP redirect in AuthModeOIDC, or passes
// through unconditionally in AuthModeNone (where "dev-token" is always available).
func (a *App) withUserToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := a.auth.GetAccessToken(r)
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

// setupRoutes registers all HTTP routes on mux. Public routes (health, auth flows) are
// registered directly; every other route is wrapped by a.auth.RequireAuthFunc and an
// optional withUserToken wrapper. The /themes.css static file sits outside the auth
// wrapper so the sign-in page can load its palette without authentication.
func (a *App) setupRoutes(mux *http.ServeMux) {
	// Public — no auth required.
	mux.HandleFunc("/health", a.handleHealth)
	mux.HandleFunc("/auth/login", a.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", a.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", a.auth.HandleLogout)

	// Themes.css must be publicly accessible so the sign-in page can style itself.
	// The htmxbase layout renders CustomCSS before CustomHead; since themes.css
	// loads after daisyUI we serve it as an inlined <style> tag inside the layout
	// (CustomHead slot) rather than a separate route — see templ_render.go.

	// Authenticated routes.
	mux.HandleFunc("/", a.auth.RequireAuthFunc(a.withUserToken(a.handleHome)))
}

func main() {
	cfg := LoadConfig()
	ctx := context.Background()

	logging.Configure(logging.Config{
		ServiceName:   "app-registry-ui",
		Domain:        "app-registry",
		JSONFormat:    true,
		EnableOTLP:    false, // dev-only for now; enable when deployed.
		EnableTracing: true,
	})

	app, err := NewApp(ctx, cfg)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Close()

	mux := http.NewServeMux()
	app.setupRoutes(mux)

	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      otelhttp.NewHandler(mux, "app-registry-ui"),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second, // generous for templ render.
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("Server listening on %s", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
