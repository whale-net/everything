// Command mcp is Audience Score System's MCP server -- the only interface
// for C4, C5, C6 (read), C7, C9 (confirm/reject, manual sync trigger), and
// C10 (NFR3). See ../ARCHITECTURE.md's "MCP server" and "NFR3 interface
// allocation" for why every other capability is MCP-only and how a caller
// authenticates.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/audience_score_system/mcp/server"
	"github.com/whale-net/everything/audience_score_system/mcp/tools"
	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/worker/sync"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/mcpauth"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
)

// scheduleManagerInterval is passed to sync.NewScheduleManager purely to
// satisfy its signature -- `mcp` only ever calls TriggerNow (via the
// trigger_channel_sync tool, issue #1650), never EnsureSchedule/Reconcile,
// so this value is never read. It is NOT ASS_SYNC_INTERVAL: `mcp` does not
// read that variable (see ../ENV.md "Temporal"), and this constant must
// satisfy sync.ValidateSyncInterval only so a future caller relying on the
// same construction can't be surprised by an out-of-band value here.
const scheduleManagerInterval = 3 * time.Hour

// config holds `mcp`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	MCPAddr string

	DatabaseURL string
	LogLevel    string

	// MCPPublicURL is ASS_MCP_PUBLIC_URL (issue #1646, FR12/NFR4) -- this
	// instance's own externally reachable URL, passed as
	// mcpauth.ProtectedResourceMetadataConfig.Resource. Must equal `web`'s
	// mcpauth.ProviderConfig.Resource exactly.
	MCPPublicURL string

	// OAuthIssuer is ASS_OAUTH_REDIRECT_BASE_URL (issue #1646, FR12/NFR4)
	// -- `web`'s OAuth2 issuer identifier, named here as the
	// authorization_servers entry `mcp`'s protected-resource metadata
	// advertises. `mcp` never talks to this URL itself; it only publishes
	// it for MCP clients to follow.
	OAuthIssuer string
}

// loadConfig loads configuration from environment variables. See
// ../ENV.md for the full variable list.
func loadConfig() config {
	return config{
		MCPAddr:      getEnv("ASS_MCP_ADDR", ":8081"),
		DatabaseURL:  os.Getenv("PG_DATABASE_URL"),
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		MCPPublicURL: os.Getenv("ASS_MCP_PUBLIC_URL"),
		OAuthIssuer:  os.Getenv("ASS_OAUTH_REDIRECT_BASE_URL"),
	}
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := loadConfig()

	logLevel := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}

	logging.Configure(logging.Config{
		ServiceName:   "audience-score-system-mcp",
		Domain:        "audience-score-system",
		Level:         logLevel,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	if cfg.DatabaseURL == "" {
		return fmt.Errorf("PG_DATABASE_URL is required")
	}
	if cfg.MCPPublicURL == "" {
		return fmt.Errorf("ASS_MCP_PUBLIC_URL is required")
	}
	if cfg.OAuthIssuer == "" {
		return fmt.Errorf("ASS_OAUTH_REDIRECT_BASE_URL is required")
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer pool.Close()

	st := store.New(pool)

	// mcpauth.NewCredentialStore preflights the mcp_credential table at
	// boot -- a missing migration 006 fails `mcp` at startup instead of at
	// first bearer-token verification (see ../ENV.md's mcp_credential
	// entry).
	creds, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{
		Pool:           pool,
		TableName:      "mcp_credential",
		IdentityColumn: "person_id",
		IdentityCast:   "uuid",
	})
	if err != nil {
		return fmt.Errorf("mcpauth credential store: apply migration 006_mcpauth_credential before starting mcp: %w", err)
	}

	// `mcp` constructs its own Temporal client and sync.ScheduleManager,
	// the same pattern `web` (issue #1614) and `worker` already use --
	// trigger_channel_sync (issue #1650) needs ScheduleManager.TriggerNow
	// to force an out-of-band ChannelSyncWorkflow run. `mcp` hard-depends
	// on Temporal being reachable at startup the same way it already
	// hard-depends on Postgres above.
	temporalCfg := temporallib.ConfigFromEnv()
	if temporalCfg.TaskQueue == "" {
		temporalCfg.TaskQueue = sync.TaskQueue
	}
	logger.Info("connecting to temporal", "host_port", temporalCfg.HostPort, "namespace", temporalCfg.Namespace, "task_queue", temporalCfg.TaskQueue)
	temporalClient, err := temporallib.NewClient(temporalCfg, temporallib.NewLogger("audience-score-system-mcp"))
	if err != nil {
		return fmt.Errorf("connect to temporal: %w", err)
	}
	defer temporalClient.Close()

	scheduleManager := sync.NewScheduleManager(temporalClient.ScheduleClient(), st.Channels(), scheduleManagerInterval)

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterWhoami(reg)
	tools.RegisterListChannels(reg, st.Roles())
	tools.RegisterResearch(reg, st)
	tools.RegisterVerdict(reg, st)
	tools.RegisterGetChannelSchedule(reg, st.Sync())
	tools.RegisterScheduleDraft(reg, st)
	tools.RegisterMatches(reg, st)
	tools.RegisterBrowse(reg, st)
	tools.RegisterStrategy(reg, st)
	tools.RegisterTriggerChannelSync(reg, st.Channels(), scheduleManager)

	handler := server.NewHTTPHandler(srv, creds, server.ResourceMetadataConfig{
		Resource:            cfg.MCPPublicURL,
		AuthorizationServer: cfg.OAuthIssuer,
		ResourceName:        "Audience Score System MCP",
	})

	httpServer := &http.Server{
		Addr:         cfg.MCPAddr,
		Handler:      otelhttp.NewHandler(handler, "audience-score-system-mcp"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.MCPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown signal received, draining in-flight requests")

	drainCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(drainCtx); err != nil {
		logger.Warn("graceful shutdown did not complete cleanly", "error", err)
	}
	return nil
}
