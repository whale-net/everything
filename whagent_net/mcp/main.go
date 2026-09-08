// Command mcp is whagent-net's MCP surface: the front door an operator
// drives from Claude Code (ARCHITECTURE.md "Open items": "`mcp` from
// Claude Code is the v1 answer"). It is a thin, faithful facade over
// `api`'s SessionService gRPC service (issue #2113,
// ARCHITECTURE.md "Service boundary vs. package boundary") -- every
// tool call is a pass-through RPC to `api`, authenticated as the
// operator who made it, never a shared service account (FR10). This
// binary never talks to Postgres or Temporal directly.
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

	"github.com/whale-net/everything/libs/go/grpcclient"
	"github.com/whale-net/everything/libs/go/logging"
	pb "github.com/whale-net/everything/whagent_net/protos"

	"github.com/whale-net/everything/whagent_net/mcp/server"
	"github.com/whale-net/everything/whagent_net/mcp/tools"
)

// config holds `mcp`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// MCPAddr is the address this binary's streamable-HTTP MCP surface
	// listens on.
	MCPAddr string

	// APIAddr is `api`'s gRPC address (WHAGENT_API_URL, ../ENV.md
	// "Service wiring") -- the only outbound dependency this binary
	// dials.
	APIAddr string
}

func loadConfig() config {
	return config{
		MCPAddr: getEnv("WHAGENT_MCP_ADDR", ":8082"),
		APIAddr: os.Getenv("WHAGENT_API_URL"),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := loadConfig()

	logging.Configure(logging.Config{
		ServiceName:   "whagent-net-mcp",
		Domain:        "whagent-net",
		Level:         slog.LevelInfo,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	if cfg.APIAddr == "" {
		return fmt.Errorf("WHAGENT_API_URL is required")
	}

	// mcp is a pure facade over api's SessionService: the only thing it
	// dials is api's own gRPC address -- it never connects to Postgres or
	// Temporal itself (ARCHITECTURE.md "Service boundary vs. package
	// boundary").
	apiConn, err := grpcclient.NewClient(ctx, cfg.APIAddr)
	if err != nil {
		return fmt.Errorf("dial api at %s: %w", cfg.APIAddr, err)
	}
	defer apiConn.Close() //nolint:errcheck

	client := pb.NewSessionServiceClient(apiConn.GetConnection())

	srv := server.New()
	tools.RegisterStartSession(srv, client)
	tools.RegisterSendTurn(srv, client)
	tools.RegisterStopSession(srv, client)
	tools.RegisterGetSession(srv, client)
	tools.RegisterReadTranscript(srv, client)

	httpServer := &http.Server{
		Addr:         cfg.MCPAddr,
		Handler:      otelhttp.NewHandler(server.NewHTTPHandler(srv), "whagent-net-mcp"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.MCPAddr, "api_addr", cfg.APIAddr)
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
