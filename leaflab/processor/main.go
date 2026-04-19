package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/whale-net/everything/libs/go/db"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/libs/go/rmq"
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
		ServiceName: "leaflab-processor",
		Domain:      "leaflab",
		JSONFormat:  true,
		EnableOTLP:  false,
	})
	defer logging.Shutdown(context.Background()) //nolint:errcheck

	logger := logging.Get("main")
	logger.Info("starting leaflab-processor", "queue", cfg.QueueName)

	dbPool, err := db.NewPool(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer dbPool.Close()
	logger.Info("database connection established")

	rmqConn, err := rmq.NewConnectionFromURL(cfg.RabbitMQURL)
	if err != nil {
		return fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}
	defer rmqConn.Close()
	logger.Info("rabbitmq connection established")

	consumer, err := rmq.NewConsumer(rmqConn, cfg.QueueName)
	if err != nil {
		return fmt.Errorf("failed to create consumer: %w", err)
	}
	defer consumer.Close() //nolint:errcheck

	// RabbitMQ MQTT plugin routes MQTT topics to amq.topic exchange,
	// replacing '/' with '.' in routing keys.
	// leaflab/<device>/sensor/<name> → leaflab.<device>.sensor.<name>
	// leaflab/<device>/manifest      → leaflab.<device>.manifest
	if err := consumer.BindExchange("amq.topic", []string{"leaflab.#"}); err != nil {
		return fmt.Errorf("failed to bind exchange: %w", err)
	}

	repo := NewRepository(dbPool)
	cache := NewSensorCache()
	handler := NewMessageHandler(logger, repo, cache)
	consumer.RegisterHandler("leaflab.#", handler.Handle)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	if err := consumer.Start(appCtx); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}
	logger.Info("consuming messages", "exchange", "amq.topic", "routing_key", "leaflab.#")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		logger.Info("received shutdown signal", "signal", sig)
	case <-appCtx.Done():
	}

	logger.Info("shutdown complete")
	return nil
}
