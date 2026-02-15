package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
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

	// Experience API
	ExperienceAPIURL string

	// ManMan gRPC API (for actions management)
	ManManAPIAddr string
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
		ExperienceAPIURL: getEnv("EXPERIENCE_API_URL", "http://experience-api-dev-service:8000"),
		ManManAPIAddr:    getEnv("MANMAN_API_ADDR", ""),
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
	grpc   *GRPCClient
}

// NewApp creates a new application instance
func NewApp(ctx context.Context, config *Config) (*App, error) {
	// Determine auth mode
	var authMode htmxauth.AuthMode
	switch config.AuthMode {
	case "none", "":
		authMode = htmxauth.AuthModeNone
		slog.Warn("running in NO-AUTH mode (development only)")
	case "oidc":
		authMode = htmxauth.AuthModeOIDC
		slog.Info("running in OIDC mode")
	default:
		return nil, fmt.Errorf("invalid AUTH_MODE: %s (must be 'none' or 'oidc')", config.AuthMode)
	}

	// Configure authenticator
	authConfig := htmxauth.Config{
		Mode:             authMode,
		SessionSecret:    config.SessionSecret,
		SessionName:      "management_ui_session",
		OIDCIssuer:       config.OIDCIssuer,
		OIDCClientID:     config.OIDCClientID,
		OIDCClientSecret: config.OIDCClientSecret,
		OIDCRedirectURL:  config.OIDCRedirectURL,
	}

	auth, err := htmxauth.NewAuthenticator(ctx, authConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
	}

	app := &App{
		config: config,
		auth:   auth,
	}

	// Initialize gRPC client for ManMan API (optional - actions management)
	if config.ManManAPIAddr != "" {
		grpcClient, err := NewGRPCClient(config.ManManAPIAddr)
		if err != nil {
			slog.Warn("failed to connect to ManMan gRPC API, actions management disabled", "error", err, "addr", config.ManManAPIAddr)
		} else {
			app.grpc = grpcClient
			slog.Info("connected to ManMan gRPC API", "addr", config.ManManAPIAddr)
		}
	} else {
		slog.Info("ManMan gRPC API not configured, actions management disabled")
	}

	return app, nil
}

func main() {
	// Configure structured logging
	logging.Configure(logging.Config{
		ServiceName: "management-ui",
		Domain:      "manmanv2",
		JSONFormat:  getEnv("LOG_FORMAT", "json") == "json",
	})
	defer logging.Shutdown(context.Background())

	logger := logging.Get("main")
	logger.Info("starting management UI")

	// Load configuration
	config := LoadConfig()

	// Create application
	ctx := context.Background()
	app, err := NewApp(ctx, config)
	if err != nil {
		logger.Error("failed to initialize application", "error", err)
		os.Exit(1)
	}

	// Setup HTTP server
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	// Create server with request logging middleware
	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      requestLoggingMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	logger.Info("server listening", "addr", addr)
	if err := server.ListenAndServe(); err != nil {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}

// statusRecorder wraps http.ResponseWriter to capture the status code
type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

// requestLoggingMiddleware logs method, path, status, and duration for each request.
// Health checks are logged at Debug to reduce noise.
func requestLoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(rec, r)
		duration := time.Since(start)

		if r.URL.Path == "/health" {
			slog.Debug("http request", "method", r.Method, "path", r.URL.Path, "status", rec.statusCode, "duration", duration)
		} else {
			slog.Info("http request", "method", r.Method, "path", r.URL.Path, "status", rec.statusCode, "duration", duration)
		}
	})
}

func (app *App) setupRoutes(mux *http.ServeMux) {
	// Public routes
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Protected routes
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.handleHome))
	mux.HandleFunc("/api/worker-status/", app.auth.RequireAuthFunc(app.handleWorkerStatus))
	mux.HandleFunc("/api/servers/", app.auth.RequireAuthFunc(app.handleServers))
	mux.HandleFunc("/api/available-servers", app.auth.RequireAuthFunc(app.handleAvailableServers))
	mux.HandleFunc("/api/start-server", app.auth.RequireAuthFunc(app.handleStartServer))
	mux.HandleFunc("/instance/", app.auth.RequireAuthFunc(app.handleInstancePage))
	mux.HandleFunc("/api/execute-command", app.auth.RequireAuthFunc(app.handleExecuteCommand))
	mux.HandleFunc("/api/add-command-modal", app.auth.RequireAuthFunc(app.handleAddCommandModal))
	mux.HandleFunc("/api/create-command", app.auth.RequireAuthFunc(app.handleCreateCommand))

	// Game server type management routes
	mux.HandleFunc("/gameservers", app.auth.RequireAuthFunc(app.handleGameServersList))
	mux.HandleFunc("/gameserver/", app.auth.RequireAuthFunc(app.handleGameServerPage))
	mux.HandleFunc("/api/add-gameserver-command-modal", app.auth.RequireAuthFunc(app.handleAddGameServerCommandModal))
	mux.HandleFunc("/api/create-gameserver-command", app.auth.RequireAuthFunc(app.handleCreateGameServerCommand))

	// Actions management routes (requires ManMan gRPC API)
	mux.HandleFunc("/actions/", app.auth.RequireAuthFunc(app.handleActionsPage))
	mux.HandleFunc("/api/actions/create", app.auth.RequireAuthFunc(app.handleCreateAction))
	mux.HandleFunc("/api/actions/", app.auth.RequireAuthFunc(app.handleDeleteAction))
}
