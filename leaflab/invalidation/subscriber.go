package invalidation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/whale-net/everything/libs/go/rmq"
)

// Handler processes one invalidation Event. Per Event's doc comment, a
// Handler must be idempotent and should re-read the current value from the
// database rather than trust the event payload -- a Subscriber makes no
// stronger delivery guarantee than "every event is observed at least once
// while connected".
type Handler func(ctx context.Context, ev Event)

// Subscriber receives every Event published on ExchangeName, via its own
// exclusive, auto-delete, server-named queue bound to that fanout
// exchange -- so it never competes with another Subscriber for a message
// (see ExchangeName's doc comment on the every-replica constraint this
// satisfies, and this package's doc comment on why Phase 4 reuses it).
type Subscriber struct {
	channel *amqp.Channel
	queue   string
	logger  *slog.Logger
}

// NewSubscriber declares ExchangeName (idempotent) and a new exclusive
// queue bound to it. The queue -- and this Subscriber's view of every
// event published from here on -- is torn down when its channel or
// connection closes; a process that needs to catch up on events it missed
// while disconnected relies on a bounded staleness backstop (a periodic
// re-load), not on this queue surviving past a reconnect.
func NewSubscriber(conn *rmq.Connection, logger *slog.Logger) (*Subscriber, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("invalidation: open channel: %w", err)
	}
	if err := ch.ExchangeDeclare(ExchangeName, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		ch.Close()
		return nil, fmt.Errorf("invalidation: declare exchange %s: %w", ExchangeName, err)
	}
	// name="" -> broker-assigned unique name; autoDelete=true, exclusive=true
	// -- this queue exists only for this Subscriber's connection/channel.
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		ch.Close()
		return nil, fmt.Errorf("invalidation: declare subscriber queue: %w", err)
	}
	if err := ch.QueueBind(q.Name, "", ExchangeName, false, nil); err != nil {
		ch.Close()
		return nil, fmt.Errorf("invalidation: bind subscriber queue: %w", err)
	}
	return &Subscriber{channel: ch, queue: q.Name, logger: logger}, nil
}

// Start begins consuming events in the background and calls handle for
// each. It returns once consumption has started; delivery continues until
// ctx is cancelled or the underlying channel closes.
func (s *Subscriber) Start(ctx context.Context, handle Handler) error {
	msgs, err := s.channel.Consume(s.queue, "", true, true, false, false, nil)
	if err != nil {
		return fmt.Errorf("invalidation: consume: %w", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case d, ok := <-msgs:
				if !ok {
					return
				}
				var ev Event
				if err := json.Unmarshal(d.Body, &ev); err != nil {
					if s.logger != nil {
						s.logger.Warn("invalidation: dropping malformed event", "err", err)
					}
					continue
				}
				handle(ctx, ev)
			}
		}
	}()

	return nil
}

// Close closes the underlying channel, which also deletes this
// Subscriber's exclusive queue.
func (s *Subscriber) Close() error {
	return s.channel.Close()
}
