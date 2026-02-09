package rmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Message contains all information about an incoming message
type Message struct {
	RoutingKey    string
	Body          []byte
	ReplyTo       string
	CorrelationID string
}

// MessageHandler is a function that handles incoming messages
type MessageHandler func(ctx context.Context, msg Message) error

// Consumer consumes messages from RabbitMQ queues
type Consumer struct {
	channel  *amqp.Channel
	queue    string
	handlers map[string]MessageHandler
	conn     *Connection
}

// NewConsumer creates a new consumer
func NewConsumer(conn *Connection, queueName string) (*Consumer, error) {
	return NewConsumerWithOpts(conn, queueName, true, false)
}

// NewConsumerWithOpts creates a new consumer with custom queue options
func NewConsumerWithOpts(conn *Connection, queueName string, durable, autoDelete bool) (*Consumer, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	// Set QoS to ensure fair dispatching
	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to set QoS: %w", err)
	}

	var arguments amqp.Table
	if durable {
		// Declare dead letter queue first
		dlqName := queueName + "-dlq"
		_, err = ch.QueueDeclare(
			dlqName, // name
			true,    // durable
			false,   // delete when unused
			false,   // exclusive
			false,   // no-wait
			nil,     // arguments
		)
		if err != nil {
			ch.Close()
			return nil, fmt.Errorf("failed to declare DLQ: %w", err)
		}

		arguments = amqp.Table{
			"x-dead-letter-exchange":    "", // Use default exchange
			"x-dead-letter-routing-key": dlqName,
		}
	}

	// Declare main queue
	queue, err := ch.QueueDeclare(
		queueName,  // name
		durable,    // durable
		autoDelete, // delete when unused
		false,      // exclusive
		false,      // no-wait
		arguments,  // arguments
	)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to declare queue: %w", err)
	}

	return &Consumer{
		channel:  ch,
		queue:    queue.Name,
		handlers: make(map[string]MessageHandler),
		conn:     conn,
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
func (c *Consumer) handleMessage(ctx context.Context, delivery amqp.Delivery) {
	// Find matching handler
	var handler MessageHandler
	for pattern, h := range c.handlers {
		if matchesRoutingKey(delivery.RoutingKey, pattern) {
			handler = h
			break
		}
	}

	if handler == nil {
		// Default handler if none registered
		log.Printf("No handler for routing key: %s", delivery.RoutingKey)
		delivery.Nack(false, false) // Reject and don't requeue
		return
	}

	// Create message struct
	msg := Message{
		RoutingKey:    delivery.RoutingKey,
		Body:          delivery.Body,
		ReplyTo:       delivery.ReplyTo,
		CorrelationID: delivery.CorrelationId,
	}

	// Call handler
	err := handler(ctx, msg)

	// Send reply if reply_to is set
	if msg.ReplyTo != "" && msg.CorrelationID != "" {
		c.sendReply(ctx, msg.ReplyTo, msg.CorrelationID, err)
	}

	if err != nil {
		log.Printf("Error handling message: %v", err)

		// Check retry count from x-death header
		retryCount := getRetryCount(delivery)
		maxRetries := 3

		// Check if it's a permanent error or max retries exceeded
		if IsPermanentError(err) {
			log.Printf("Permanent error - sending to DLQ: %v", err)
			delivery.Nack(false, false) // Reject and send to DLQ
		} else if retryCount >= maxRetries {
			log.Printf("Max retries (%d) exceeded - sending to DLQ", maxRetries)
			delivery.Nack(false, false) // Reject and send to DLQ
		} else {
			log.Printf("Transient error (retry %d/%d) - requeuing", retryCount+1, maxRetries)
			delivery.Nack(false, true) // Reject and requeue for retry
		}
		return
	}

	// Acknowledge message
	delivery.Ack(false)
}

// getRetryCount extracts the retry count from the x-death header
func getRetryCount(delivery amqp.Delivery) int {
	if delivery.Headers == nil {
		return 0
	}

	xDeath, ok := delivery.Headers["x-death"].([]interface{})
	if !ok || len(xDeath) == 0 {
		return 0
	}

	// First entry in x-death contains the count
	if deathMap, ok := xDeath[0].(amqp.Table); ok {
		if count, ok := deathMap["count"].(int64); ok {
			return int(count)
		}
	}

	return 0
}

// sendReply sends a reply message back to the caller
func (c *Consumer) sendReply(ctx context.Context, replyTo, correlationID string, err error) {
	response := map[string]interface{}{
		"correlation_id": correlationID,
		"success":        err == nil,
	}

	if err != nil {
		response["error"] = err.Error()
	}

	responseBytes, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		log.Printf("Failed to marshal reply: %v", marshalErr)
		return
	}

	// Publish reply using the channel directly (no exchange, direct to queue)
	log.Printf("Sending reply to %s (correlation_id=%s, success=%v)", replyTo, correlationID, err == nil)
	publishErr := c.channel.PublishWithContext(
		ctx,
		"",      // exchange (empty for direct queue publish)
		replyTo, // routing key (queue name)
		false,   // mandatory
		false,   // immediate
		amqp.Publishing{
			ContentType:   "application/json",
			Body:          responseBytes,
			CorrelationId: correlationID,
		},
	)

	if publishErr != nil {
		log.Printf("Failed to send reply to %s: %v", replyTo, publishErr)
	} else {
		log.Printf("Successfully sent reply to %s", replyTo)
	}
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
