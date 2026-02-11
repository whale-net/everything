package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/whale-net/everything/libs/go/rmq"
)

// CommandResponse represents a response from a host manager
type CommandResponse struct {
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
	CorrelationID string `json:"correlation_id"`
}

// CommandPublisher publishes commands to RabbitMQ for host managers with RPC support
type CommandPublisher struct {
	publisher    *rmq.Publisher
	consumer     *rmq.Consumer
	replyQueue   string
	pendingCalls sync.Map // map[correlationID]chan CommandResponse
}

// NewCommandPublisher creates a new command publisher with RPC support
func NewCommandPublisher(conn *rmq.Connection) (*CommandPublisher, error) {
	publisher, err := rmq.NewPublisher(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}

	// Create unique reply queue for this API instance (non-durable, auto-delete)
	// No message limits needed for reply queue (transient, low volume)
	replyQueue := fmt.Sprintf("api-replies-%s", uuid.New().String())
	consumer, err := rmq.NewConsumerWithOpts(conn, replyQueue, false, true, 0, 0)
	if err != nil {
		publisher.Close()
		return nil, fmt.Errorf("failed to create reply consumer: %w", err)
	}

	cp := &CommandPublisher{
		publisher:  publisher,
		consumer:   consumer,
		replyQueue: replyQueue,
	}

	// Register handler for reply messages
	consumer.RegisterHandler("#", cp.handleReply)

	return cp, nil
}

// Start starts the reply consumer
func (p *CommandPublisher) Start(ctx context.Context) error {
	return p.consumer.Start(ctx)
}

// PublishStartSession publishes a start session command and waits for response
func (p *CommandPublisher) PublishStartSession(ctx context.Context, serverID int64, cmd interface{}, timeout time.Duration) error {
	routingKey := fmt.Sprintf("command.host.%d.session.start", serverID)
	return p.publishAndWait(ctx, routingKey, cmd, timeout)
}

// PublishStopSession publishes a stop session command and waits for response
func (p *CommandPublisher) PublishStopSession(ctx context.Context, serverID int64, cmd interface{}, timeout time.Duration) error {
	routingKey := fmt.Sprintf("command.host.%d.session.stop", serverID)
	return p.publishAndWait(ctx, routingKey, cmd, timeout)
}

func (p *CommandPublisher) publishAndWait(ctx context.Context, routingKey string, data interface{}, timeout time.Duration) error {
	// Generate correlation ID
	correlationID := uuid.New().String()
	log.Printf("[rpc] publishing command to %s (correlation_id=%s, timeout=%v)...", routingKey, correlationID, timeout)

	// Create response channel
	respChan := make(chan CommandResponse, 1)
	p.pendingCalls.Store(correlationID, respChan)
	defer p.pendingCalls.Delete(correlationID)

	// Marshal command
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	// Publish with reply_to and correlation_id
	if err := p.publisher.PublishWithReply(ctx, "manman", routingKey, body, p.replyQueue, correlationID); err != nil {
		log.Printf("[rpc] error: failed to publish command %s: %v", correlationID, err)
		return fmt.Errorf("failed to publish command: %w", err)
	}

	// Wait for response with timeout
	select {
	case <-ctx.Done():
		log.Printf("[rpc] context cancelled while waiting for response %s", correlationID)
		return ctx.Err()
	case <-time.After(timeout):
		log.Printf("[rpc] command %s timed out after %v", correlationID, timeout)
		return fmt.Errorf("command timeout after %v", timeout)
	case resp := <-respChan:
		if !resp.Success {
			log.Printf("[rpc] command %s failed: %s", correlationID, resp.Error)
			return fmt.Errorf("command failed: %s", resp.Error)
		}
		log.Printf("[rpc] command %s succeeded", correlationID)
		return nil
	}
}

func (p *CommandPublisher) handleReply(ctx context.Context, msg rmq.Message) error {
	var resp CommandResponse
	if err := json.Unmarshal(msg.Body, &resp); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Find pending call and send response
	if ch, ok := p.pendingCalls.Load(resp.CorrelationID); ok {
		respChan := ch.(chan CommandResponse)
		select {
		case respChan <- resp:
		default:
			// Channel already closed or filled
		}
	}

	return nil
}

// Close closes the publisher and consumer
func (p *CommandPublisher) Close() error {
	p.consumer.Close()
	return p.publisher.Close()
}
