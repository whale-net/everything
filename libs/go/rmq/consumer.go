package rmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
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
// It automatically recovers from channel closures for reply publishing
type Consumer struct {
	mu       sync.Mutex
	channel  *amqp.Channel
	queue    string
	handlers map[string]MessageHandler
	conn     *Connection

	// init params — stored so startConsuming can fully reinitialize after a
	// connection reset (non-durable queues are deleted on connection close).
	declaredName string // original name passed at creation, used for DLQ/arg derivation
	durable      bool
	autoDelete   bool
	messageTTL   int
	maxMessages  int
	bindings     []binding // exchange + routing keys bound at creation time
}

type binding struct {
	exchange    string
	routingKeys []string
}

// NewConsumer creates a new consumer
func NewConsumer(conn *Connection, queueName string) (*Consumer, error) {
	return NewConsumerWithOpts(conn, queueName, true, false, 0, 0)
}

// NewConsumerWithOpts creates a new consumer with custom queue options
// messageTTL is in milliseconds (0 = no limit)
// maxMessages is the maximum number of messages in the queue (0 = no limit)
func NewConsumerWithOpts(conn *Connection, queueName string, durable, autoDelete bool, messageTTL, maxMessages int) (*Consumer, error) {
	if durable && queueName == "" {
		return nil, fmt.Errorf("durable queues require an explicit queue name")
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	// Set QoS to ensure fair dispatching
	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to set QoS: %w", err)
	}

	// reopenCh closes ch and returns a fresh channel with QoS set.
	// Needed after PRECONDITION_FAILED (406) which closes the channel server-side.
	reopenCh := func() error {
		ch.Close()
		ch, err = conn.Channel()
		if err != nil {
			return fmt.Errorf("failed to reopen channel: %w", err)
		}
		if err := ch.Qos(1, 0, false); err != nil {
			ch.Close()
			return fmt.Errorf("failed to set QoS: %w", err)
		}
		return nil
	}

	if durable && messageTTL == 0 && maxMessages == 0 {
		dlqName := queueName + "-dlq"
		if _, err = ch.QueueDeclare(dlqName, true, false, false, false, nil); err != nil {
			if !isPreconditionFailed(err) {
				ch.Close()
				return nil, fmt.Errorf("failed to declare DLQ: %w", err)
			}
			log.Printf("DLQ %s already exists with different args, using as-is: %v", dlqName, err)
			if err := reopenCh(); err != nil {
				return nil, err
			}
		}
	}

	arguments := buildQueueArguments(queueName, durable, autoDelete, messageTTL, maxMessages)

	queue, err := ch.QueueDeclare(queueName, durable, autoDelete, false, false, arguments)
	if err != nil {
		if !isPreconditionFailed(err) {
			ch.Close()
			return nil, fmt.Errorf("failed to declare queue: %w", err)
		}
		log.Printf("queue %s already exists with different args, using as-is: %v", queueName, err)
		if err := reopenCh(); err != nil {
			return nil, err
		}
		queue.Name = queueName
	}

	return &Consumer{
		channel:      ch,
		queue:        queue.Name,
		handlers:     make(map[string]MessageHandler),
		conn:         conn,
		declaredName: queueName,
		durable:      durable,
		autoDelete:   autoDelete,
		messageTTL:   messageTTL,
		maxMessages:  maxMessages,
	}, nil
}

// buildQueueArguments constructs the amqp.Table of arguments for queue declaration.
// DLQ routing is only added for durable queues without TTL or max-length limits.
// Queues with those limits are high-throughput (e.g. log streams) where TTL expiry
// and overflow are expected operational conditions — routing them to the DLQ would
// flood it. Low-volume, critical queues (e.g. lifecycle events) have no limits and
// should dead-letter on failure.
// Non-durable, non-auto-delete queues get x-expires as a safety net for orphan cleanup.
func buildQueueArguments(queueName string, durable, autoDelete bool, messageTTL, maxMessages int) amqp.Table {
	var arguments amqp.Table

	if durable && messageTTL == 0 && maxMessages == 0 {
		dlqName := queueName + "-dlq"
		arguments = amqp.Table{
			"x-dead-letter-exchange":    "", // Use default exchange
			"x-dead-letter-routing-key": dlqName,
		}
	}

	// Add x-expires only for non-durable, non-auto-delete queues as a safety net
	// for orphaned queues. Durable queues are long-lived and should not expire.
	if !durable && !autoDelete {
		if arguments == nil {
			arguments = amqp.Table{}
		}
		arguments["x-expires"] = 300000 // 5 minutes in milliseconds
	}

	// Add message TTL if specified (prevents unbounded memory growth)
	if messageTTL > 0 {
		if arguments == nil {
			arguments = amqp.Table{}
		}
		arguments["x-message-ttl"] = messageTTL
	}

	// Add max messages limit if specified (prevents unbounded queue growth)
	if maxMessages > 0 {
		if arguments == nil {
			arguments = amqp.Table{}
		}
		arguments["x-max-length"] = maxMessages
		arguments["x-overflow"] = "drop-head" // Drop oldest messages when limit is reached
	}

	return arguments
}

