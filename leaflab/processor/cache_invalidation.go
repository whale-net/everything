package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
)

// CacheInvalidationSignal is published whenever a sensor's cached properties
// (region, identity, cache key) change. All subscribers must receive the signal
// to maintain consistency across processes.
type CacheInvalidationSignal struct {
	// Timestamp when the change was committed to the database
	CommittedAt time.Time `json:"committed_at"`

	// DeviceID of the board whose sensor changed
	DeviceID string `json:"device_id"`

	// SensorID of the sensor that changed
	SensorID int64 `json:"sensor_id"`

	// OldName is the sensor's previous name, if it was renamed
	// Empty string means no rename occurred in this signal
	OldName string `json:"old_name,omitempty"`

	// NewName is the sensor's new name, if it was renamed
	// Empty string means no rename occurred in this signal
	NewName string `json:"new_name,omitempty"`

	// RegionID is the new region assignment, if the region changed.
	// nil means the sensor has no region assignment
	RegionID *int64 `json:"region_id,omitempty"`

	// ChangeType describes which property changed: "region", "rename", "rewire", or "identity"
	// This helps subscribers understand what to invalidate
	ChangeType string `json:"change_type"`
}

// CacheInvalidator publishes cache invalidation signals to all subscribers.
// Implementations must guarantee delivery to every subscriber via a broadcast
// mechanism (AMQP fanout), not a competing-consumer queue.
type CacheInvalidator interface {
	// PublishInvalidation sends a cache invalidation signal to all subscribers.
	// The call is synchronous and must complete before the logical operation is
	// considered committed.
	PublishInvalidation(ctx context.Context, signal CacheInvalidationSignal) error
}

// RabbitMQInvalidator publishes cache invalidation signals via RabbitMQ fanout exchange.
type RabbitMQInvalidator struct {
	logger    *slog.Logger
	publisher *rmq.Publisher
	exchange  string
}

// NewRabbitMQInvalidator creates an invalidator that publishes via RabbitMQ.
// The exchange is created as a fanout exchange to ensure broadcast delivery.
func NewRabbitMQInvalidator(logger *slog.Logger, publisher *rmq.Publisher, exchange string) *RabbitMQInvalidator {
	return &RabbitMQInvalidator{
		logger:    logger,
		publisher: publisher,
		exchange:  exchange,
	}
}

// PublishInvalidation sends a cache invalidation signal to the broadcast exchange.
func (i *RabbitMQInvalidator) PublishInvalidation(ctx context.Context, signal CacheInvalidationSignal) error {
	if signal.DeviceID == "" || signal.SensorID == 0 {
		return fmt.Errorf("invalid invalidation signal: device_id and sensor_id are required")
	}

	signalJSON, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("failed to marshal invalidation signal: %w", err)
	}

	// Use a fixed routing key for fanout; the exchange type ensures all subscribers receive it
	routingKey := "leaflab.cache-invalidation"

	err = i.publisher.Publish(ctx, i.exchange, routingKey, signalJSON)
	if err != nil {
		i.logger.Error("failed to publish cache invalidation signal",
			"device_id", signal.DeviceID,
			"sensor_id", signal.SensorID,
			"change_type", signal.ChangeType,
			"err", err,
		)
		return err
	}

	i.logger.Debug("cache invalidation signal published",
		"device_id", signal.DeviceID,
		"sensor_id", signal.SensorID,
		"change_type", signal.ChangeType,
	)
	return nil
}

// CacheInvalidationListener subscribes to cache invalidation signals and applies them.
// This interface is the consumer side of the broadcast mechanism.
type CacheInvalidationListener interface {
	// ListenAndInvalidate starts listening for invalidation signals and applies them
	// to the cache until ctx is cancelled.
	ListenAndInvalidate(ctx context.Context) error

	// Stop stops the listener
	Stop() error
}

// LocalCacheInvalidationListener listens for cache invalidation signals and applies
// them to the SensorCache. It subscribes to a broadcast exchange to receive signals
// from all publishers (including other processes).
type LocalCacheInvalidationListener struct {
	logger   *slog.Logger
	cache    *SensorCache
	consumer *rmq.Consumer
	exchange string

	mu       sync.Mutex
	stopOnce sync.Once
	stopped  bool
}

// NewLocalCacheInvalidationListener creates a listener that applies invalidation signals
// to the local SensorCache.
func NewLocalCacheInvalidationListener(logger *slog.Logger, cache *SensorCache, consumer *rmq.Consumer, exchange string) *LocalCacheInvalidationListener {
	return &LocalCacheInvalidationListener{
		logger:   logger,
		cache:    cache,
		consumer: consumer,
		exchange: exchange,
	}
}

// ListenAndInvalidate subscribes to the invalidation exchange and processes signals until ctx is cancelled.
func (l *LocalCacheInvalidationListener) ListenAndInvalidate(ctx context.Context) error {
	// Register a handler for cache invalidation messages
	handler := func(ctx context.Context, msg rmq.Message) error {
		var signal CacheInvalidationSignal
		if err := json.Unmarshal(msg.Body, &signal); err != nil {
			l.logger.Warn("failed to unmarshal cache invalidation signal",
				"routing_key", msg.RoutingKey,
				"err", err,
			)
			// Permanent error to prevent requeue
			return &rmq.PermanentError{Err: err}
		}

		l.logger.Debug("cache invalidation signal received",
			"device_id", signal.DeviceID,
			"sensor_id", signal.SensorID,
			"change_type", signal.ChangeType,
		)

		l.cache.ApplyInvalidation(&signal)

		return nil
	}

	l.consumer.RegisterHandler("leaflab.cache-invalidation", handler)

	// The consumer is already started in main.go; this just ensures the handler is registered.
	// The consumer will deliver messages to the registered handler until ctx is cancelled.
	<-ctx.Done()
	return ctx.Err()
}

// Stop stops the listener.
func (l *LocalCacheInvalidationListener) Stop() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var lastErr error
	l.stopOnce.Do(func() {
		l.stopped = true
		// Consumer cleanup is handled by main.go
	})

	return lastErr
}
