// Command mcp is Audience Score System's MCP server -- the only interface
// for C4, C5, C6 (read), C7, C9 (confirm/reject), and C10 (NFR3). See
// ../ARCHITECTURE.md's "MCP server" and "NFR3 interface allocation" for why
// every other capability is MCP-only and how a caller authenticates.
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
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
)

// config holds `mcp`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	MCPAddr string

	DatabaseURL string
	LogLevel    string
}

// loadConfig loads configuration from environment variables. See
// ../ENV.md for the full variable list.
func loadConfig() config {
	return config{
		MCPAddr:     getEnv("ASS_MCP_ADDR", ":8081"),
		DatabaseURL: os.Getenv("PG_DATABASE_URL"),
		LogLevel:    getEnv("LOG_LEVEL", "info"),
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

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer pool.Close()

	st := store.New(pool)

	srv := server.New(st)
	reg := server.NewRegistry(srv, st)
	tools.RegisterWhoami(reg)
	tools.RegisterResearch(reg, st)
	tools.RegisterVerdict(reg, st)
	tools.RegisterGetChannelSchedule(reg, st.Sync())
	tools.RegisterScheduleDraft(reg, st)

	handler := server.NewHTTPHandler(srv, st.Credentials())

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
