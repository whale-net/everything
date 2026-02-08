package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
)

// Config holds the application configuration
type Config struct {
	Host     string
	Port     string
	AuthMode string

	// OIDC Configuration (optional, only for oidc mode)
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string

	// Session
	SessionSecret string

	// Control API (gRPC)
	ControlAPIURL string
}

// LoadConfig loads configuration from environment variables
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
		ControlAPIURL:    getEnv("CONTROL_API_URL", "control-api-dev-service:50051"),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// App holds the application state
type App struct {
	config *Config
	auth   *htmxauth.Authenticator
	grpc   *ControlClient
}

// NewApp creates a new application instance
func NewApp(ctx context.Context, config *Config) (*App, error) {
	// Determine auth mode
	var authMode htmxauth.AuthMode
	switch config.AuthMode {
	case "none", "":
		authMode = htmxauth.AuthModeNone
		log.Println("⚠️  Running in NO-AUTH mode (development only)")
	case "oidc":
		authMode = htmxauth.AuthModeOIDC
		log.Println("Running in OIDC mode")
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE: %s (must be 'none' or 'oidc')", config.AuthMode)
	}

	// Configure authenticator
	authConfig := htmxauth.Config{
		Mode:             authMode,
		SessionSecret:    config.SessionSecret,
		SessionName:      "manmanv2_ui_session",
		OIDCIssuer:       config.OIDCIssuer,
		OIDCClientID:     config.OIDCClientID,
		OIDCClientSecret: config.OIDCClientSecret,
		OIDCRedirectURL:  config.OIDCRedirectURL,
	}

	auth, err := htmxauth.NewAuthenticator(ctx, authConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
	}

	// Initialize gRPC client
	grpcClient, err := NewControlClient(ctx, config.ControlAPIURL)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize gRPC client: %w", err)
	}

	return &App{
		config: config,
		auth:   auth,
		grpc:   grpcClient,
	}, nil
}

// Close cleans up application resources
func (app *App) Close() error {
	if app.grpc != nil {
		return app.grpc.Close()
	}
	return nil
}

func main() {
	log.Println("Starting ManManV2 Management UI...")

	// Load configuration
	config := LoadConfig()

	// Create application
	ctx := context.Background()
	app, err := NewApp(ctx, config)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Close()

	// Setup HTTP server
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	// Create server
	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("Server listening on %s", addr)
	log.Printf("Control API: %s", config.ControlAPIURL)
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func (app *App) setupRoutes(mux *http.ServeMux) {
	// Public routes
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Protected routes - Home/Dashboard
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.handleHome))

	// Protected routes - Games
	mux.HandleFunc("/games", app.auth.RequireAuthFunc(app.handleGames))
	mux.HandleFunc("/games/new", app.auth.RequireAuthFunc(app.handleGameNew))
	mux.HandleFunc("/games/create", app.auth.RequireAuthFunc(app.handleGameCreate))
	mux.HandleFunc("/games/", app.auth.RequireAuthFunc(app.handleGameDetail))
	
	// Note: Config routes are handled within handleGameDetail based on URL parsing

	// Protected routes - Servers
	mux.HandleFunc("/servers", app.auth.RequireAuthFunc(app.handleServers))
	mux.HandleFunc("/servers/", app.auth.RequireAuthFunc(app.handleServerDetail))

	// API endpoints for HTMX partial updates
	mux.HandleFunc("/api/dashboard-summary", app.auth.RequireAuthFunc(app.handleDashboardSummary))
}

func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}
