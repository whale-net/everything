package handlers

import (
	"context"
	"log/slog"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// DeferredStarter is the narrow interface SessionRestartConsumer needs over
// the deferred Start (SessionHandler.StartSession). It exists so the
// consumer is unit-testable with a fake instead of a full SessionHandler,
// and so this file never duplicates Start's business logic -- it only ever
// calls through to it.
type DeferredStarter interface {
	StartSession(ctx context.Context, req *pb.StartSessionRequest) (*pb.StartSessionResponse, error)
}

// SessionRestartConsumer is a second, independent status.session.* consumer
// living inside control-api (Track B, durable restart -- #1712 FR9 back
// half, NFR9, NFR10). It fires the deferred StartSession the moment the
// gating Stop for a pending_restarts record reaches a terminal status, so
// restart survives a control-api pod restart: the intent lives in Postgres
// (#1729), the Stop is already dispatched (#1730), and this consumer closes
// the loop from whichever control-api replica happens to be alive when the
// terminal status arrives.
//
// It binds status.session.# on the shared "manman" exchange directly, the
// same way event-processor's persistence consumer
// (manmanv2/processor/consumer) does -- but on its own dedicated queue, not
// event-processor's "processor-events" queue. Sharing a queue would make
// the two consumers compete for messages instead of both seeing every one.
// This consumer is read-only with respect to status.session.* and
// manmanv2.htmxsse: it never publishes to either. Event-processor keeps
// sole ownership of persisting status.
type SessionRestartConsumer struct {
	pendingRepo repository.PendingRestartRepository
	sessions    repository.SessionRepository
	starter     DeferredStarter
	consumer    *rmq.Consumer
	logger      *slog.Logger
}

// sessionRestartConsumerQueue is the dedicated queue name for this
// consumer's status.session.# binding. Distinct from event-processor's
// "processor-events" queue by design -- see the SessionRestartConsumer doc
// comment.
const sessionRestartConsumerQueue = "control-api.session.restart"

// NewSessionRestartConsumer creates the consumer, declares its dedicated
// queue, and binds it to status.session.# on the "manman" exchange. It does
// not start consuming -- call Start(ctx) for that.
func NewSessionRestartConsumer(
	pendingRepo repository.PendingRestartRepository,
	sessions repository.SessionRepository,
	starter DeferredStarter,
	rmqConn *rmq.Connection,
	logger *slog.Logger,
) (*SessionRestartConsumer, error) {
	consumer, err := rmq.NewConsumerWithOpts(rmqConn, sessionRestartConsumerQueue, false, false, 0, 0)
	if err != nil {
		return nil, err
	}

	if err := consumer.BindExchange("manman", []string{"status.session.#"}); err != nil {
		consumer.Close()
		return nil, err
	}

	h := &SessionRestartConsumer{
		pendingRepo: pendingRepo,
		sessions:    sessions,
		starter:     starter,
		consumer:    consumer,
		logger:      logger,
	}

	consumer.RegisterHandler("status.session.#", h.handleStatusUpdate)

	return h, nil
}

// Start starts consuming status update messages. It blocks until ctx is
// cancelled or the underlying consumer returns an error.
func (h *SessionRestartConsumer) Start(ctx context.Context) error {
	return h.consumer.Start(ctx)
}

// Close closes the consumer's channel.
func (h *SessionRestartConsumer) Close() error {
	return h.consumer.Close()
}

// handleStatusUpdate processes a status.session.* message and, if it
// terminalizes the gating Stop of a pending restart, fires the deferred
// Start. See #1731's Implementation section for the full contract; the
// scaffold stub below is filled in during Implementation.
func (h *SessionRestartConsumer) handleStatusUpdate(ctx context.Context, msg rmq.Message) error {
	// TODO(#1731 Implementation): unmarshal msg.Body into
	// manmanv2/host/rmq.SessionStatusUpdate; fast-path out for non-terminal
	// statuses; ClaimForSession; call h.starter.StartSession with a bounded
	// context; MarkStarted/MarkFailed accordingly. See issue body for the
	// exact contract (NFR7 unknown-field tolerance, NFR9 no periodic sweep,
	// NFR10 idempotency via the atomic claim).
	_ = ctx
	_ = msg
	return nil
}
