package main

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/whale-net/everything/leaflab/broadcast"
	"github.com/whale-net/everything/libs/go/rmq"
)

// AckListener subscribes to config-ack broadcast signals and delivers them to
// a ConfigAckWaiter. It uses leaflab/broadcast — the same fanout exchange
// leaflab/processor/cache.go's cache-invalidation listener subscribes to
// (FR73/#1203) — so there is one signalling mechanism, not a second one
// introduced for FR47/NFR15.
//
// One AckListener runs per API replica, each on its own private, ephemeral
// queue (see broadcast.NewListener): the exchange is a fanout, so every
// replica's listener receives every ack signal regardless of which replica
// the processor's message happens to be delivered "to" first — there is no
// "first" for a fanout, every bound queue gets a copy. This is what
// satisfies NFR15's "must reach every API replica" constraint; a shared
// durable queue name across replicas would not (that is a competing-consumer
// work queue and is explicitly disallowed).
type AckListener struct {
	logger   *slog.Logger
	consumer *rmq.Consumer
	repo     *Repository
	waiter   *ConfigAckWaiter
}

// NewAckListener creates a listener bound to the shared leaflab broadcast
// exchange. Call Start to begin consuming.
func NewAckListener(logger *slog.Logger, conn *rmq.Connection, repo *Repository, waiter *ConfigAckWaiter) (*AckListener, error) {
	consumer, err := broadcast.NewListener(conn, broadcast.Exchange)
	if err != nil {
		return nil, err
	}

	l := &AckListener{
		logger:   logger,
		consumer: consumer,
		repo:     repo,
		waiter:   waiter,
	}
	consumer.RegisterHandler(broadcast.RoutingKeyConfigAck, l.handle)
	return l, nil
}

func (l *AckListener) handle(ctx context.Context, msg rmq.Message) error {
	var signal broadcast.ConfigAckSignal
	if err := json.Unmarshal(msg.Body, &signal); err != nil {
		l.logger.Warn("failed to unmarshal config ack signal",
			"routing_key", msg.RoutingKey,
			"err", err)
		return nil // non-fatal, don't requeue
	}

	boardID, err := l.repo.GetOrCreateBoard(ctx, signal.DeviceID)
	if err != nil {
		l.logger.Warn("failed to look up board for ack notification",
			"device_id", signal.DeviceID,
			"err", err)
		return nil
	}

	l.waiter.NotifyAck(boardID, signal.ConfigVersion, signal.Accepted, signal.RejectionReason, signal.AckedAt)
	return nil
}

// Start begins consuming ack signals in the background. It returns once the
// consumer is registered; delivery happens on an internal goroutine until ctx
// is cancelled.
func (l *AckListener) Start(ctx context.Context) error {
	return l.consumer.Start(ctx)
}

// Close releases the underlying consumer's channel/queue.
func (l *AckListener) Close() error {
	return l.consumer.Close()
}
