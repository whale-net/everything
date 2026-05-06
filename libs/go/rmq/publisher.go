package rmq

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Publisher publishes messages to RabbitMQ exchanges
// It automatically recovers from channel closures by recreating the channel
type Publisher struct {
	mu         sync.Mutex
	channel    *amqp.Channel
	conn       *Connection
	exchange   string
	// chanOpener is the factory used to recreate channels after failure.
	// It defaults to openAndConfigureChannel and is overridable in tests.
	chanOpener func(conn *Connection, exchange string) (*amqp.Channel, error)
}

// NewPublisher creates a new publisher
func NewPublisher(conn *Connection) (*Publisher, error) {
	ch, err := openAndConfigureChannel(conn, "manman")
	if err != nil {
		return nil, err
	}

	return &Publisher{
		channel:    ch,
		conn:       conn,
		exchange:   "manman",
		chanOpener: openAndConfigureChannel,
	}, nil
}

// openAndConfigureChannel opens a channel and declares the exchange
func openAndConfigureChannel(conn *Connection, exchange string) (*amqp.Channel, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	// Declare topic exchange (default for ManMan)
	if err := ch.ExchangeDeclare(
		exchange,  // exchange name
		"topic",   // exchange type
		true,      // durable
		false,     // auto-deleted
		false,     // internal
		false,     // no-wait
		nil,       // arguments
	); err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to declare exchange: %w", err)
	}

	return ch, nil
}

// isChannelClosed checks if the channel is closed or if it's a channel-closed error
func isChannelClosed(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "channel/connection is not open") ||
		strings.Contains(errStr, "Exception (504)") ||
		strings.Contains(errStr, "channel closed")
}

// isPreconditionFailed returns true when RabbitMQ rejects a declare because the
// queue already exists with different arguments (AMQP 406 PRECONDITION_FAILED).
func isPreconditionFailed(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "PRECONDITION_FAILED") ||
		strings.Contains(err.Error(), "Exception (406)")
}

// isNotFound returns true when RabbitMQ reports the queue does not exist (AMQP 404).
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "NOT_FOUND") ||
		strings.Contains(err.Error(), "Exception (404)")
}

