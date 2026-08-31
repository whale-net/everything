// Command app-registry-ui is the App Registry's HTMX admin UI. It records
// promotions; it never deploys anything (NFR-1). All registry domain data
// comes over gRPC from app-registry-api, forwarding the logged-in user's
// own access token (FR-40) — this package's only direct database use is
// htmxauth's session storage (NFR-3), never registry domain tables.
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
	"google.golang.org/grpc"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/htmxsse"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/tools/app_registry/events"
)

//go:embed favicon.ico
var faviconIco []byte

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

	// app-registry-api gRPC endpoint
	RegistryAPIURL string
	// gRPC auth mode for forwarding the user's own token to app-registry-api
	GRPCAuthMode string

	// DatabaseURL backs htmxauth's DB-backed session manager. Always
	// required — unlike manmanv2/ui, this UI never falls back to
	// cookie-only sessions (see NewApp).
	DatabaseURL string

	// ShowDemoDomain controls whether the "demo" domain's apps/charts
	// appear in the Apps Catalog (issue #750). Mirrors release.yml's
	// include_demo workflow_dispatch input / resolveReleaseScope's
	// includeDemo param (release_scope.go's demoDomain const) — "demo" is
	// hidden from this UI by default, with an explicit opt-in to show it.
	ShowDemoDomain bool
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
		RegistryAPIURL:            getEnv("REGISTRY_API_URL", "app-registry-api:50051"),
		GRPCAuthMode:              strings.ToLower(getEnv("GRPC_AUTH_MODE", "none")),
		// PG_DATABASE_URL matches the variable every other App Registry
		// component reads (see ../ENV.md) — deliberately not "DATABASE_URL".
		DatabaseURL:    getEnv("PG_DATABASE_URL", ""),
		ShowDemoDomain: getEnvBool("APP_REGISTRY_UI_SHOW_DEMO_DOMAIN", false),
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvBool parses key via strconv.ParseBool ("1"/"t"/"true"/"0"/"f"/
// "false", case-insensitive, per the stdlib). An unset or unparseable value
// falls back to defaultValue rather than failing boot — this only gates a
// UI display default, not a required credential.
func getEnvBool(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		log.Printf("invalid %s=%q (expected true/false); using default %v", key, value, defaultValue)
		return defaultValue
	}
	return parsed
}

// getEnvDuration parses key via time.ParseDuration (e.g. "5s", "1m"). An
// unset or unparseable value falls back to defaultValue rather than failing
// boot — mirrors worker/main.go's own getEnvDuration.
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

// App holds the application state.
type App struct {
	config      *Config
	auth        *htmxauth.Authenticator
	registry    *RegistryClient
	userAuthOpt grpc.DialOption
	sessionMgr  *htmxauth.DBSessionManager // Retained for FR27 session re-check on failure path
	sseHub      *htmxsse.Hub               // Hub for SSE connections
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
		Mode:                      authMode,
		SessionSecret:             config.SessionSecret,
		SessionName:               "app_registry_ui_session",
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
	sseHub := initializeSSEHub(ctx)

	return &App{
		config:      config,
		auth:        auth,
		registry:    registry,
		userAuthOpt: userAuthOpt,
		sessionMgr:  store, // Retain for FR27 failure discrimination
		sseHub:      sseHub,
	}, nil
}

