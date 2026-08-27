package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/whale-net/everything/leaflab/invalidation"
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

	// FR73: broadcasts an invalidation event after this process's own
	// ApplyConfigRegions commits a region change (handler.go's
	// handleConfigAck), so every process's cache -- including this one, via
	// the Subscriber below -- observes it regardless of which process wrote
	// the assignment. See leaflab/invalidation's doc comment.
	invalidationPub, err := invalidation.NewPublisher(rmqConn)
	if err != nil {
		return fmt.Errorf("failed to create invalidation publisher: %w", err)
	}
	defer invalidationPub.Close() //nolint:errcheck

	handler := NewMessageHandler(logger, repo, cache, invalidationPub)
	consumer.RegisterHandler("leaflab.#", handler.Handle)

	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	// FR73: receives every invalidation event this process or the API
	// publishes -- including events this process publishes to itself, e.g.
	// its own ApplyConfigRegions -- and evicts the corresponding SensorCache
	// entry so the next handleReading re-reads the current value from the
	// database instead of serving what's cached. A rename evicts the
	// *prior* name (see SensorCache.Invalidate's doc comment).
	invalidationSub, err := invalidation.NewSubscriber(rmqConn, logger)
	if err != nil {
		return fmt.Errorf("failed to create invalidation subscriber: %w", err)
	}
	defer invalidationSub.Close() //nolint:errcheck
	if err := invalidationSub.Start(appCtx, func(_ context.Context, ev invalidation.Event) {
		if ev.Kind == invalidation.KindName {
			cache.Invalidate(ev.DeviceID, ev.PriorSensorName)
			return
		}
		cache.Invalidate(ev.DeviceID, ev.SensorName)
	}); err != nil {
		return fmt.Errorf("failed to start invalidation subscriber: %w", err)
	}

	// FR73 bounded staleness backstop: self-heals a dropped invalidation
	// event (e.g. one lost during a RabbitMQ reconnect window) within
	// cfg.CacheBackstopInterval, so a missed signal cannot leave the cache
	// wrong indefinitely. The 5s bound itself is met by invalidationSub
	// above, not by this loop — see backstop.go and leaflab/ARCHITECTURE.md.
	go RunCacheBackstop(appCtx, cfg.CacheBackstopInterval, repo, cache, logger)
	logger.Info("cache backstop started", "interval", cfg.CacheBackstopInterval)

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
