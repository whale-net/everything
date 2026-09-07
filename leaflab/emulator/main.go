package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/whale-net/everything/libs/go/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	logging.Configure(logging.Config{
		ServiceName: "leaflab-emulator",
		Domain:      "leaflab",
		JSONFormat:  true,
	})
	defer logging.Shutdown(context.Background()) //nolint:errcheck

	logger := logging.Get("leaflab-emulator")
	logger.Info("starting leaflab-emulator",
		"mqtt_broker_url", cfg.MQTTBrokerURL,
		"mqtt_username", cfg.MQTTUsername,
		"scenario_dir", cfg.ScenarioDir,
		"emulator_boards", cfg.EmulatorBoards,
		"publish_interval", cfg.PublishInterval,
		"random_seed", cfg.RandomSeed,
	)

	scenarios, err := LoadScenarioDir(cfg.ScenarioDir)
	if err != nil {
		return fmt.Errorf("failed to load scenarios: %w", err)
	}

	boards, err := BuildBoards(cfg, scenarios)
	if err != nil {
		return fmt.Errorf("failed to build boards: %w", err)
	}

	logEffectiveRate(logger, cfg, boards)

	runners := make([]*Runner, 0, len(boards))
	for _, b := range boards {
		deps := RunnerDeps{
			DefaultInterval: cfg.PublishInterval,
			Rand:            rand.New(rand.NewSource(seedFor(cfg.RandomSeed, b.DeviceID))),
			Logger:          logger,
		}
		r := NewPahoRunner(b, cfg, deps)
		if err := r.Start(); err != nil {
			// Logged (ERROR) by Runner.Start itself; skip this board rather
			// than aborting every other simulated board over one failure.
			continue
		}
		runners = append(runners, r)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()

	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Add(1)
		go func(r *Runner) {
			defer wg.Done()
			r.Stop()
		}(r)
	}
	wg.Wait()

	logger.Info("shutdown complete")
	return nil
}

// logEffectiveRate logs the aggregate publish rate across every board at
// INFO on startup (NFR6), so a developer can see what they are about to
// generate before it starts flowing.
func logEffectiveRate(logger interface {
	Info(msg string, args ...any)
}, cfg *Config, boards []Board) {
	sensorCount := 0
	var msgsPerSec float64
	for _, b := range boards {
		for _, s := range b.Sensors {
			if !s.Enabled {
				continue
			}
			sensorCount++
			interval := intervalFor(cfg.PublishInterval, s)
			if interval > 0 {
				msgsPerSec += 1.0 / interval.Seconds()
			}
		}
	}
	logger.Info("effective publish rate",
		"board_count", len(boards),
		"sensor_count", sensorCount,
		"default_publish_interval", cfg.PublishInterval,
		"approx_messages_per_second", msgsPerSec,
	)
}
