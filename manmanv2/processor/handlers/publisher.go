package handlers

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/events"
)

// Publisher publishes events to the external exchange and to the dedicated
// manmanv2.htmxsse live-status exchange.
type Publisher interface {
	PublishExternal(ctx context.Context, routingKey string, message interface{}) error
	PublishLive(ctx context.Context, routingKey string, message interface{}) error
}

// RMQPublisher implements Publisher using RabbitMQ
type RMQPublisher struct {
	publisher        *rmq.Publisher
	externalExchange string
	liveExchange     string
	logger           *slog.Logger
}

// NewRMQPublisher creates a new RabbitMQ publisher that declares and
// publishes to both the external exchange and the manmanv2.htmxsse live
// exchange.
func NewRMQPublisher(conn *rmq.Connection, externalExchange, liveExchange string, logger *slog.Logger) (*RMQPublisher, error) {
	// Create channel for declaring exchanges
	ch, err := conn.Channel()
	if err != nil {
		return nil, err
	}

	// Declare external exchange
	err = ch.ExchangeDeclare(
		externalExchange, // exchange name
		"topic",          // exchange type
		true,             // durable
		false,            // auto-deleted
		false,            // internal
		false,            // no-wait
		nil,              // arguments
	)
	if err != nil {
		ch.Close()
		return nil, err
	}

	// Declare live exchange, using the shared declare args so this stays in
	// lockstep with manmanv2/ui's htmxsse.DefaultAttachFunc declare call.
	liveKind, liveDurable, liveAutoDelete, liveInternal, liveNoWait, liveArgs := events.DeclareArgs()
	err = ch.ExchangeDeclare(
		liveExchange,
		liveKind,
		liveDurable,
		liveAutoDelete,
		liveInternal,
		liveNoWait,
		liveArgs,
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

	logger.Info("declared external exchange", "exchange", externalExchange)
	logger.Info("declared live exchange", "exchange", liveExchange)

	return &RMQPublisher{
		publisher:        publisher,
		externalExchange: externalExchange,
		liveExchange:     liveExchange,
		logger:           logger,
	}, nil
}

// PublishExternal publishes a message to the external exchange
func (p *RMQPublisher) PublishExternal(ctx context.Context, routingKey string, message interface{}) error {
	body, err := json.Marshal(message)
	if err != nil {
		p.logger.Error("failed to marshal message", "error", err, "routing_key", routingKey)
		return err
	}

	err = p.publisher.Publish(ctx, p.externalExchange, routingKey, body)
	if err != nil {
		p.logger.Error("failed to publish message", "error", err, "routing_key", routingKey, "exchange", p.externalExchange)
		return err
	}

	p.logger.Debug("published external message", "routing_key", routingKey, "exchange", p.externalExchange)
	return nil
}

// PublishLive publishes a message to the dedicated manmanv2.htmxsse live
// exchange, keyed by SGC. Unlike PublishExternal, this is not filtered by
// status - see manmanv2/processor/handlers/session_status.go call sites.
func (p *RMQPublisher) PublishLive(ctx context.Context, routingKey string, message interface{}) error {
	body, err := json.Marshal(message)
	if err != nil {
		p.logger.Error("failed to marshal message", "error", err, "routing_key", routingKey)
		return err
	}

	err = p.publisher.Publish(ctx, p.liveExchange, routingKey, body)
	if err != nil {
		p.logger.Error("failed to publish message", "error", err, "routing_key", routingKey, "exchange", p.liveExchange)
		return err
	}

	p.logger.Debug("published live message", "routing_key", routingKey, "exchange", p.liveExchange)
	return nil
}
