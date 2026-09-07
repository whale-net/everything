package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	mqtt "github.com/eclipse/paho.mqtt.golang"

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

	// No boards are simulated yet -- this scaffold only proves the paho MQTT
	// client wires up (options construction, no Connect()) and the process
	// starts, logs its resolved config, and shuts down cleanly.
	opts := mqtt.NewClientOptions().
		AddBroker(cfg.MQTTBrokerURL).
		SetUsername(cfg.MQTTUsername).
		SetPassword(cfg.MQTTPassword).
		SetClientID("leaflab-emulator")
	_ = mqtt.NewClient(opts)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutdown complete")
	return nil
}
