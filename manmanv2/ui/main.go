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
	"google.golang.org/grpc/credentials/insecure"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/manmanv2/ui/components"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
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

	// gRPC auth mode for forwarding user tokens
	GRPCAuthMode string

	// Database (optional; enables DB-backed sessions with automatic token refresh)
	DatabaseURL string
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
		GRPCAuthMode:     strings.ToLower(getEnv("GRPC_AUTH_MODE", "none")),
		DatabaseURL:      getEnv("PG_DATABASE_URL", ""),
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
	userAuthOpt  grpc.DialOption
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

	var auth *htmxauth.Authenticator
	if config.DatabaseURL != "" {
		log.Println("Using DB-backed sessions (token refresh enabled)")
		pool, err := db.NewPool(ctx, config.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to session DB: %w", err)
		}
		store := htmxauth.NewDBSessionManager(ctx, pool, config.SessionSecret, "manmanv2_ui_session")
		auth, err = htmxauth.NewAuthenticatorWithDB(ctx, authConfig, store)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
		}
	} else {
		log.Println("Using cookie-backed sessions (no DATABASE_URL set; access tokens will not refresh)")
		var err error
		auth, err = htmxauth.NewAuthenticator(ctx, authConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
		}
	}

	// Create user token dial option for forwarding per-request tokens
	userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(config.GRPCAuthMode))

	// Initialize gRPC client
	grpcClient, err := NewControlClient(ctx, config.ControlAPIURL, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize gRPC client: %w", err)
	}

	// Initialize log-processor gRPC client
	logProcessorConn, err := grpc.NewClient(config.LogProcessorURL,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		userAuthOpt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to log-processor: %w", err)
	}
	logProcessorClient := manmanpb.NewLogProcessorClient(logProcessorConn)

	return &App{
		config:       config,
		auth:         auth,
		grpc:         grpcClient,
		logProcessor: logProcessorClient,
		userAuthOpt:  userAuthOpt,
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

// withAccessToken wraps a protected handler to also inject the user's access token
// into the request context for gRPC forwarding.
func (app *App) withAccessToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := app.auth.GetAccessToken(r)
		if err == nil {
			r = r.WithContext(grpcauth.WithUserToken(r.Context(), token))
		}
		next(w, r)
	}
}

