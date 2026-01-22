package rmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	amqp "github.com/rabbitmq/amqp091-go"
)

// MessageHandler is a function that handles incoming messages
type MessageHandler func(ctx context.Context, routingKey string, body []byte) error

// Consumer consumes messages from RabbitMQ queues
type Consumer struct {
	channel  *amqp.Channel
	queue    string
	handlers map[string]MessageHandler
}

// NewConsumer creates a new consumer
func NewConsumer(conn *Connection, queueName string) (*Consumer, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	// Set QoS to ensure fair dispatching
	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to set QoS: %w", err)
	}

	// Declare queue
	queue, err := ch.QueueDeclare(
		queueName, // name
		true,      // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to declare queue: %w", err)
	}

	return &Consumer{
		channel:  ch,
		queue:    queue.Name,
		handlers: make(map[string]MessageHandler),
	}, nil
}

// BindExchange binds the consumer's queue to an exchange with routing keys
func (c *Consumer) BindExchange(exchange string, routingKeys []string) error {
	for _, key := range routingKeys {
		if err := c.channel.QueueBind(
			c.queue,    // queue name
			key,        // routing key
			exchange,   // exchange
			false,      // no-wait
			nil,        // arguments
		); err != nil {
			return fmt.Errorf("failed to bind queue to exchange: %w", err)
		}
	}
	return nil
}

// RegisterHandler registers a message handler for a specific routing key pattern
func (c *Consumer) RegisterHandler(routingKeyPattern string, handler MessageHandler) {
	c.handlers[routingKeyPattern] = handler
}

// Start starts consuming messages
func (c *Consumer) Start(ctx context.Context) error {
	msgs, err := c.channel.Consume(
		c.queue, // queue
		"",      // consumer
		false,   // auto-ack (we'll ack manually)
		false,   // exclusive
		false,   // no-local
		false,   // no-wait
		nil,     // args
	)
	if err != nil {
		return fmt.Errorf("failed to register consumer: %w", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				c.handleMessage(ctx, msg)
			}
		}
	}()

	return nil
}

// handleMessage processes a single message
func (c *Consumer) handleMessage(ctx context.Context, msg amqp.Delivery) {
	// Find matching handler
	var handler MessageHandler
	for pattern, h := range c.handlers {
		if matchesRoutingKey(msg.RoutingKey, pattern) {
			handler = h
			break
		}
	}

	if handler == nil {
		// Default handler if none registered
		log.Printf("No handler for routing key: %s", msg.RoutingKey)
		msg.Nack(false, false) // Reject and don't requeue
		return
	}

	// Call handler
	if err := handler(ctx, msg.RoutingKey, msg.Body); err != nil {
		log.Printf("Error handling message: %v", err)
		msg.Nack(false, true) // Reject and requeue
		return
	}

	// Acknowledge message
	msg.Ack(false)
}

// matchesRoutingKey checks if a routing key matches a pattern
// Supports wildcards: * (single word), # (zero or more words)
func matchesRoutingKey(key, pattern string) bool {
	if pattern == "#" {
		return true
	}
	if pattern == key {
		return true
	}
	
	// Simple prefix matching for now
	// Full wildcard support can be added later
	if len(pattern) > 0 && pattern[len(pattern)-1] == '#' {
		prefix := pattern[:len(pattern)-1]
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			return true
		}
	}
	
	return false
}

// UnmarshalMessage unmarshals a JSON message body into a struct
func UnmarshalMessage(body []byte, v interface{}) error {
	return json.Unmarshal(body, v)
}

// Close closes the consumer channel
func (c *Consumer) Close() error {
	if c.channel != nil {
		return c.channel.Close()
	}
	return nil
}
