package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/whale-net/everything/libs/go/rmq"
)

// PublisherInterface is the contract for publishing committed transcript
// events. Both the real Publisher and test fakes implement this interface,
// and it is what the session store's append path (whagent_net/session)
// depends on so a nil/disabled publisher can be substituted by config.
type PublisherInterface interface {
	Publish(ctx context.Context, event Event) error
}

// Publisher publishes committed transcript events onto ExchangeName. It is
// a thin wrapper over //libs/go/rmq's Publisher: this package owns the
// exchange identity and routing-key scheme, rmq owns the channel and
// reconnect mechanics.
//
// Callers (whagent_net/session's transcript append path) must publish
// after the owning Postgres commit, never before: a retry can then
// re-publish a duplicate, but can never publish an event that was never
// committed. Consumers dedup on EventID and order on Seq, so duplicate
// delivery is expected and safe.
type Publisher struct {
	pub *rmq.Publisher
}

var _ PublisherInterface = (*Publisher)(nil)

// NewPublisher opens a publisher against ExchangeName using conn.
func NewPublisher(conn *rmq.Connection) (*Publisher, error) {
	pub, err := rmq.NewPublisherWithExchange(conn, ExchangeName)
	if err != nil {
		return nil, fmt.Errorf("create %s publisher: %w", ExchangeName, err)
	}
	return &Publisher{pub: pub}, nil
}

// Publish marshals event as the message body and routes it on the
// session.{session_id}.{event_type} scheme (RoutingKey). Publish failures
// are the caller's to handle -- per NFR2, a failed publish must never roll
// back or fail the Postgres commit that already happened, only be logged.
func (p *Publisher) Publish(ctx context.Context, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return p.pub.Publish(ctx, ExchangeName, RoutingKey(event.SessionID, event.Type), body)
}

// Close closes the underlying rmq publisher.
func (p *Publisher) Close() error {
	return p.pub.Close()
}
