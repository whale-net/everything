package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/whale-net/everything/leaflab/broadcast"
)

// ConfigAckSignal is the wire type published whenever the processor receives a
// device acknowledgement for a config push. It is an alias for
// broadcast.ConfigAckSignal — leaflab/api decodes the exact same type — so
// there is one canonical schema for the signal, not two structurally-similar
// copies maintained independently.
type ConfigAckSignal = broadcast.ConfigAckSignal

// ConfigAckPublisher publishes config acknowledgement signals to all API replicas.
// Implementations must guarantee delivery to every replica via a broadcast
// mechanism (AMQP fanout), not a competing-consumer queue.
type ConfigAckPublisher interface {
	// PublishAck sends a config ack signal to all subscribers.
	// The call is synchronous and must complete before the logical operation is
	// considered committed.
	PublishAck(ctx context.Context, signal ConfigAckSignal) error
}

// RabbitMQAckPublisher publishes config ack signals via the leaflab broadcast
// fanout exchange (leaflab/broadcast) — the same exchange
// leaflab/processor/cache.go's CacheInvalidator publishes on, distinguished
// only by routing key. This is a deliberate reuse of the Phase 3 (#1203)
// signalling path, not a second mechanism.
type RabbitMQAckPublisher struct {
	logger *slog.Logger
	pub    *broadcast.Publisher
}

// NewRabbitMQAckPublisher creates a publisher that publishes ack signals onto
// the shared leaflab broadcast exchange via pub.
func NewRabbitMQAckPublisher(logger *slog.Logger, pub *broadcast.Publisher) *RabbitMQAckPublisher {
	return &RabbitMQAckPublisher{
		logger: logger,
		pub:    pub,
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

	if err := p.pub.Publish(ctx, broadcast.RoutingKeyConfigAck, signalJSON); err != nil {
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