// initializeSSEHub creates and initializes the SSE Hub with message broker
// attachment. If attachment fails, returns a Hub that will attempt to attach
// asynchronously. Startup failure does not fail the overall application boot
// (FR1b, NFR7).
func initializeSSEHub(ctx context.Context) *htmxsse.Hub {
	// Get RabbitMQ connection string from environment
	brokerURL := os.Getenv("RABBITMQ_URL")
	if brokerURL == "" {
		brokerURL = "amqp://guest:guest@localhost:5672/"
	}

	// Create the attach function using the events package's exchange config
	attachFunc := func(ctx context.Context) (htmxsse.Transport, error) {
		conn, err := rmq.NewConnectionFromURL(brokerURL)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
		}

		// Declare the exchange using app-registry events package config
		ch, err := conn.Channel()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to get channel: %w", err)
		}

		kind, durable, autoDelete, internal, noWait, args := events.DeclareArgs()
		err = ch.ExchangeDeclare(events.ExchangeName, kind, durable, autoDelete, internal, noWait, args)
		ch.Close()
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to declare exchange %q: %w", events.ExchangeName, err)
		}

		// Create ephemeral consumer for this process instance
		return rmq.NewConsumerWithOpts(conn, "", false, true, 0, 0)
	}

	config := htmxsse.DefaultConfig()
	config.ExchangeName = events.ExchangeName
	// Broker-pushed updates land immediately regardless of this value; the
	// heartbeat is only the worst-case fallback a client falls back to when
	// no broker event arrives for a state change it's waiting on (e.g. a
	// dropped/never-published event, or RABBITMQ_URL unset entirely -- see
	// ENV.md's "graceful degradation" note). The library default (30s,
	// htmxsse.DefaultConfig) reads as a stuck page for that fallback path
	// (issue #1537); five seconds keeps the same never-block guarantees at
	// a much smaller worst case, still overridable per-deployment.
	config.HeartbeatInterval = getEnvDuration("APP_REGISTRY_SSE_HEARTBEAT_INTERVAL", 5*time.Second)
	if config.HeartbeatInterval <= 0 {
		// time.NewTicker (htmxsse.Handler's heartbeat ticker) panics for
		// d<=0, and Validate() below only checks the retry/heartbeat ratio
		// when both fields are >0 -- so an operator-set "0s"/"-1s" would
		// otherwise sail through Validate() and panic on the first SSE
		// connection instead of falling back here.
		log.Printf("invalid APP_REGISTRY_SSE_HEARTBEAT_INTERVAL=%q (must be positive); using default %v", os.Getenv("APP_REGISTRY_SSE_HEARTBEAT_INTERVAL"), htmxsse.DefaultConfig().HeartbeatInterval)
		config.HeartbeatInterval = htmxsse.DefaultConfig().HeartbeatInterval
	}
	// Validate() requires AdvertisedRetryInterval < 2*HeartbeatInterval.
	// DefaultConfig()'s AdvertisedRetryInterval (5s) alone would fail that
	// check for any override below ~2.5s, reverting the whole config back
	// to the library's 30s heartbeat default -- silently reintroducing the
	// exact staleness issue #1537 was filed over. Scale it down with the
	// override instead of leaving it fixed.
	if config.AdvertisedRetryInterval >= 2*config.HeartbeatInterval {
		config.AdvertisedRetryInterval = config.HeartbeatInterval / 2
	}
	if err := config.Validate(); err != nil {
		log.Printf("invalid SSE hub config (%v); falling back to library defaults", err)
		config = htmxsse.DefaultConfig()
		config.ExchangeName = events.ExchangeName
	}
	hub := htmxsse.NewHub(attachFunc, config)
	return hub
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

// Note: withAccessToken was hoisted to htmxauth.Authenticator.WithAccessToken
// (convergence spike, #998 FR9 point 1) — it was byte-for-byte identical to
// manmanv2/ui's copy. Call sites use app.auth.WithAccessToken.

