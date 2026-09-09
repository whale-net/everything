// Command archiver is whagent-net's hot-to-cold transcript archiver
// (FR7, C18, issue #2244): the process that batches a terminal session's
// transcript out of Postgres past WHAGENT_TRANSCRIPT_TTL, gzips it,
// uploads it to S3, writes the `transcript_archive` index row, and only
// then trims the hot-tier rows -- see whagent_net/ARCHITECTURE.md
// "Transcript storage tiers" for the tier contract this binary shares
// with #2240's tier-transparent reads, and archiver.go's doc comment for
// the write-order/crash-safety contract.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/s3"
)

func main() {
	if err := run(); err != nil {
		logging.Get("main").Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logging.Configure(logging.Config{
		ServiceName:   "whagent-net-archiver",
		Domain:        "whagent-net",
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(ctx) //nolint:errcheck
	logger := logging.Get("main")

	cfg, err := ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.S3Bucket == "" {
		return fmt.Errorf("WHAGENT_S3_BUCKET is required: the archiver has nothing to write to without it")
	}

	// Postgres, shared with api/worker (ARCHITECTURE.md "Service boundary
	// vs. package boundary") -- the archiver imports whagent_net/session
	// directly, same as worker/api, no RPC hop.
	databaseURL := getEnv("PG_DATABASE_URL", "")
	pool, err := db.NewPool(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()
	logger.Info("database connected")

	// Unlike api/worker's optional S3 client (session.WithS3's doc
	// comment: a nil client just means hot-only reads), the archiver has
	// nothing to do without one -- fail startup loudly on a construction
	// error rather than looping forever with nothing to write to.
	s3Client, err := s3.NewClient(ctx, s3.Config{
		Bucket:    cfg.S3Bucket,
		Region:    getEnv("S3_REGION", "us-east-1"),
		Endpoint:  getEnv("S3_ENDPOINT", ""),
		AccessKey: getEnv("S3_ACCESS_KEY", ""),
		SecretKey: getEnv("S3_SECRET_KEY", ""),
	})
	if err != nil {
		return fmt.Errorf("s3 client: %w", err)
	}
	logger.Info("s3 client initialized", "bucket", cfg.S3Bucket)

	archiver := NewArchiver(pool, s3Client, cfg, logger)

	done := make(chan error, 1)
	go func() {
		done <- archiver.Run(ctx)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
		logger.Info("shutting down gracefully")
		cancel()
	case err := <-done:
		cancel()
		if err != nil && err != context.Canceled { //nolint:errorlint // Run returns ctx.Err() directly, never wrapped
			return err
		}
		return nil
	}
	<-done
	return nil
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
