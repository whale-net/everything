package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/whale-net/everything/leaflab/broadcast"
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

	// Declare the leaflab broadcast fanout exchange used for cross-process
	// signalling. This is the single mechanism for cross-process broadcast in
	// leaflab: cache invalidation (FR73, #1203) and config ack delivery to every
	// API replica (FR47/NFR15, #1216) both ride this one exchange, distinguished
	// only by routing key. See leaflab/broadcast and leaflab/ARCHITECTURE.md
	// "Cross-process broadcast signalling".
	if err := broadcast.DeclareExchange(rmqConn, broadcast.Exchange); err != nil {
		return fmt.Errorf("failed to declare broadcast exchange: %w", err)
	}

	// Bind this process's queue to the broadcast exchange so it also receives
	// cache invalidation signals published by any process (including itself).
	if err := consumer.BindExchange(broadcast.Exchange, []string{broadcast.RoutingKeyCacheInvalidation}); err != nil {
		return fmt.Errorf("failed to bind cache invalidation exchange: %w", err)
	}

	// Create ONE publisher shared by both signal types published from this
	// process, so there is no ambiguity that they ride the same connection,
	// channel, and exchange.
	broadcastPublisher, err := rmq.NewPublisher(rmqConn)
	if err != nil {
		return fmt.Errorf("failed to create broadcast publisher: %w", err)
	}
	defer broadcastPublisher.Close() //nolint:errcheck

	invalidator := NewRabbitMQInvalidator(logger, broadcastPublisher, broadcast.Exchange)

	broadcastAckPub, err := broadcast.NewPublisher(rmqConn, broadcastPublisher, broadcast.Exchange)
	if err != nil {
		return fmt.Errorf("failed to create broadcast ack publisher: %w", err)
	}
	ackPublisher := NewRabbitMQAckPublisher(logging.Get("ack-publisher"), broadcastAckPub)

	// Register handler for cache invalidation signals. This runs in the same consumer loop
	// and receives all invalidation broadcasts from all publishers (including other processes).
	consumer.RegisterHandler(broadcast.RoutingKeyCacheInvalidation, func(ctx context.Context, msg rmq.Message) error {
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

	handler := NewMessageHandler(logger, repo, cache, invalidator, ackPublisher)
	consumer.RegisterHandler("leaflab.#", handler.Handle)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	if err := consumer.Start(appCtx); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}
	logger.Info("consuming messages",
		"sensor_exchange", "amq.topic",
		"sensor_routing_key", "leaflab.#",
		"broadcast_exchange", broadcast.Exchange,
		"invalidation_routing_key", broadcast.RoutingKeyCacheInvalidation,
		"config_ack_routing_key", broadcast.RoutingKeyConfigAck,
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