func (app *App) setupRoutes(mux *http.ServeMux) {
	// Public routes — must sit outside the auth wrapper, or the sign-in
	// page itself would redirect to sign-in.
	mux.HandleFunc("/favicon.ico", htmxbase.FaviconHandler(faviconIco))
	mux.HandleFunc("/health", app.handleHealth)
	mux.HandleFunc("/auth/login", app.auth.HandleLogin)
	mux.HandleFunc("/auth/callback", app.auth.HandleCallback)
	mux.HandleFunc("/auth/logout", app.auth.HandleLogout)

	// Protected routes.
	mux.HandleFunc("/", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDashboard)))
	mux.HandleFunc("/deployments", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDeployments)))
	// Promotion Details (FR7-FR9, issue #1032): reached via a per-event link
	// from the promotion timeline (pages/app_detail.templ's
	// promotionTimelineCard), not from top-level nav.
	mux.HandleFunc("/promotions/{id}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handlePromotionDetails)))
	// FR10-FR13 manual retry (issue #1033): admin-gated server-side by
	// RetryArgoSync's own auth.Require check, not by this route's
	// (identical-to-every-other-route) session-login requirement.
	mux.HandleFunc("/promotions/{id}/retry", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleRetryArgoSync)))
	// SSE route for promotion status updates (FR6, FR27, FR28, NFR4, NFR13).
	// Wrapped with noRedirectWriter to prevent redirects mid-stream.
	// Does NOT use WithAccessToken (which redirects), instead re-acquires token per read.
	mux.HandleFunc("/promotions/{id}/status/sse", func(w http.ResponseWriter, r *http.Request) {
		// Wrap with noRedirectWriter before RequireAuthFunc processes the request
		// This ensures auth failures return 401 with no redirect headers/body
		w = newNoRedirectWriter(w)
		app.auth.RequireAuthFunc(func(w http.ResponseWriter, r *http.Request) {
			app.handlePromoStatusSSE(w, r)
		})(w, r)
	})
	mux.HandleFunc("/apps", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAppsCatalog)))
	mux.HandleFunc("/apps/{id}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAppDetail)))

	// Screens 12/13/21/22 (#645): environment diff, app version history,
	// artifact detail, chart detail.
	mux.HandleFunc("/environments/diff", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleEnvironmentDiff)))
	mux.HandleFunc("/apps/{id}/history", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleAppHistory)))
	mux.HandleFunc("/artifacts/{digest}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleArtifactDetail)))
	mux.HandleFunc("/charts/{id}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleChartDetail)))
	mux.HandleFunc("/charts/{id}/argo-override", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleChartSetArgoOverride)))

	// Screens 30/31/32 (#649): builds, build detail, reconcile runs.
	mux.HandleFunc("/builds", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBuilds)))
	mux.HandleFunc("/builds/lookup", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBuildLookup)))
	mux.HandleFunc("/builds/{id}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleBuildDetail)))
	mux.HandleFunc("/reconcile-runs", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleReconcileRuns)))

	// Screens 09/53 (#646): environments list, create/edit/archive form.
	mux.HandleFunc("/environments", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleEnvironments)))
	mux.HandleFunc("/environments/new", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleEnvironmentNew)))
	mux.HandleFunc("/environments/edit", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleEnvironmentEdit)))
	mux.HandleFunc("/environments/save", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleEnvironmentSave)))
	mux.HandleFunc("/environments/archive", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleEnvironmentArchive)))

	// Screen 50 (#647): promote, opened pre-scoped to (entity, environment)
	// from a deployments-matrix cell (FR-6) — GET renders the form, POST
	// handles both the dry-run and commit submit buttons.
	mux.HandleFunc("/promote", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handlePromote)))

	// Screen 51 (#648): rollback, opened pre-scoped to (entity,
	// environment) from a deployments-matrix cell (FR-6) — GET renders the
	// SCD2-derived confirmation (FR-17), POST handles the commit.
	mux.HandleFunc("/rollback", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleRollback)))

	// Screen 40 (#650): drift and adoption audit — read-side only, no
	// write control (the adopt action, screen 52, is deferred).
	mux.HandleFunc("/drift-audit", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleDriftAudit)))

	// Release trigger + status (#890, FR1-5/FR10): GET renders the scope
	// form, POST resolves it (FR1); status is a durable page keyed by
	// release_run_id. "/releases" (list) and "/releases/trigger" (both
	// literal paths) take precedence over the "/releases/{id}" wildcard for
	// their exact segments, the same precedent "/builds/lookup" vs.
	// "/builds/{id}" already relies on above.
	mux.HandleFunc("/releases", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleReleaseHistory)))
	mux.HandleFunc("/releases/trigger", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleReleaseTrigger)))
	mux.HandleFunc("/releases/{id}", app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleReleaseStatus)))
}

func (app *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}
