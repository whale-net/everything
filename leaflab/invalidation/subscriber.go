package invalidation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/whale-net/everything/libs/go/rmq"
)

const (
	// reconnectMinDelay is the initial backoff after a Subscriber's channel
	// closes unexpectedly. Subsequent failures double the delay up to
	// reconnectMaxDelay. Mirrors libs/go/rmq.Consumer's own reconnect
	// backoff for the device-facing consumer.
	reconnectMinDelay = 1 * time.Second
	// reconnectMaxDelay caps the exponential backoff between reconnect attempts.
	reconnectMaxDelay = 30 * time.Second
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
	conn   *rmq.Connection
	logger *slog.Logger

	mu      sync.Mutex
	channel *amqp.Channel
	queue   string
}

// NewSubscriber declares ExchangeName (idempotent) and a new exclusive
// queue bound to it. The queue -- and this Subscriber's view of every
// event published from here on -- is torn down when its channel or
// connection closes; Start's reconnect handling redeclares a fresh one
// (see declare's doc comment), and a process that needs to catch up on
// events it missed while disconnected relies on a bounded staleness
// backstop (a periodic re-load), not on this queue surviving past a
// reconnect.
func NewSubscriber(conn *rmq.Connection, logger *slog.Logger) (*Subscriber, error) {
	s := &Subscriber{conn: conn, logger: logger}
	ch, queue, err := s.declare()
	if err != nil {
		return nil, err
	}
	s.channel = ch
	s.queue = queue
	return s, nil
}

// declare opens a fresh channel on s.conn, (re)declares ExchangeName
// (idempotent) and declares a new exclusive, auto-delete, server-named
// queue bound to it. Called at construction and again after every
// reconnect: an exclusive queue does not survive its channel or connection
// closing, so a reconnect must bind a brand new queue rather than re-attach
// to the old (now-gone) name.
func (s *Subscriber) declare() (*amqp.Channel, string, error) {
	ch, err := s.conn.Channel()
	if err != nil {
		return nil, "", fmt.Errorf("invalidation: open channel: %w", err)
	}
	if err := ch.ExchangeDeclare(ExchangeName, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		ch.Close()
		return nil, "", fmt.Errorf("invalidation: declare exchange %s: %w", ExchangeName, err)
	}
	// name="" -> broker-assigned unique name; autoDelete=true, exclusive=true
	// -- this queue exists only for this Subscriber's connection/channel.
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		ch.Close()
		return nil, "", fmt.Errorf("invalidation: declare subscriber queue: %w", err)
	}
	if err := ch.QueueBind(q.Name, "", ExchangeName, false, nil); err != nil {
		ch.Close()
		return nil, "", fmt.Errorf("invalidation: bind subscriber queue: %w", err)
	}
	return ch, q.Name, nil
}

// Start begins consuming events in the background and calls handle for
// each. It returns once consumption has started; delivery continues until
// ctx is cancelled.
//
// If the underlying channel closes unexpectedly (a RabbitMQ connection
// drop, a broker restart, flow-control teardown), Start transparently
// redeclares this Subscriber's exchange binding and queue with exponential
// backoff and resumes -- mirroring libs/go/rmq.Consumer's own reconnect
// handling for the device-facing consumer -- so a connection blip does not
// silently and permanently stop this process from observing invalidation
// events until restart. Any event published during the outage is not
// redelivered (the old exclusive queue is gone); that gap is what the
// bounded staleness backstop (a periodic full cache re-load) exists to
// self-heal.
func (s *Subscriber) Start(ctx context.Context, handle Handler) error {
	s.mu.Lock()
	ch, queue := s.channel, s.queue
	s.mu.Unlock()

	msgs, err := ch.Consume(queue, "", true, true, false, false, nil)
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
					newMsgs := s.reconnect(ctx)
					if newMsgs == nil {
						return // ctx cancelled while reconnecting
					}
					msgs = newMsgs
					continue
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

// reconnect redeclares this Subscriber's exchange binding and a fresh
// exclusive queue with exponential backoff, and returns the new delivery
// channel. It returns nil only if ctx is cancelled before a reconnect
// succeeds.
func (s *Subscriber) reconnect(ctx context.Context) <-chan amqp.Delivery {
	if s.logger != nil {
		s.logger.Warn("invalidation: subscriber channel closed, reconnecting")
	}

	retryDelay := reconnectMinDelay
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(retryDelay):
		}

		ch, queue, err := s.declare()
		if err == nil {
			msgs, consumeErr := ch.Consume(queue, "", true, true, false, false, nil)
			if consumeErr == nil {
				s.mu.Lock()
				s.channel, s.queue = ch, queue
				s.mu.Unlock()
				if s.logger != nil {
					s.logger.Info("invalidation: subscriber reconnected")
				}
				return msgs
			}
			ch.Close()
			err = consumeErr
		}

		nextDelay := retryDelay * 2
		if nextDelay > reconnectMaxDelay {
			nextDelay = reconnectMaxDelay
		}
		if s.logger != nil {
			s.logger.Warn("invalidation: subscriber reconnect failed, retrying", "err", err, "retry_in", nextDelay)
		}
		retryDelay = nextDelay
	}
}

// Close closes the underlying channel, which also deletes this
// Subscriber's exclusive queue.
func (s *Subscriber) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channel == nil {
		return nil
	}
	return s.channel.Close()
}
