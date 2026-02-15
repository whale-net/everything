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

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/htmxauth"
	manmanpb "github.com/whale-net/everything/manman/protos"
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

	// Log Processor (gRPC)
	LogProcessorURL string
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
		LogProcessorURL:  getEnv("LOG_PROCESSOR_URL", "log-processor:50053"),
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
	config       *Config
	auth         *htmxauth.Authenticator
	grpc         *ControlClient
	logProcessor manmanpb.LogProcessorClient
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

	// Initialize log-processor gRPC client
	logProcessorConn, err := grpc.Dial(config.LogProcessorURL, grpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("failed to connect to log-processor: %w", err)
	}
	logProcessorClient := manmanpb.NewLogProcessorClient(logProcessorConn)

	return &App{
		config:       config,
		auth:         auth,
		grpc:         grpcClient,
		logProcessor: logProcessorClient,
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

	// Server selection endpoint
	mux.HandleFunc("/select-server", app.auth.RequireAuthFunc(app.handleSelectServer))

	// Protected routes - Home/Dashboard
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.handleHome))
	mux.HandleFunc("/sessions", app.auth.RequireAuthFunc(app.handleSessions))
	mux.HandleFunc("/sessions/", app.auth.RequireAuthFunc(app.handleSessionDetail))
	mux.HandleFunc("/sessions/start", app.auth.RequireAuthFunc(app.handleSessionStart))
	mux.HandleFunc("/api/sessions/check-active", app.auth.RequireAuthFunc(app.handleCheckActiveSession))
	mux.HandleFunc("/api/sessions/historical-logs", app.auth.RequireAuthFunc(app.handleHistoricalLogs))
	mux.HandleFunc("/api/sessions/", app.auth.RequireAuthFunc(app.handleSessionStdin))

	// Note: Log streaming endpoint is handled by handleSessionDetail which routes to handleSessionLogsStream

	// Protected routes - Games
	mux.HandleFunc("/games", app.auth.RequireAuthFunc(app.handleGames))
	mux.HandleFunc("/games/new", app.auth.RequireAuthFunc(app.handleGameNew))
	mux.HandleFunc("/games/create", app.auth.RequireAuthFunc(app.handleGameCreate))
	mux.HandleFunc("/games/", app.auth.RequireAuthFunc(app.handleGameDetail))

	// Note: Config routes are handled within handleGameDetail based on URL parsing

	// Documentation routes
	mux.HandleFunc("/docs/config-strategies", app.auth.RequireAuthFunc(app.handleConfigStrategiesDocs))

	// Protected routes - Servers
	mux.HandleFunc("/servers", app.auth.RequireAuthFunc(app.handleServers))
	mux.HandleFunc("/servers/", app.auth.RequireAuthFunc(app.handleServerDetail))

	// API endpoints for HTMX partial updates
	mux.HandleFunc("/api/dashboard-summary", app.auth.RequireAuthFunc(app.handleDashboardSummary))
	mux.HandleFunc("/api/dashboard-sessions", app.auth.RequireAuthFunc(app.handleDashboardSessions))
}

func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}

func (app *App) handleSelectServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	serverIDStr := strings.TrimSpace(r.FormValue("server_id"))
	if serverIDStr == "" {
		http.Error(w, "Missing server_id", http.StatusBadRequest)
		return
	}

	_, err := strconv.ParseInt(serverIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid server_id", http.StatusBadRequest)
		return
	}

	// Set cookie for selected server (expires in 30 days)
	http.SetCookie(w, &http.Cookie{
		Name:     "selected_server_id",
		Value:    serverIDStr,
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	// Redirect back to referer or home
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}

	// Handle HTMX redirect
	if r.Header.Get("HX-Request") != "" {
		w.Header().Set("HX-Redirect", referer)
		w.WriteHeader(http.StatusOK)
	} else {
		http.Redirect(w, r, referer, http.StatusSeeOther)
	}
}

// getSelectedServerID retrieves the selected server ID from cookie, falling back to default
func (app *App) getSelectedServerID(r *http.Request, servers []*manmanpb.Server) int64 {
	// Try cookie first
	if cookie, err := r.Cookie("selected_server_id"); err == nil {
		if serverID, err := strconv.ParseInt(cookie.Value, 10, 64); err == nil {
			// Verify server exists
			for _, s := range servers {
				if s.ServerId == serverID {
					return serverID
				}
			}
		}
	}

	// Fall back to default server
	for _, s := range servers {
		if s.IsDefault {
			return s.ServerId
		}
	}

	// Last resort: first server
	if len(servers) > 0 {
		return servers[0].ServerId
	}

	return 0
}

// getSelectedServer returns the selected server object
func (app *App) getSelectedServer(r *http.Request, servers []*manmanpb.Server) *manmanpb.Server {
	selectedID := app.getSelectedServerID(r, servers)
	for _, s := range servers {
		if s.ServerId == selectedID {
			return s
		}
	}
	return nil
}

// buildLayoutData populates common layout data with servers and selection
func (app *App) buildLayoutData(r *http.Request, title, active string, user *htmxauth.UserInfo) (LayoutData, error) {
	ctx := context.Background()
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers for layout: %v", err)
		servers = []*manmanpb.Server{}
	}

	selectedServer := app.getSelectedServer(r, servers)
	var defaultServerID int64
	for _, s := range servers {
		if s.IsDefault {
			defaultServerID = s.ServerId
			break
		}
	}

	return LayoutData{
		Title:           title,
		Active:          active,
		User:            user,
		Servers:         servers,
		SelectedServer:  selectedServer,
		DefaultServerID: defaultServerID,
	}, nil
}
