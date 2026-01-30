package rmq

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/libs/go/rmq"
)

// Publisher publishes status updates to RabbitMQ
type Publisher struct {
	publisher *rmq.Publisher
	serverID  int64
}

// NewPublisher creates a new status publisher
func NewPublisher(conn *rmq.Connection, serverID int64) (*Publisher, error) {
	publisher, err := rmq.NewPublisher(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}

	return &Publisher{
		publisher: publisher,
		serverID:  serverID,
	}, nil
}

// PublishHostStatus publishes a host status update
func (p *Publisher) PublishHostStatus(ctx context.Context, status string) error {
	update := HostStatusUpdate{
		ServerID: p.serverID,
		Status:   status,
	}
	routingKey := fmt.Sprintf("status.host.%d", p.serverID)
	return p.publisher.Publish(ctx, "manman", routingKey, update)
}

// PublishSessionStatus publishes a session status update
func (p *Publisher) PublishSessionStatus(ctx context.Context, update *SessionStatusUpdate) error {
	routingKey := fmt.Sprintf("status.session.%d", update.SessionID)
	return p.publisher.Publish(ctx, "manman", routingKey, update)
}

// PublishHealth publishes a health/keepalive message with optional session stats
func (p *Publisher) PublishHealth(ctx context.Context, stats *SessionStats) error {
	update := HealthUpdate{
		ServerID:     p.serverID,
		SessionStats: stats,
	}
	routingKey := fmt.Sprintf("health.host.%d", p.serverID)
	return p.publisher.Publish(ctx, "manman", routingKey, update)
}

// Close closes the publisher
func (p *Publisher) Close() error {
	return p.publisher.Close()
}
