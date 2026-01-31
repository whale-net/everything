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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manman/api/repository"
	"github.com/whale-net/everything/manman/api/repository/postgres"
	"github.com/whale-net/everything/manman/processor/consumer"
	"github.com/whale-net/everything/manman/processor/handlers"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Load configuration
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Setup logger
	logLevel := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	logger.Info("starting manmanv2-processor",
		"queue", cfg.QueueName,
		"stale_threshold", cfg.StaleHostThreshold,
		"external_exchange", cfg.ExternalExchange,
	)

	// Initialize database connection
	dbURL := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		cfg.DBUser,
		cfg.DBPassword,
		cfg.DBHost,
		cfg.DBPort,
		cfg.DBName,
		cfg.DBSSLMode,
	)

	poolConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return fmt.Errorf("failed to parse database config: %w", err)
	}

	// Configure connection pool
	poolConfig.MaxConns = 5
	poolConfig.MinConns = 2
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.ConnConfig.ConnectTimeout = 30 * time.Second

	dbPool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		return fmt.Errorf("failed to create database pool: %w", err)
	}
	defer dbPool.Close()

	// Verify database connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := dbPool.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}
	logger.Info("database connection established")

	// Initialize RabbitMQ connection
	rmqConn, err := rmq.NewConnectionFromURL(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer rmqConn.Close()
	logger.Info("rabbitmq connection established")

	// Initialize repository
	repo := &repository.Repository{
		Servers:            postgres.NewServerRepository(dbPool),
		Sessions:           postgres.NewSessionRepository(dbPool),
		Games:              postgres.NewGameRepository(dbPool),
		GameConfigs:        postgres.NewGameConfigRepository(dbPool),
		ServerGameConfigs:  postgres.NewServerGameConfigRepository(dbPool),
		ServerCapabilities: postgres.NewServerCapabilityRepository(dbPool),
		LogReferences:      postgres.NewLogReferenceRepository(dbPool),
		Backups:            postgres.NewBackupRepository(dbPool),
	}

	// Initialize publisher for external exchange
	publisher, err := handlers.NewRMQPublisher(rmqConn, cfg.ExternalExchange, logger)
	if err != nil {
		return fmt.Errorf("failed to create publisher: %w", err)
	}

	// Create handler registry
	handlerRegistry := handlers.NewHandlerRegistry(repo, logger)

	// Register handlers
	hostStatusHandler := handlers.NewHostStatusHandler(repo, publisher, logger)
	handlerRegistry.Register("status.host.#", hostStatusHandler)

	sessionStatusHandler := handlers.NewSessionStatusHandler(repo, publisher, logger)
	handlerRegistry.Register("status.session.#", sessionStatusHandler)

	healthHandler := handlers.NewHealthHandler(repo, publisher, cfg.StaleHostThreshold, logger)
	handlerRegistry.Register("health.#", healthHandler)

	// Create consumer
	processorConsumer, err := consumer.NewProcessorConsumer(
		rmqConn,
		cfg.QueueName,
		handlerRegistry,
		logger,
	)
	if err != nil {
		return fmt.Errorf("failed to create consumer: %w", err)
	}

	// Create context for graceful shutdown
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// Start health check server
	healthServer := &http.Server{
		Addr:    fmt.Sprintf(":%s", cfg.HealthCheckPort),
		Handler: setupHealthCheckRoutes(dbPool, logger),
	}

	go func() {
		logger.Info("starting health check server", "port", cfg.HealthCheckPort)
		if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("health check server error", "error", err)
		}
	}()

	// Start stale host checker
	healthHandler.StartStaleHostChecker(appCtx)

	// Start consumer in background
	consumerErrChan := make(chan error, 1)
	go func() {
		if err := processorConsumer.Start(appCtx); err != nil {
			consumerErrChan <- err
		}
	}()

	// Wait for signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		logger.Info("received shutdown signal", "signal", sig)
	case err := <-consumerErrChan:
		logger.Error("consumer error", "error", err)
		return err
	}

	// Graceful shutdown
	logger.Info("shutting down gracefully")

	// Stop accepting new messages
	appCancel()

	// Stop health handler
	healthHandler.Stop()

	// Give in-flight messages time to complete
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Stop health check server
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("error shutting down health check server", "error", err)
	}

	// Stop consumer
	processorConsumer.Stop()

	logger.Info("shutdown complete")
	return nil
}

func setupHealthCheckRoutes(dbPool *pgxpool.Pool, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Liveness probe - returns 200 if process is running
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Readiness probe - returns 200 if DB is accessible
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := dbPool.Ping(ctx); err != nil {
			logger.Error("readiness check failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(fmt.Sprintf("database unhealthy: %v", err)))
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
	})

	return mux
}