// BindExchange binds the consumer's queue to an exchange with routing keys.
// The binding is stored so it can be reapplied after a connection reset.
func (c *Consumer) BindExchange(exchange string, routingKeys []string) error {
	c.mu.Lock()
	ch := c.channel
	c.mu.Unlock()

	for _, key := range routingKeys {
		if err := ch.QueueBind(c.queue, key, exchange, false, nil); err != nil {
			return fmt.Errorf("failed to bind queue to exchange: %w", err)
		}
	}

	keysCopy := append([]string(nil), routingKeys...)
	c.mu.Lock()
	c.bindings = append(c.bindings, binding{exchange: exchange, routingKeys: keysCopy})
	c.mu.Unlock()
	return nil
}

// RegisterHandler registers a message handler for a specific routing key pattern
func (c *Consumer) RegisterHandler(routingKeyPattern string, handler MessageHandler) {
	c.handlers[routingKeyPattern] = handler
}

// Start starts consuming messages. If the AMQP channel closes unexpectedly
// (connection blip, broker restart, flow-control teardown) it reopens the
// channel and resumes — so the consumer never silently dies mid-session.
func (c *Consumer) Start(ctx context.Context) error {
	msgs, err := c.startConsuming()
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
					// Channel closed — reconnect loop.
					for {
						if ctx.Err() != nil {
							return
						}
						log.Printf("consumer channel closed, reconnecting")
						newMsgs, err := c.startConsuming()
						if err != nil {
							log.Printf("consumer reconnect failed: %v, retrying", err)
							continue
						}
						msgs = newMsgs
						break
					}
					continue
				}
				c.handleMessage(ctx, msg)
			}
		}
	}()

	return nil
}