// Publish publishes a message to an exchange with a routing key
// It automatically reconnects to RabbitMQ if the channel is closed
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, body interface{}) error {
	var bodyBytes []byte
	var err error

	switch v := body.(type) {
	case []byte:
		bodyBytes = v
	case string:
		bodyBytes = []byte(v)
	default:
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Inject trace context into AMQP headers so consumers can extract it.
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	headers := amqp.Table{}
	for k, v := range carrier {
		headers[k] = v
	}

	publishing := amqp.Publishing{
		ContentType:  "application/json",
		Body:         bodyBytes,
		DeliveryMode: amqp.Persistent,
		Timestamp:    time.Now(),
		Headers:      headers,
	}

	// First attempt
	p.mu.Lock()
	ch := p.channel
	p.mu.Unlock()

	err = ch.PublishWithContext(ctx, exchange, routingKey, false, false, publishing)

	// If publish succeeded, return
	if err == nil {
		return nil
	}

	// If the channel is closed, try to recreate and retry once.
	// Guard against the double-recreation race: two goroutines can both read
	// the same closed channel pointer, both fail, and both enter this block.
	// Only the first to acquire the lock should recreate; the second should
	// reuse the channel the first goroutine already installed.
	if isChannelClosed(err) {
		p.mu.Lock()
		if p.channel != ch {
			// Another goroutine already replaced the closed channel.
			ch = p.channel
			p.mu.Unlock()
		} else {
			newCh, recreateErr := p.chanOpener(p.conn, exchange)
			if recreateErr != nil {
				p.mu.Unlock()
				return fmt.Errorf("publish failed and channel recreation failed: %w (original error: %w)", recreateErr, err)
			}
			p.channel = newCh
			ch = newCh
			p.mu.Unlock()
		}

		// Retry the publish once
		retryErr := ch.PublishWithContext(ctx, exchange, routingKey, false, false, publishing)
		if retryErr != nil {
			return fmt.Errorf("publish failed after channel recreation: %w", retryErr)
		}
		return nil
	}

	// For other errors, return as-is (don't retry)
	return err
}

// PublishWithExpiry publishes a message with a per-message TTL (expiration).
// expiry is the duration after which the broker will drop the message if undelivered.
// It automatically reconnects to RabbitMQ if the channel is closed.
func (p *Publisher) PublishWithExpiry(ctx context.Context, exchange, routingKey string, body interface{}, expiry time.Duration) error {
	var bodyBytes []byte
	var err error

	switch v := body.(type) {
	case []byte:
		bodyBytes = v
	case string:
		bodyBytes = []byte(v)
	default:
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}
	}

	expirationMS := fmt.Sprintf("%d", expiry.Milliseconds())

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	p.mu.Lock()
	ch := p.channel
	p.mu.Unlock()

	err = ch.PublishWithContext(
		ctx,
		exchange,
		routingKey,
		false,
		false,
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         bodyBytes,
			DeliveryMode: amqp.Persistent,
			Timestamp:    time.Now(),
			Expiration:   expirationMS,
		},
	)

	if err == nil {
		return nil
	}

	if isChannelClosed(err) {
		p.mu.Lock()
		if p.channel != ch {
			ch = p.channel
			p.mu.Unlock()
		} else {
			newCh, recreateErr := p.chanOpener(p.conn, exchange)
			if recreateErr != nil {
				p.mu.Unlock()
				return fmt.Errorf("publish failed and channel recreation failed: %w (original error: %w)", recreateErr, err)
			}
			p.channel = newCh
			ch = newCh
			p.mu.Unlock()
		}

		retryErr := ch.PublishWithContext(
			ctx,
			exchange,
			routingKey,
			false,
			false,
			amqp.Publishing{
				ContentType:  "application/json",
				Body:         bodyBytes,
				DeliveryMode: amqp.Persistent,
				Timestamp:    time.Now(),
				Expiration:   expirationMS,
			},
		)

		if retryErr != nil {
			return fmt.Errorf("publish failed after channel recreation: %w", retryErr)
		}
		return nil
	}

	return err
}

// PublishWithReply publishes a message with RPC support (reply_to and correlation_id)
// It automatically reconnects to RabbitMQ if the channel is closed
func (p *Publisher) PublishWithReply(ctx context.Context, exchange, routingKey string, body []byte, replyTo, correlationID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// First attempt
	p.mu.Lock()
	ch := p.channel
	p.mu.Unlock()

	err := ch.PublishWithContext(
		ctx,
		exchange,
		routingKey,
		false, // mandatory
		false, // immediate
		amqp.Publishing{
			ContentType:   "application/json",
			Body:          body,
			DeliveryMode:  amqp.Persistent,
			Timestamp:     time.Now(),
			ReplyTo:       replyTo,
			CorrelationId: correlationID,
		},
	)

	// If publish succeeded, return
	if err == nil {
		return nil
	}

	// If the channel is closed, try to recreate and retry once.
	// Same double-recreation guard as Publish.
	if isChannelClosed(err) {
		p.mu.Lock()
		if p.channel != ch {
			ch = p.channel
			p.mu.Unlock()
		} else {
			newCh, recreateErr := p.chanOpener(p.conn, exchange)
			if recreateErr != nil {
				p.mu.Unlock()
				return fmt.Errorf("publish failed and channel recreation failed: %w (original error: %w)", recreateErr, err)
			}
			p.channel = newCh
			ch = newCh
			p.mu.Unlock()
		}

		// Retry the publish once
		retryErr := ch.PublishWithContext(
			ctx,
			exchange,
			routingKey,
			false, // mandatory
			false, // immediate
			amqp.Publishing{
				ContentType:   "application/json",
				Body:          body,
				DeliveryMode:  amqp.Persistent,
				Timestamp:     time.Now(),
				ReplyTo:       replyTo,
				CorrelationId: correlationID,
			},
		)

		if retryErr != nil {
			return fmt.Errorf("publish failed after channel recreation: %w", retryErr)
		}
		return nil
	}

	// For other errors, return as-is (don't retry)
	return err
}

// Close closes the publisher channel
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.channel != nil {
		return p.channel.Close()
	}
	return nil
}
