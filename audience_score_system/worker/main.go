// Command worker is Audience Score System's Temporal worker: the
// per-Channel scheduled sync machinery (issue #1574, FR14/FR21, NFR4) that
// runs sync.ChannelSyncWorkflow for every connected Channel on a ~15-30
// minute cadence. See ../ARCHITECTURE.md's Component map and
// audience_score_system/worker/sync's package doc comment for the
// workflow/activity/schedule machinery this binary registers.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"

	"github.com/whale-net/everything/audience_score_system/store"
	"github.com/whale-net/everything/audience_score_system/worker/sync"
	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
	temporallib "github.com/whale-net/everything/libs/go/temporal"
)

// defaultSyncInterval is ASS_SYNC_INTERVAL's default -- 20 minutes, inside
// sync.MinSyncInterval/MaxSyncInterval's 15-30 minute NFR4 band.
const defaultSyncInterval = 20 * time.Minute

// config holds `worker`'s configuration, loaded entirely from environment
// variables -- no config files (see ../ENV.md).
type config struct {
	DatabaseURL  string
	LogLevel     string
	SyncInterval time.Duration
}

// loadConfig loads configuration from environment variables, failing fast
// if ASS_SYNC_INTERVAL is set but unparseable or outside NFR4's 15-30
// minute band (sync.ValidateSyncInterval) -- see ../ENV.md.
func loadConfig() (config, error) {
	interval := defaultSyncInterval
	if raw := os.Getenv("ASS_SYNC_INTERVAL"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return config{}, fmt.Errorf("parse ASS_SYNC_INTERVAL %q: %w", raw, err)
		}
		interval = parsed
	}
	if err := sync.ValidateSyncInterval(interval); err != nil {
		return config{}, fmt.Errorf("ASS_SYNC_INTERVAL: %w", err)
	}

	return config{
		DatabaseURL:  os.Getenv("PG_DATABASE_URL"),
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		SyncInterval: interval,
	}, nil
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
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

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
		ServiceName:   "audience-score-system-worker",
		Domain:        "audience-score-system",
		Level:         logLevel,
		JSONFormat:    true,
		EnableOTLP:    true,
		EnableTracing: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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

	temporalCfg := temporallib.ConfigFromEnv()
	if temporalCfg.TaskQueue == "" {
		temporalCfg.TaskQueue = sync.TaskQueue
	}
	logger.Info("connecting to temporal", "host_port", temporalCfg.HostPort, "namespace", temporalCfg.Namespace, "task_queue", temporalCfg.TaskQueue)
	temporalClient, err := temporallib.NewClient(temporalCfg, temporallib.NewLogger("audience-score-system-worker"))
	if err != nil {
		return fmt.Errorf("connect to temporal: %w", err)
	}
	defer temporalClient.Close()

	w := temporallib.NewWorker(temporalClient, temporalCfg.TaskQueue, worker.Options{})
	w.RegisterWorkflow(sync.ChannelSyncWorkflow)

	syncActivities := &sync.Activities{Channels: st.Channels()}
	w.RegisterActivityWithOptions(syncActivities.LoadChannelState, activityOptions(sync.ActivityLoadChannelState))
	w.RegisterActivityWithOptions(syncActivities.SyncSchedule, activityOptions(sync.ActivitySyncSchedule))
	w.RegisterActivityWithOptions(syncActivities.SyncOutcomes, activityOptions(sync.ActivitySyncOutcomes))

	scheduleManager := sync.NewScheduleManager(temporalClient.ScheduleClient(), st.Channels(), cfg.SyncInterval)

	// Reconcile ensures a schedule exists for every already-connected
	// Channel, per ../ARCHITECTURE.md/issue #1574's Implementation scope:
	// "a Channel connected while the worker was down still gets a
	// schedule". Non-fatal at startup -- a reconcile hiccup (e.g. Temporal
	// transiently unreachable) should not prevent the worker from starting
	// and processing already-scheduled Channels.
	if err := scheduleManager.Reconcile(ctx); err != nil {
		logger.Warn("schedule reconcile failed at startup", "error", err)
	}

	logger.Info("starting temporal worker", "task_queue", temporalCfg.TaskQueue)
	if err := w.Run(worker.InterruptCh()); err != nil {
		return fmt.Errorf("temporal worker: %w", err)
	}
	return nil
}

// activityOptions names an activity registration so sync.ChannelSyncWorkflow's
// workflow.ExecuteActivity dispatch-by-name (see sync/workflow.go's Activity
// name constants) resolves to whatever sync.Activities implementation this
// binary registers -- mirrors tools/app_registry/worker/main.go's identical
// helper.
func activityOptions(name string) activity.RegisterOptions {
	return activity.RegisterOptions{Name: name}
}
