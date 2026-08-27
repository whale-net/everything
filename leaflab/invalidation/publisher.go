package invalidation

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/whale-net/everything/libs/go/rmq"
)

// ExchangeName is the fanout exchange every LeafLab writer publishes
// invalidation Events to, and every Subscriber binds its own exclusive
// queue to. Fanout -- not the "amq.topic" exchange the device-facing MQTT
// traffic uses -- is the mechanism choice: every queue bound to a fanout
// exchange gets its own copy of every message, so every subscribing
// process's replica observes every event, rather than one of a
// competing-consumer set winning it. See this package's doc comment.
const ExchangeName = "leaflab.invalidation"

// Publisher broadcasts invalidation Events to every current Subscriber.
// It is safe for concurrent use.
type Publisher struct {
	channel *amqp.Channel
}

// NewPublisher declares ExchangeName (idempotent -- a no-op if it already
// exists with the same arguments) and returns a Publisher bound to it.
func NewPublisher(conn *rmq.Connection) (*Publisher, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("invalidation: open channel: %w", err)
	}
	if err := ch.ExchangeDeclare(ExchangeName, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		ch.Close()
		return nil, fmt.Errorf("invalidation: declare exchange %s: %w", ExchangeName, err)
	}
	return &Publisher{channel: ch}, nil
}

// Publish broadcasts ev to every process currently subscribed.
//
// Callers must call Publish only after the write ev describes has
// committed -- never before. Publishing ahead of the commit would let a
// Subscriber's handler re-read the database (the idempotent, self-healing
// design Event's doc comment describes) and still observe the pre-commit
// value, defeating FR73's guarantee rather than satisfying it.
func (p *Publisher) Publish(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("invalidation: marshal event: %w", err)
	}
	if err := p.channel.PublishWithContext(ctx, ExchangeName, "", false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	}); err != nil {
		return fmt.Errorf("invalidation: publish event: %w", err)
	}
	return nil
}

// Close closes the underlying channel.
func (p *Publisher) Close() error {
	return p.channel.Close()
}
