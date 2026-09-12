// Command api is krill's HTTP surface. This scaffold (issue #2487) stands
// up the binary, its config, and /healthz only -- a live database
// connectivity check, not a static 200 -- so the chart, Tiltfile, and
// image build are exercised from this task onward. No spec endpoints
// exist yet; those land once krill's entity model does (M1's later
// tasks).
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

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
)

// config holds `api`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	// Addr is the address this binary's HTTP surface listens on.
	Addr string

	// DatabaseURL is PG_DATABASE_URL -- empty defers to
	// libs/go/db.NewPool's own PG_DATABASE_URL fallback.
	DatabaseURL string
}

func loadConfig() config {
	return config{
		Addr:        getEnv("KRILL_API_ADDR", ":8080"),
		DatabaseURL: os.Getenv("PG_DATABASE_URL"),
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
		ServiceName:   "krill-api",
		Domain:        "krill",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx := context.Background()
	defer logging.Shutdown(ctx) //nolint:errcheck

	logger := logging.Get("main")

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	setupRoutes(mux, pool)

	httpServer := &http.Server{
		Addr:         cfg.Addr,
		Handler:      otelhttp.NewHandler(mux, "krill-api"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown: stop accepting new connections and drain
	// in-flight requests on SIGTERM/SIGINT instead of dropping them
	// (mirrors whagent_net/ui/main.go's run()).
	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed { //nolint:errorlint // net/http documents this exact sentinel
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-shutdownCtx.Done():
		logger.Info("shutting down")
		shutdownTimeoutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownTimeoutCtx)
	case err := <-errCh:
		if err != nil {
			logger.Error("server failed", "error", err)
		}
		return err
	}
}
