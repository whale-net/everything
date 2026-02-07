package consumer

import (
	"context"
	"log/slog"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manman/processor/handlers"
)

const (
	exchangeName = "manman"
)

// ProcessorConsumer wraps rmq.Consumer and integrates with handler registry
type ProcessorConsumer struct {
	consumer *rmq.Consumer
	registry *handlers.HandlerRegistry
	logger   *slog.Logger
}

// NewProcessorConsumer creates a new processor consumer
func NewProcessorConsumer(
	conn *rmq.Connection,
	queueName string,
	registry *handlers.HandlerRegistry,
	logger *slog.Logger,
) (*ProcessorConsumer, error) {
	consumer, err := rmq.NewConsumer(conn, queueName)
	if err != nil {
		return nil, err
	}

	// Bind to internal exchange with routing key patterns
	bindings := []string{
		"status.host.#",
		"status.session.#",
		"health.#",
	}

	if err := consumer.BindExchange(exchangeName, bindings); err != nil {
		return nil, err
	}

	for _, routingKey := range bindings {
		logger.Info("bound queue to exchange", "exchange", exchangeName, "routing_key", routingKey)
	}

	pc := &ProcessorConsumer{
		consumer: consumer,
		registry: registry,
		logger:   logger,
	}

	// Register message handler for all routing keys
	consumer.RegisterHandler("#", pc.handleMessage)

	return pc, nil
}

// handleMessage processes incoming messages and routes to appropriate handlers
func (c *ProcessorConsumer) handleMessage(ctx context.Context, msg rmq.Message) error {
	c.logger.Info("received message", "routing_key", msg.RoutingKey)

	err := c.registry.Route(ctx, msg.RoutingKey, msg.Body)
	if err != nil {
		// Check if this is a permanent error (don't retry)
		if handlers.IsPermanentError(err) {
			c.logger.Warn("permanent error processing message",
				"error", err,
				"routing_key", msg.RoutingKey,
			)
			// Return nil to ACK the message (don't requeue)
			return nil
		}

		// Transient error - return error to trigger NACK with requeue
		c.logger.Error("transient error processing message",
			"error", err,
			"routing_key", msg.RoutingKey,
		)
		return err
	}

	c.logger.Info("message processed successfully", "routing_key", msg.RoutingKey)
	return nil
}

// Start begins consuming messages
func (c *ProcessorConsumer) Start(ctx context.Context) error {
	c.logger.Info("starting consumer")
	return c.consumer.Start(ctx)
}

// Stop gracefully stops the consumer
func (c *ProcessorConsumer) Stop() {
	c.logger.Info("stopping consumer")
	c.consumer.Close()
}
