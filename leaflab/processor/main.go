package main

import (
	"context"
	"encoding/json"
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
		EnableOTLP:  true,
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

	// Cache invalidation is broadcast via a separate fanout exchange.
	// This ensures every subscriber (including multiple processor replicas in future)
	// receives invalidation signals, not just one arbitrary consumer.
	// See leaflab/ARCHITECTURE.md "FR73 Cache Invalidation Signalling" for design.
	if err := consumer.BindExchange("leaflab.cache-invalidations", []string{"leaflab.cache-invalidation"}); err != nil {
		return fmt.Errorf("failed to bind cache invalidation exchange: %w", err)
	}

	repo := NewRepository(dbPool)
	cache := NewSensorCache()

	// Pre-warm the cache from the DB so readings are accepted immediately,
	// even if the device connected and sent its manifest before this process started.
	if entries, err := repo.LoadSensorCache(context.Background()); err != nil {
		logger.Warn("failed to pre-load sensor cache", "err", err)
	} else {
		cache.Load(entries)
		logger.Info("sensor cache pre-loaded", "devices", len(entries))
	}

	if versions, err := repo.LoadConfigVersionCache(context.Background()); err != nil {
		logger.Warn("failed to pre-load config version cache", "err", err)
	} else {
		cache.LoadConfigVersions(versions)
		logger.Info("config version cache pre-loaded", "devices", len(versions))
	}

	// Declare the cache invalidation fanout exchange.
	// This must be declared before the publisher and consumer use it.
	ch, err := rmqConn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open channel for exchange declaration: %w", err)
	}
	if err := ch.ExchangeDeclare(
		"leaflab.cache-invalidations", // exchange name
		"fanout",                      // exchange type (broadcast to all subscribers)
		true,                          // durable
		false,                         // auto-deleted
		false,                         // internal
		false,                         // no-wait
		nil,                           // arguments
	); err != nil {
		ch.Close()
		return fmt.Errorf("failed to declare cache invalidation fanout exchange: %w", err)
	}
	ch.Close()

	// Create the cache invalidation publisher and invalidator.
	// The publisher uses the fanout exchange declared above for broadcast delivery.
	invalidationPublisher, err := rmq.NewPublisher(rmqConn)
	if err != nil {
		return fmt.Errorf("failed to create cache invalidation publisher: %w", err)
	}
	defer invalidationPublisher.Close() //nolint:errcheck

	invalidator := NewRabbitMQInvalidator(logger, invalidationPublisher, "leaflab.cache-invalidations")

	// Register handler for cache invalidation signals. This runs in the same consumer loop
	// and receives all invalidation broadcasts from all publishers (including other processes).
	consumer.RegisterHandler("leaflab.cache-invalidation", func(ctx context.Context, msg rmq.Message) error {
		var signal CacheInvalidationSignal
		if err := json.Unmarshal(msg.Body, &signal); err != nil {
			logger.Warn("failed to unmarshal cache invalidation signal",
				"routing_key", msg.RoutingKey,
				"err", err,
			)
			return &rmq.PermanentError{Err: err}
		}
		logger.Debug("cache invalidation signal received",
			"device_id", signal.DeviceID,
			"sensor_id", signal.SensorID,
			"change_type", signal.ChangeType,
		)
		cache.ApplyInvalidation(&signal)
		return nil
	})

	handler := NewMessageHandler(logger, repo, cache, invalidator)
	consumer.RegisterHandler("leaflab.#", handler.Handle)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	if err := consumer.Start(appCtx); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}
	logger.Info("consuming messages",
		"sensor_exchange", "amq.topic",
		"sensor_routing_key", "leaflab.#",
		"invalidation_exchange", "leaflab.cache-invalidations",
		"invalidation_routing_key", "leaflab.cache-invalidation",
	)

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
