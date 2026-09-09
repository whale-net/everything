// Command ui is whagent-net's standalone agent web UI (M2, issue #2236):
// a Go html/template + templ + HTMX surface, distinct from `api`'s gRPC
// surface and `mcp`'s MCP surface. This scaffold stands up the binary,
// its config, and its HTTP mux with graceful shutdown -- Keycloak sign-in
// (NFR1), the authenticated `api` client, and real session pages are
// wired in by later tasks under the same plan (#2233).
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/whale-net/everything/libs/go/logging"
)

// config holds `ui`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// Addr is the address this binary's HTTP surface listens on.
	Addr string
}

func loadConfig() config {
	return config{
		Addr: getEnv("WHAGENT_UI_ADDR", ":8080"),
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
		ServiceName:   "whagent-net-ui",
		Domain:        "whagent-net",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	mux := http.NewServeMux()
	setupRoutes(mux)

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(mux, "whagent-net-ui"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown: stop accepting new connections and drain
	// in-flight requests on SIGTERM/SIGINT instead of dropping them
	// (mirrors whagent_net/mcp/main.go's run()).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("listening", "addr", cfg.Addr)
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

// setupRoutes registers this scaffold's routes. /healthz is unauthenticated
// (used by the chart's health check and Tilt); everything else is a
// placeholder until Keycloak sign-in, the `api` client, and real pages
// land in later tasks under this plan (#2233).
func setupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/", handleIndexPlaceholder)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"status":"ok"}`)
}

// handleIndexPlaceholder is a scaffold-only stand-in: no auth wrapping,
// no rendered shell yet. Superseded once Keycloak sign-in and the
// authenticated index page land (see this task's Implementation section).
func handleIndexPlaceholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "whagent-net-ui: scaffold placeholder\n")
}
