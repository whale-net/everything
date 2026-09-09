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
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/events"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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

	// RabbitMQURL backs the SSE hub's live-status consumer (manmanv2.htmxsse
	// exchange, see initializeSSEHub). Unset or unreachable degrades to
	// sseHub == nil rather than failing boot (NFR3/NFR8) -- see
	// initializeSSEHub's doc comment.
	RabbitMQURL string

	// SSE hub tuning, named with the MANMANV2_SSE_ prefix mirroring
	// app-registry's APP_REGISTRY_SSE_* (tools/app_registry/ui/main.go).
	// SSEHeartbeatInterval must stay > SSEAdvertisedRetryInterval/2, or
	// initializeSSEHub falls back to htmxsse.DefaultConfig() -- see its doc
	// comment and ENV.md.
	SSEHeartbeatInterval       time.Duration
	SSEMaxStreamLifetime       time.Duration
	SSESubscriberBufferDepth   int
	SSEAdvertisedRetryInterval time.Duration
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
		RabbitMQURL:      getEnv("RABBITMQ_URL", ""),

		SSEHeartbeatInterval:       getEnvDuration("MANMANV2_SSE_HEARTBEAT_INTERVAL", 5*time.Second),
		SSEMaxStreamLifetime:       getEnvDuration("MANMANV2_SSE_MAX_STREAM_LIFETIME", 1*time.Hour),
		SSESubscriberBufferDepth:   getEnvInt("MANMANV2_SSE_SUBSCRIBER_BUFFER_DEPTH", 100),
		SSEAdvertisedRetryInterval: getEnvDuration("MANMANV2_SSE_ADVERTISED_RETRY_INTERVAL", 2*time.Second),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvDuration parses key via time.ParseDuration (e.g. "5s", "1m"). An
// unset or unparseable value falls back to defaultValue rather than failing
// boot -- mirrors tools/app_registry/ui/main.go's own getEnvDuration.
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		log.Printf("invalid %s=%q (expected a duration like \"5s\"); using default %v", key, value, defaultValue)
		return defaultValue
	}
	return parsed
}

// getEnvInt parses key via strconv.Atoi. An unset or unparseable value falls
// back to defaultValue rather than failing boot.
func getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("invalid %s=%q (expected an integer); using default %d", key, value, defaultValue)
		return defaultValue
	}
	return parsed
}

// App holds the application state
type App struct {
	config       *Config
	auth         *htmxauth.Authenticator
	grpc         *ControlClient
	logProcessor manmanpb.LogProcessorClient
	userAuthOpt  grpc.DialOption
	// sseHub backs /api/live/deployments (handlers_sessions_live.go). nil
	// when RABBITMQ_URL is unset or the broker was unreachable at startup
	// (initializeSSEHub) -- the route handler must degrade to 503 rather
	// than dereference a nil hub (NFR3/NFR8).
	sseHub *htmxsse.Hub

	// deploymentActionTimeout overrides boundDeploymentRPC's bound around
	// Stop/Restart/Start's own outbound StopSession/StartSession RPC call
	// (#1664 defense-in-depth, hardened and extended to Start by #1668,
	// well under main.go's 15s WriteTimeout). Zero value means "use the
	// production default" -- see handlers_deployment_actions.go's
	// deploymentActionBound -- so only tests that need a fast timeout set
	// this.
	deploymentActionTimeout time.Duration
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

	sseHub := initializeSSEHub(ctx, config)

	return &App{
		config:       config,
		auth:         auth,
		grpc:         grpcClient,
		logProcessor: logProcessorClient,
		userAuthOpt:  userAuthOpt,
		sseHub:       sseHub,
	}, nil
}

// initializeSSEHub dials RabbitMQ and builds the Hub backing
// /api/live/deployments (FR6-FR8). Mirrors
// tools/app_registry/ui/main.go's initializeSSEHub.
//
// RABBITMQ_URL unset or the broker unreachable must not prevent the UI from
// starting or serving /sessions (NFR3/NFR8 degradation): this returns nil
// in that case, logged as a WARNING, and handleDeploymentsLiveSSE responds
// 503 so the client's reconnect loop retries. Attach is lazy in htmxsse
// (Hub.Subscribe triggers it), so an unreachable-but-configured broker
// already degrades correctly on its own once a connection is returned here.
func initializeSSEHub(ctx context.Context, config *Config) *htmxsse.Hub {
	if config.RabbitMQURL == "" {
		log.Printf("WARNING: RABBITMQ_URL not set; live deployment updates (/api/live/deployments) disabled")
		return nil
	}

	conn, err := rmq.NewConnectionFromURL(config.RabbitMQURL)
	if err != nil {
		log.Printf("WARNING: failed to connect to RabbitMQ at %s; live deployment updates (/api/live/deployments) disabled: %v", config.RabbitMQURL, err)
		return nil
	}

	// events.DeclareArgs() matches htmxsse.DefaultAttachFunc's own hardcoded
	// declare call byte-for-byte (topic/durable=true/autoDelete=false/
	// internal=false/noWait=false/args=nil) -- see events.go's doc comment --
	// so the library's default attach func is used directly rather than a
	// local copy that could drift.
	attachFunc := htmxsse.DefaultAttachFunc(events.ExchangeName, conn)

	hubConfig := htmxsse.DefaultConfig()
	hubConfig.ExchangeName = events.ExchangeName
	hubConfig.HeartbeatInterval = config.SSEHeartbeatInterval
	hubConfig.MaxStreamLifetime = config.SSEMaxStreamLifetime
	hubConfig.SubscriberBufferDepth = config.SSESubscriberBufferDepth
	hubConfig.AdvertisedRetryInterval = config.SSEAdvertisedRetryInterval

	if hubConfig.HeartbeatInterval <= 0 {
		// time.NewTicker (htmxsse.Handler's heartbeat ticker) panics for
		// d<=0 -- see tools/app_registry/ui/main.go's identical guard.
		log.Printf("invalid MANMANV2_SSE_HEARTBEAT_INTERVAL=%q (must be positive); using default %v", os.Getenv("MANMANV2_SSE_HEARTBEAT_INTERVAL"), htmxsse.DefaultConfig().HeartbeatInterval)
		hubConfig.HeartbeatInterval = htmxsse.DefaultConfig().HeartbeatInterval
	}
	if err := hubConfig.Validate(); err != nil {
		log.Printf("invalid SSE hub config (%v); falling back to library defaults", err)
		hubConfig = htmxsse.DefaultConfig()
		hubConfig.ExchangeName = events.ExchangeName
	}

	return htmxsse.NewHub(attachFunc, hubConfig)
}