// startConsuming (re)opens a channel and fully reinitializes the queue and
// bindings. Called on initial setup and after any connection/channel reset.
// For durable queues, QueueDeclare is idempotent when args match. On
// PRECONDITION_FAILED (args changed between deploys) we log a warning and
// proceed — the existing queue is still usable and will be replaced naturally
// as sessions end and new ones start with the updated args.
func (c *Consumer) startConsuming() (<-chan amqp.Delivery, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	if err := ch.Qos(1, 0, false); err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to set QoS: %w", err)
	}

	c.mu.Lock()
	durable := c.durable
	autoDelete := c.autoDelete
	messageTTL := c.messageTTL
	maxMessages := c.maxMessages
	declaredName := c.declaredName
	bindings := append(c.bindings[:0:0], c.bindings...)
	for i := range bindings {
		bindings[i].routingKeys = append(bindings[i].routingKeys[:0:0], bindings[i].routingKeys...)
	}
	c.mu.Unlock()

	// reopenChannel closes ch and opens a fresh one with QoS applied.
	// Required after PRECONDITION_FAILED because RabbitMQ closes the channel on 406.
	reopenChannel := func() error {
		ch.Close()
		var err error
		ch, err = c.conn.Channel()
		if err != nil {
			return fmt.Errorf("failed to reopen channel: %w", err)
		}
		if err := ch.Qos(1, 0, false); err != nil {
			ch.Close()
			return fmt.Errorf("failed to set QoS: %w", err)
		}
		return nil
	}

	if durable {
		dlqName := declaredName + "-dlq"
		if _, err := ch.QueueDeclare(dlqName, true, false, false, false, nil); err != nil {
			if !isPreconditionFailed(err) {
				ch.Close()
				return nil, fmt.Errorf("failed to declare DLQ: %w", err)
			}
			log.Printf("DLQ %s already exists with different args, using as-is: %v", dlqName, err)
			if err := reopenChannel(); err != nil {
				return nil, err
			}
		}
	}

	arguments := buildQueueArguments(declaredName, durable, autoDelete, messageTTL, maxMessages)
	if _, err := ch.QueueDeclare(c.queue, durable, autoDelete, false, false, arguments); err != nil {
		if !isPreconditionFailed(err) {
			ch.Close()
			return nil, fmt.Errorf("failed to declare queue: %w", err)
		}
		log.Printf("queue %s already exists with different args, using as-is: %v", c.queue, err)
		if err := reopenChannel(); err != nil {
			return nil, err
		}
	}

	for _, b := range bindings {
		for _, key := range b.routingKeys {
			if err := ch.QueueBind(c.queue, key, b.exchange, false, nil); err != nil {
				ch.Close()
				return nil, fmt.Errorf("failed to bind queue: %w", err)
			}
		}
	}

	msgs, err := ch.Consume(c.queue, "", false, false, false, false, nil)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("failed to register consumer: %w", err)
	}

	c.mu.Lock()
	c.channel = ch
	c.mu.Unlock()
	return msgs, nil
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

	// Extract trace context from AMQP headers so this message is linked to
	// the publisher's span as a child.
	carrier := propagation.MapCarrier{}
	for k, v := range delivery.Headers {
		if s, ok := v.(string); ok {
			carrier[k] = s
		}
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)

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

		// Check if it's a permanent error or max retries exceeded.
		// Note: whether the message reaches a DLQ depends on whether the queue
		// was declared with dead-letter routing. Queues with TTL/max-length limits
		// do not have DLQ routing and will discard the message on Nack.
		if IsPermanentError(err) {
			log.Printf("Permanent error - discarding message (DLQ if configured): %v", err)
			delivery.Nack(false, false)
		} else if retryCount >= maxRetries {
			log.Printf("Max retries (%d) exceeded - discarding message (DLQ if configured)", maxRetries)
			delivery.Nack(false, false)
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
// It automatically reconnects to RabbitMQ if the channel is closed
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

	// First attempt
	c.mu.Lock()
	ch := c.channel
	c.mu.Unlock()

	publishErr := ch.PublishWithContext(
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

	// If publish succeeded, return
	if publishErr == nil {
		log.Printf("Successfully sent reply to %s", replyTo)
		return
	}

	// If the channel is closed, try to recreate and retry once
	if isChannelClosed(publishErr) {
		log.Printf("Channel closed while sending reply, attempting to recreate channel")
		c.mu.Lock()
		// Recreate the channel
		newCh, recreateErr := c.conn.Channel()
		if recreateErr != nil {
			c.mu.Unlock()
			log.Printf("Failed to recreate channel for reply: %v (original error: %v)", recreateErr, publishErr)
			return
		}

		c.channel = newCh
		ch = newCh
		c.mu.Unlock()

		// Retry the publish once
		retryErr := ch.PublishWithContext(
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

		if retryErr != nil {
			log.Printf("Failed to send reply after channel recreation: %v", retryErr)
		} else {
			log.Printf("Successfully sent reply to %s after channel recreation", replyTo)
		}
		return
	}

	log.Printf("Failed to send reply to %s: %v", replyTo, publishErr)
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

// DeleteQueue deletes the queue associated with this consumer
// This should be called before Close() to remove the queue from RabbitMQ
func (c *Consumer) DeleteQueue() error {
	c.mu.Lock()
	ch := c.channel
	c.mu.Unlock()

	if ch == nil {
		return fmt.Errorf("channel is nil")
	}

	_, err := ch.QueueDelete(
		c.queue, // queue name
		false,   // ifUnused - delete even if there are consumers
		false,   // ifEmpty - delete even if there are messages
		false,   // noWait
	)
	if err != nil {
		return fmt.Errorf("failed to delete queue %s: %w", c.queue, err)
	}

	log.Printf("Deleted queue: %s", c.queue)
	return nil
}

// Close closes the consumer channel
func (c *Consumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.channel != nil {
		return c.channel.Close()
	}
	return nil
}
