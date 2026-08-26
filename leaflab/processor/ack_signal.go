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

// ConfigAckSignal is published whenever the processor receives a device acknowledgement
// for a config push. All API replicas must receive the signal to enable bounded waits
// across any replica (NFR15 broadcast constraint).
type ConfigAckSignal struct {
	// Timestamp when the ack was received from the device
	AckedAt time.Time `json:"acked_at"`

	// DeviceID of the board that acked
	DeviceID string `json:"device_id"`

	// ConfigVersion of the config being acked
	ConfigVersion int64 `json:"config_version"`

	// Accepted indicates whether the device accepted (true) or rejected (false) the config
	Accepted bool `json:"accepted"`

	// RejectionReason is the verbatim reason from the device, if rejected.
	// Empty string if the config was accepted.
	RejectionReason string `json:"rejection_reason,omitempty"`
}

// ConfigAckPublisher publishes config acknowledgement signals to all API replicas.
// Implementations must guarantee delivery to every replica via a broadcast mechanism
// (AMQP fanout), not a competing-consumer queue.
type ConfigAckPublisher interface {
	// PublishAck sends a config ack signal to all subscribers.
	// The call is synchronous and must complete before the logical operation is
	// considered committed.
	PublishAck(ctx context.Context, signal ConfigAckSignal) error
}

// RabbitMQAckPublisher publishes config ack signals via RabbitMQ fanout exchange.
type RabbitMQAckPublisher struct {
	logger    *slog.Logger
	publisher *rmq.Publisher
	exchange  string
}

// NewRabbitMQAckPublisher creates a publisher that publishes via RabbitMQ.
// The exchange is created as a fanout exchange to ensure broadcast delivery.
func NewRabbitMQAckPublisher(logger *slog.Logger, publisher *rmq.Publisher, exchange string) *RabbitMQAckPublisher {
	return &RabbitMQAckPublisher{
		logger:    logger,
		publisher: publisher,
		exchange:  exchange,
	}
}

// PublishAck sends a config ack signal to the broadcast exchange.
func (p *RabbitMQAckPublisher) PublishAck(ctx context.Context, signal ConfigAckSignal) error {
	if signal.DeviceID == "" || signal.ConfigVersion == 0 {
		return fmt.Errorf("invalid ack signal: device_id and config_version are required")
	}

	signalJSON, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("failed to marshal ack signal: %w", err)
	}

	// Use a fixed routing key for fanout; the exchange type ensures all subscribers receive it
	routingKey := "leaflab.config-ack"

	err = p.publisher.Publish(ctx, p.exchange, routingKey, signalJSON)
	if err != nil {
		p.logger.Error("failed to publish config ack signal",
			"device_id", signal.DeviceID,
			"config_version", signal.ConfigVersion,
			"accepted", signal.Accepted,
			"err", err,
		)
		return err
	}

	p.logger.Debug("config ack signal published",
		"device_id", signal.DeviceID,
		"config_version", signal.ConfigVersion,
		"accepted", signal.Accepted,
	)
	return nil
}

// ConfigAckListener subscribes to config ack signals and delivers them to waiting callers.
// This interface is the consumer side of the broadcast mechanism.
type ConfigAckListener interface {
	// ListenAndDeliver starts listening for ack signals and delivers them
	// until ctx is cancelled.
	ListenAndDeliver(ctx context.Context) error

	// Stop stops the listener
	Stop() error
}

// LocalConfigAckListener listens for config ack signals and notifies waiting callers
// via a callback mechanism. It subscribes to a broadcast exchange to receive signals
// from the processor.
type LocalConfigAckListener struct {
	logger   *slog.Logger
	consumer *rmq.Consumer
	exchange string

	// callbacks maps (device_id, version) -> callback function
	mu        sync.RWMutex
	callbacks map[string]map[int64]func(ConfigAckSignal)

	stopOnce sync.Once
	stopped  bool
}

// NewLocalConfigAckListener creates a listener that delivers ack signals to callers.
func NewLocalConfigAckListener(logger *slog.Logger, consumer *rmq.Consumer, exchange string) *LocalConfigAckListener {
	return &LocalConfigAckListener{
		logger:    logger,
		consumer:  consumer,
		exchange:  exchange,
		callbacks: make(map[string]map[int64]func(ConfigAckSignal)),
	}
}

// RegisterCallback registers a callback for a specific (device_id, version) pair.
// The callback will be invoked when an ack signal arrives for that pair.
func (l *LocalConfigAckListener) RegisterCallback(deviceID string, version int64, callback func(ConfigAckSignal)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.callbacks[deviceID] == nil {
		l.callbacks[deviceID] = make(map[int64]func(ConfigAckSignal))
	}
	l.callbacks[deviceID][version] = callback
}

// UnregisterCallback removes a callback for a specific (device_id, version) pair.
func (l *LocalConfigAckListener) UnregisterCallback(deviceID string, version int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if callbacks, ok := l.callbacks[deviceID]; ok {
		delete(callbacks, version)
		if len(callbacks) == 0 {
			delete(l.callbacks, deviceID)
		}
	}
}

// ListenAndDeliver subscribes to the ack exchange and processes signals until ctx is cancelled.
func (l *LocalConfigAckListener) ListenAndDeliver(ctx context.Context) error {
	// Register a handler for ack messages
	handler := func(ctx context.Context, msg rmq.Message) error {
		var signal ConfigAckSignal
		if err := json.Unmarshal(msg.Body, &signal); err != nil {
			l.logger.Warn("failed to unmarshal config ack signal",
				"routing_key", msg.RoutingKey,
				"err", err,
			)
			// Permanent error to prevent requeue
			return &rmq.PermanentError{Err: err}
		}

		l.logger.Debug("config ack signal received",
			"device_id", signal.DeviceID,
			"config_version", signal.ConfigVersion,
			"accepted", signal.Accepted,
		)

		// Deliver to registered callbacks
		l.mu.RLock()
		callback, ok := l.callbacks[signal.DeviceID][signal.ConfigVersion]
		l.mu.RUnlock()

		if ok && callback != nil {
			callback(signal)
		}

		return nil
	}

	l.consumer.RegisterHandler("leaflab.config-ack", handler)

	// The consumer is already started in main.go; this just ensures the handler is registered.
	// The consumer will deliver messages to the registered handler until ctx is cancelled.
	<-ctx.Done()
	return ctx.Err()
}

// Stop stops the listener.
func (l *LocalConfigAckListener) Stop() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	var lastErr error
	l.stopOnce.Do(func() {
		l.stopped = true
		// Consumer cleanup is handled by main.go
	})

	return lastErr
}