// Close cleans up application resources
func (app *App) Close() error {
	if app.sseHub != nil {
		if err := app.sseHub.Close(); err != nil {
			log.Printf("Error closing SSE hub: %v", err)
		}
	}
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
	// Registered ahead of (and more specific than) the "/sessions/" catch-all
	// above: Go's ServeMux dispatches on longest-matching-pattern, so
	// "/sessions/deployments/" wins over "/sessions/" for these paths and
	// never reaches handleSessionDetail, which parses path segments
	// positionally and would otherwise try (and fail) to parse
	// "deployments" as a session id (#1627).
	mux.HandleFunc("/sessions/deployments/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDeploymentAction)))
	mux.HandleFunc("/sessions/start", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionStart)))
	mux.HandleFunc("/api/sessions/check-active", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleCheckActiveSession)))
	mux.HandleFunc("/api/sessions/historical-logs", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleHistoricalLogs)))
	mux.HandleFunc("/api/sessions/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleSessionStdin)))
	// "/api/deployments/" is a distinct prefix from "/api/sessions/" above
	// (handleSessionStdin's catch-all), so the row's self-polling GET
	// (#1628) never collides with it under Go's ServeMux
	// longest-pattern-wins dispatch.
	mux.HandleFunc("/api/deployments/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDeploymentRowFragment)))
	// SSE route for live per-deployment row updates (FR6, FR7, FR8,
	// handlers_sessions_live.go). Deliberately a fresh "/api/live/" prefix,
	// distinct from "/api/sessions/" and "/api/deployments/" above, so it
	// never depends on ServeMux precedence against either catch-all.
	// Wrapped with RequireAuthFunc only, never WithAccessToken -- the latter
	// redirects (or sends HX-Redirect + 401) on a stale token, which would
	// corrupt an established SSE stream; the token is re-acquired per
	// delivery inside the fragment instead.
	mux.HandleFunc("/api/live/deployments", app.auth.RequireAuthFunc(app.handleDeploymentsLiveSSE))

	// Note: Log streaming endpoint is handled by handleSessionDetail which routes to handleSessionLogsStream

	// Activity: fleet-wide Live now/History view (FR14, task #2271). A
	// fresh top-level path -- no collision with "/sessions/", "/sgc/" or
	// "/games/"'s catch-alls, so it carries none of those routes'
	// longest-pattern-wins precedence concerns (see the "/sessions/
	// deployments/" comment above for the shape of that bite).
	mux.HandleFunc("/activity", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleActivity)))

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
	mux.HandleFunc("/workshop/batch-status", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopBatchStatus)))
	mux.HandleFunc("/workshop/cache", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopCache)))
	mux.HandleFunc("/workshop/cache/verify", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopCacheVerify)))
	mux.HandleFunc("/workshop/cache/evict", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleWorkshopCacheEvict)))
	mux.HandleFunc("/workshop/bulk-add-collection", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBulkAddCollection)))
	mux.HandleFunc("/workshop/batch-create-addons", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBatchCreateAddons)))

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
	// /sgc/{id}/random-port (task #2098, FR13): one random in-range,
	// not-in-use host port for the ports editor's random affordance.
	if len(pathParts) >= 3 && pathParts[2] == "random-port" {
		app.handleSGCRandomPort(w, r, pathParts[1])
		return
	}
	// /sgc/{id}/env/set, /sgc/{id}/env/remove, /sgc/{id}/env/edit
	// (task #2090: deployment-level environment overrides, FR2/FR4).
	if len(pathParts) >= 4 && pathParts[2] == "env" {
		switch pathParts[3] {
		case "set":
			app.handleSGCEnvSet(w, r, pathParts[1])
			return
		case "remove":
			app.handleSGCEnvRemove(w, r, pathParts[1])
			return
		case "edit":
			app.handleSGCEnvEdit(w, r, pathParts[1])
			return
		}
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
