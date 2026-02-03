package handlers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/whale-net/everything/libs/go/rmq"
)

// CommandPublisher publishes commands to RabbitMQ for host managers
type CommandPublisher struct {
	publisher *rmq.Publisher
}

// NewCommandPublisher creates a new command publisher
func NewCommandPublisher(conn *rmq.Connection) (*CommandPublisher, error) {
	publisher, err := rmq.NewPublisher(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}

	return &CommandPublisher{
		publisher: publisher,
	}, nil
}

// PublishStartSession publishes a start session command to the host
func (p *CommandPublisher) PublishStartSession(ctx context.Context, serverID int64, cmd interface{}) error {
	routingKey := fmt.Sprintf("command.host.%d.session.start", serverID)
	return p.publish(ctx, routingKey, cmd)
}

// PublishStopSession publishes a stop session command to the host
func (p *CommandPublisher) PublishStopSession(ctx context.Context, serverID int64, cmd interface{}) error {
	routingKey := fmt.Sprintf("command.host.%d.session.stop", serverID)
	return p.publish(ctx, routingKey, cmd)
}

func (p *CommandPublisher) publish(ctx context.Context, routingKey string, data interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal command: %w", err)
	}

	if err := p.publisher.Publish(ctx, "manman", routingKey, body); err != nil {
		return fmt.Errorf("failed to publish command: %w", err)
	}

	return nil
}

// Close closes the publisher
func (p *CommandPublisher) Close() error {
	return p.publisher.Close()
}