func (app *App) setupRoutes(mux *http.ServeMux) {
	// Public routes
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Server selection endpoint
	mux.HandleFunc("/select-server", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSelectServer)))

	// Protected routes - Home/Dashboard
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleHome)))
	mux.HandleFunc("/sessions", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSessions)))
	mux.HandleFunc("/sessions/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSessionDetail)))
	mux.HandleFunc("/sessions/start", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSessionStart)))
	mux.HandleFunc("/api/sessions/check-active", app.auth.RequireAuthFunc(app.withAccessToken(app.handleCheckActiveSession)))
	mux.HandleFunc("/api/sessions/historical-logs", app.auth.RequireAuthFunc(app.withAccessToken(app.handleHistoricalLogs)))
	mux.HandleFunc("/api/sessions/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSessionStdin)))

	// Note: Log streaming endpoint is handled by handleSessionDetail which routes to handleSessionLogsStream

	// Protected routes - Games
	mux.HandleFunc("/games", app.auth.RequireAuthFunc(app.withAccessToken(app.handleGames)))
	mux.HandleFunc("/games/new", app.auth.RequireAuthFunc(app.withAccessToken(app.handleGameNew)))
	mux.HandleFunc("/games/create", app.auth.RequireAuthFunc(app.withAccessToken(app.handleGameCreate)))
	mux.HandleFunc("/games/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleGameDetail)))

	// Note: Config routes are handled within handleGameDetail based on URL parsing
	// Note: Action management routes are also handled within handleGameDetail and handleGameConfigDetail

	// Documentation routes
	mux.HandleFunc("/docs/config-strategies", app.auth.RequireAuthFunc(app.withAccessToken(app.handleConfigStrategiesDocs)))

	// Protected routes - Servers
	mux.HandleFunc("/servers", app.auth.RequireAuthFunc(app.withAccessToken(app.handleServers)))
	mux.HandleFunc("/servers/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleServerDetail)))

	// Protected routes - Workshop
	mux.HandleFunc("/workshop/library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleWorkshopLibrary)))
	mux.HandleFunc("/workshop/search", app.auth.RequireAuthFunc(app.withAccessToken(app.handleWorkshopSearch)))
	mux.HandleFunc("/workshop/addon", app.auth.RequireAuthFunc(app.withAccessToken(app.handleWorkshopAddonDetail)))
	mux.HandleFunc("/workshop/library-detail", app.auth.RequireAuthFunc(app.withAccessToken(app.handleLibraryDetail)))
	mux.HandleFunc("/workshop/create-library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleCreateLibrary)))
	mux.HandleFunc("/workshop/delete-library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleDeleteLibrary)))
	mux.HandleFunc("/workshop/add-addon-to-library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleAddAddonToLibrary)))
	mux.HandleFunc("/workshop/remove-addon-from-library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleRemoveAddonFromLibrary)))
	mux.HandleFunc("/workshop/add-library-reference", app.auth.RequireAuthFunc(app.withAccessToken(app.handleAddLibraryReference)))
	mux.HandleFunc("/workshop/remove-library-reference", app.auth.RequireAuthFunc(app.withAccessToken(app.handleRemoveLibraryReference)))
	mux.HandleFunc("/workshop/installations", app.auth.RequireAuthFunc(app.withAccessToken(app.handleWorkshopInstallations)))
	mux.HandleFunc("/workshop/install", app.auth.RequireAuthFunc(app.withAccessToken(app.handleInstallAddon)))
	mux.HandleFunc("/workshop/remove", app.auth.RequireAuthFunc(app.withAccessToken(app.handleRemoveInstallation)))
	mux.HandleFunc("/workshop/reset", app.auth.RequireAuthFunc(app.withAccessToken(app.handleResetInstallation)))
	mux.HandleFunc("/workshop/fetch-metadata", app.auth.RequireAuthFunc(app.withAccessToken(app.handleFetchAddonMetadata)))
	mux.HandleFunc("/workshop/create-addon", app.auth.RequireAuthFunc(app.withAccessToken(app.handleCreateAddon)))
	mux.HandleFunc("/workshop/update-addon-details", app.auth.RequireAuthFunc(app.withAccessToken(app.handleUpdateAddonDetails)))
	mux.HandleFunc("/workshop/update-library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleUpdateLibrary)))
	mux.HandleFunc("/workshop/delete-addon", app.auth.RequireAuthFunc(app.withAccessToken(app.handleDeleteAddon)))
	mux.HandleFunc("/workshop/api/available-addons", app.auth.RequireAuthFunc(app.withAccessToken(app.handleAvailableAddons)))
	mux.HandleFunc("/workshop/api/available-libraries", app.auth.RequireAuthFunc(app.withAccessToken(app.handleAvailableLibraries)))
	mux.HandleFunc("/workshop/api/presets-for-game", app.auth.RequireAuthFunc(app.withAccessToken(app.handlePresetsForGame)))

	// Protected routes - SGC detail
	mux.HandleFunc("/sgc/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSGCRoutes)))
	mux.HandleFunc("/sgc/add-library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleAddLibraryToSGC)))
	mux.HandleFunc("/sgc/remove-library", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSGCRemoveLibrary)))
	mux.HandleFunc("/sgc/api/available-libraries", app.auth.RequireAuthFunc(app.withAccessToken(app.handleSGCAvailableLibraries)))

	// Backup config management
	mux.HandleFunc("/backup-configs/create", app.auth.RequireAuthFunc(app.withAccessToken(app.handleBackupConfigCreate)))
	mux.HandleFunc("/backup-configs/", app.auth.RequireAuthFunc(app.withAccessToken(app.handleBackupConfigDelete)))

	// API endpoints for HTMX partial updates
	mux.HandleFunc("/api/dashboard-summary", app.auth.RequireAuthFunc(app.withAccessToken(app.handleDashboardSummary)))
	mux.HandleFunc("/api/dashboard-sessions", app.auth.RequireAuthFunc(app.withAccessToken(app.handleDashboardSessions)))
}
// handleSGCRoutes dispatches /sgc/* routes
func (app *App) handleSGCRoutes(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /sgc/{id}/backup/trigger
	if len(pathParts) >= 4 && pathParts[2] == "backup" && pathParts[3] == "trigger" {
		app.handleTriggerBackup(w, r)
		return
	}
	app.handleSGCDetail(w, r)
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

// buildTemplLayoutData builds components.LayoutData for templ pages
func (app *App) buildTemplLayoutData(r *http.Request, title, active string, user *htmxauth.UserInfo, breadcrumbs []components.Breadcrumb) (components.LayoutData, error) {
	ctx := context.Background()
	servers, err := app.grpc.ListServers(ctx)
	if err != nil {
		log.Printf("Error fetching servers for layout: %v", err)
		servers = []*manmanpb.Server{}
	}

	selectedServer := app.getSelectedServer(r, servers)

	return components.LayoutData{
		Title:          title,
		Active:         active,
		User:           user,
		Servers:        servers,
		SelectedServer: selectedServer,
		Breadcrumbs:    breadcrumbs,
	}, nil
}
