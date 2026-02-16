package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/whale-net/everything/libs/go/rmq"
)

// Publisher publishes events to the external exchange
type Publisher interface {
	PublishExternal(ctx context.Context, routingKey string, message interface{}) error
}

// RMQPublisher implements Publisher using RabbitMQ
type RMQPublisher struct {
	publisher *rmq.Publisher
	exchange  string
	logger    *slog.Logger
}

// NewRMQPublisher creates a new RabbitMQ publisher
func NewRMQPublisher(conn *rmq.Connection, exchange string, logger *slog.Logger) (*RMQPublisher, error) {
	// Create channel for declaring exchange
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}

	// Declare external exchange
	err = ch.ExchangeDeclare(
		exchange, // exchange name
		"topic",  // exchange type
		true,     // durable
		false,    // auto-deleted
		false,    // internal
		false,    // no-wait
		nil,      // arguments
	)
	ch.Close()
	if err != nil {
		return nil, err
	}

	// Create publisher
	publisher, err := rmq.NewPublisher(conn)
	if err != nil {
		return nil, err
	}

	logger.Info("declared external exchange", "exchange", exchange)

	return &RMQPublisher{
		publisher: publisher,
		exchange:  exchange,
		logger:    logger,
	}, nil
}

// PublishExternal publishes a message to the external exchange
func (p *RMQPublisher) PublishExternal(ctx context.Context, routingKey string, message interface{}) error {
	body, err := json.Marshal(message)
	if err != nil {
		p.logger.Error("failed to marshal message", "error", err, "routing_key", routingKey)
		return err
	}

	err = p.publisher.Publish(ctx, p.exchange, routingKey, body)
	if err != nil {
		p.logger.Error("failed to publish message", "error", err, "routing_key", routingKey, "exchange", p.exchange)
		return err
	}

	p.logger.Debug("published external message", "routing_key", routingKey, "exchange", p.exchange)
	return nil
}
