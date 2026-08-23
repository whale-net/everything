package main

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/logging"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"github.com/whale-net/everything/manmanv2/ui/components"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

//go:embed favicon.ico
var faviconIco []byte

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
		store, err := htmxauth.NewDBSessionManager(ctx, pool, config.SessionSecret, "manmanv2_ui_session")
		if err != nil {
			return nil, fmt.Errorf("failed to initialize session store: %w", err)
		}
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
	logProcessorConn, err := grpcclient.NewClient(ctx, config.LogProcessorURL, userAuthOpt)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to log-processor: %w", err)
	}
	logProcessorClient := manmanpb.NewLogProcessorClient(logProcessorConn.GetConnection())

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

	ctx := context.Background()

	logging.Configure(logging.Config{
		ServiceName:   "manmanv2-ui",
		Domain:        "manmanv2",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck

	// Create application
	app, err := NewApp(ctx, config)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Close()

	// Setup HTTP server
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	// Create server — wrap mux with otelhttp so every HTTP request gets a span.
	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      otelhttp.NewHandler(mux, "manmanv2-ui"),
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

// Note: withAccessToken was hoisted to htmxauth.Authenticator.WithAccessToken
// (convergence spike, #998 FR9 point 1) — it was byte-for-byte identical to
// tools/app_registry/ui's copy. Call sites use app.auth.WithAccessToken.

func (app *App) setupRoutes(mux *http.ServeMux) {
	// Public routes
	mux.HandleFunc("/favicon.ico", htmxbase.FaviconHandler(faviconIco))
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Server selection endpoint
	mux.HandleFunc("/select-server", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSelectServer)))

	// Protected routes - Home/Dashboard
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHome)))
	mux.HandleFunc("/sessions", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessions)))
	mux.HandleFunc("/sessions/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionDetail)))
	mux.HandleFunc("/sessions/start", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionStart)))
	mux.HandleFunc("/api/sessions/check-active", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleCheckActiveSession)))
	mux.HandleFunc("/api/sessions/historical-logs", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHistoricalLogs)))
	mux.HandleFunc("/api/sessions/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionStdin)))

	// Note: Log streaming endpoint is handled by handleSessionDetail which routes to handleSessionLogsStream

	// Protected routes - Games
	mux.HandleFunc("/games", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleGames)))
	mux.HandleFunc("/games/new", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleGameNew)))
	mux.HandleFunc("/games/create", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleGameCreate)))
	mux.HandleFunc("/games/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleGameDetail)))

	// Note: Config routes are handled within handleGameDetail based on URL parsing
	// Note: Action management routes are also handled within handleGameDetail and handleGameConfigDetail

	// Documentation routes
	mux.HandleFunc("/docs/config-strategies", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleConfigStrategiesDocs)))

	// Protected routes - Servers
	mux.HandleFunc("/servers", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleServers)))
	mux.HandleFunc("/servers/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleServerDetail)))

	// Protected routes - Workshop
	mux.HandleFunc("/workshop/library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopLibrary)))
	mux.HandleFunc("/workshop/search", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopSearch)))
	mux.HandleFunc("/workshop/addon", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopAddonDetail)))
	mux.HandleFunc("/workshop/library-detail", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleLibraryDetail)))
	mux.HandleFunc("/workshop/create-library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleCreateLibrary)))
	mux.HandleFunc("/workshop/delete-library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDeleteLibrary)))
	mux.HandleFunc("/workshop/add-addon-to-library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAddAddonToLibrary)))
	mux.HandleFunc("/workshop/remove-addon-from-library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleRemoveAddonFromLibrary)))
	mux.HandleFunc("/workshop/add-library-reference", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAddLibraryReference)))
	mux.HandleFunc("/workshop/remove-library-reference", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleRemoveLibraryReference)))
	mux.HandleFunc("/workshop/installations", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopInstallations)))
	mux.HandleFunc("/workshop/install", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleInstallAddon)))
	mux.HandleFunc("/workshop/remove", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleRemoveInstallation)))
	mux.HandleFunc("/workshop/reset", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleResetInstallation)))
	mux.HandleFunc("/workshop/fetch-metadata", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleFetchAddonMetadata)))
	mux.HandleFunc("/workshop/create-addon", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleCreateAddon)))
	mux.HandleFunc("/workshop/update-addon-details", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleUpdateAddonDetails)))
	mux.HandleFunc("/workshop/update-library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleUpdateLibrary)))
	mux.HandleFunc("/workshop/delete-addon", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDeleteAddon)))
	mux.HandleFunc("/workshop/api/available-addons", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAvailableAddons)))
	mux.HandleFunc("/workshop/api/available-libraries", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAvailableLibraries)))
	mux.HandleFunc("/workshop/api/presets-for-game", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handlePresetsForGame)))

	// Protected routes - SGC detail
	mux.HandleFunc("/sgc/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSGCRoutes)))
	mux.HandleFunc("/sgc/add-library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAddLibraryToSGC)))
	mux.HandleFunc("/sgc/remove-library", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSGCRemoveLibrary)))
	mux.HandleFunc("/sgc/api/available-libraries", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSGCAvailableLibraries)))

	// Backup config management
	mux.HandleFunc("/backup-configs/create", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBackupConfigCreate)))
	mux.HandleFunc("/backup-configs/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBackupConfigDelete)))

	// API endpoints for HTMX partial updates
	mux.HandleFunc("/api/dashboard-summary", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDashboardSummary)))
	mux.HandleFunc("/api/dashboard-sessions", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDashboardSessions)))
}
// handleSGCRoutes dispatches /sgc/* routes
func (app *App) handleSGCRoutes(w http.ResponseWriter, r *http.Request) {
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// /sgc/{id}/backup/trigger
	if len(pathParts) >= 4 && pathParts[2] == "backup" && pathParts[3] == "trigger" {
		app.handleTriggerBackup(w, r)
		return
	}
	// /sgc/{id}/update-ports
	if len(pathParts) >= 3 && pathParts[2] == "update-ports" {
		app.handleSGCUpdatePorts(w, r, pathParts[1])
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

// buildTemplLayoutData builds components.LayoutData for templ pages
func (app *App) buildTemplLayoutData(r *http.Request, title, active string, user *htmxauth.UserInfo, breadcrumbs []components.Breadcrumb) (components.LayoutData, error) {
	servers, err := app.grpc.ListServers(r.Context())
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
