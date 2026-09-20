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
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
	s3lib "github.com/whale-net/everything/libs/go/s3"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
	"github.com/whale-net/everything/manmanv2/api/repository"
	"github.com/whale-net/everything/manmanv2/api/repository/postgres"
	"github.com/whale-net/everything/manmanv2/processor/backupsched"
	"github.com/whale-net/everything/manmanv2/processor/consumer"
	"github.com/whale-net/everything/manmanv2/processor/handlers"
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

	// Setup structured logging
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
		ServiceName:   "event-processor",
		Domain:        "manmanv2",
		Level:         logLevel,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	defer logging.Shutdown(context.Background())

	logger := logging.Get("main")

	logger.Info("starting event-processor",
		"queue", cfg.QueueName,
		"stale_host_threshold", cfg.StaleHostThreshold,
		"stale_session_threshold", cfg.StaleSessionThreshold,
		"external_exchange", cfg.ExternalExchange,
		"live_exchange", cfg.LiveExchange,
		"health_check_port", cfg.HealthCheckPort,
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
		BackupConfigs:      postgres.NewBackupConfigRepository(dbPool),
		GameConfigVolumes:  postgres.NewGameConfigVolumeRepository(dbPool),
		ServerPorts:        postgres.NewServerPortRepository(dbPool),
	}

	// Initialize publisher for external exchange and the manmanv2.htmxsse live exchange
	publisher, err := handlers.NewRMQPublisher(rmqConn, cfg.ExternalExchange, cfg.LiveExchange, logger)
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

	backupStatusHandler := handlers.NewBackupStatusHandler(repo, logger)
	handlerRegistry.Register("status.backup.#", backupStatusHandler)

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

	// Start stale session checker
	sessionStatusHandler.StartStaleSessionChecker(appCtx, 10*time.Second, time.Duration(cfg.StaleSessionThreshold)*time.Second)

	// Start backup scheduler (River)
	s3Client, err := s3lib.NewClient(appCtx, s3lib.Config{
		Bucket:         os.Getenv("S3_BUCKET"),
		Region:         os.Getenv("S3_REGION"),
		Endpoint:       os.Getenv("S3_ENDPOINT"),
		PublicEndpoint: os.Getenv("S3_PUBLIC_ENDPOINT"),
		AccessKey:      os.Getenv("S3_ACCESS_KEY"),
		SecretKey:      os.Getenv("S3_SECRET_KEY"),
	})
	if err != nil {
		logger.Warn("failed to initialize S3 client, scheduled backups will not run", "error", err)
		s3Client = nil
	}
	riverClient, err := startBackupScheduler(appCtx, dbPool, repo, rmqConn, s3Client, logger)
	if err != nil {
		logger.Warn("failed to start backup scheduler, scheduled backups will not run", "error", err)
	} else {
		defer riverClient.Stop(context.Background()) //nolint:errcheck
	}

	// Start the Temporal-based backup scheduler (FR16, FR17 M7) alongside
	// the still-running River path above -- #2819 removes River once this
	// path has proven itself on trunk (NFR2). Unlike the S3/River startup
	// above, a failure here fails the process: a worker that silently never
	// came up would leave every enabled BackupConfig's cadence unscheduled
	// with no observable signal beyond a missing Temporal Schedule.
	temporalWorker, temporalClient, err := startBackupTemporalWorker(appCtx, cfg, dbPool, repo, rmqConn, s3Client, logger)
	if err != nil {
		return fmt.Errorf("failed to start temporal backup scheduler: %w", err)
	}
	defer temporalWorker.Stop()
	defer temporalClient.Close()

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

// startBackupTemporalWorker connects to Temporal, registers backupsched's
// BackupScanWorkflow/DispatchBackupWorkflow and Activities on
// cfg.TemporalTaskQueue, starts the worker, and upserts the
// backupsched.ScanScheduleID Schedule (manmanv2/ARCHITECTURE.md's "Backup
// Scheduler (Temporal)" section). Every step here is fatal on error -- see
// the call site's comment for why this path, unlike the River/S3 startup
// above it, must fail the process loudly rather than degrade silently.
func startBackupTemporalWorker(ctx context.Context, cfg *Config, dbPool *pgxpool.Pool, repo *repository.Repository, rmqConn *rmq.Connection, s3Client *s3lib.Client, logger *slog.Logger) (worker.Worker, temporalclient.Client, error) {
	temporalCfg := temporallib.Config{
		HostPort:  cfg.TemporalHost,
		Namespace: cfg.TemporalNamespace,
		TaskQueue: cfg.TemporalTaskQueue,
	}

	temporalClient, err := temporallib.NewClient(temporalCfg, temporallib.NewLogger("event-processor"))
	if err != nil {
		return nil, nil, fmt.Errorf("connect to temporal at %s (namespace %s): %w", temporalCfg.HostPort, temporalCfg.Namespace, err)
	}

	backupPublisher, err := rmq.NewPublisher(rmqConn)
	if err != nil {
		temporalClient.Close()
		return nil, nil, fmt.Errorf("create backup dispatch publisher: %w", err)
	}

	temporalWorker := temporallib.NewWorker(temporalClient, temporalCfg.TaskQueue, worker.Options{})
	temporalWorker.RegisterWorkflow(backupsched.BackupScanWorkflow)
	temporalWorker.RegisterWorkflow(backupsched.DispatchBackupWorkflow)

	backupActivities := &backupsched.Activities{
		Repo:       repo,
		ActionRepo: postgres.NewActionRepository(dbPool),
		Publisher:  backupPublisher,
		S3Client:   s3Client,
	}
	temporalWorker.RegisterActivityWithOptions(backupActivities.ListDueBackupConfigs, activity.RegisterOptions{Name: backupsched.ActivityListDueBackupConfigs})
	temporalWorker.RegisterActivityWithOptions(backupActivities.DispatchBackup, activity.RegisterOptions{Name: backupsched.ActivityDispatchBackup})

	if err := temporalWorker.Start(); err != nil {
		temporalClient.Close()
		return nil, nil, fmt.Errorf("start temporal worker on task queue %s: %w", temporalCfg.TaskQueue, err)
	}

	// UpsertSchedule (libs/go/temporal), not a one-time Create: a changed
	// ScanInterval must take effect on the next worker restart rather than
	// staying pinned to whatever value first created the schedule (see that
	// function's doc comment for the incident this closes).
	err = temporallib.UpsertSchedule(ctx, temporalClient.ScheduleClient(), temporalclient.ScheduleOptions{
		ID: backupsched.ScanScheduleID,
		Spec: temporalclient.ScheduleSpec{
			Intervals: []temporalclient.ScheduleIntervalSpec{{Every: backupsched.ScanInterval}},
		},
		Action: &temporalclient.ScheduleWorkflowAction{
			Workflow:  backupsched.BackupScanWorkflow,
			TaskQueue: temporalCfg.TaskQueue,
		},
		Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		temporalWorker.Stop()
		temporalClient.Close()
		return nil, nil, fmt.Errorf("upsert schedule %s: %w", backupsched.ScanScheduleID, err)
	}

	logger.Info("temporal backup scheduler started",
		"host_port", temporalCfg.HostPort,
		"namespace", temporalCfg.Namespace,
		"task_queue", temporalCfg.TaskQueue,
		"schedule_id", backupsched.ScanScheduleID,
	)
	return temporalWorker, temporalClient, nil
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
