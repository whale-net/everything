package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/whale-net/everything/libs/go/rmq"
	"github.com/whale-net/everything/manmanv2/api/repository"
	hostrmq "github.com/whale-net/everything/manmanv2/host/rmq"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// startSessionTimeout bounds the deferred StartSession call so a hung Start
// can't wedge this consumer's message-processing loop (see handleStatusUpdate).
const startSessionTimeout = 30 * time.Second

// startSessionRetryAttempts and startSessionRetryBackoff bound a retry of the
// deferred StartSession call on codes.FailedPrecondition. This absorbs the
// race between this consumer's read of active sessions for the SGC and
// event-processor's independent commit of the terminal status that unblocks
// it (see #1712 FR9, #2059): the same status.session.* message is delivered
// to both consumers with no ordering guarantee between them, so
// StartSession's precondition check can occasionally lose that race by a
// margin of milliseconds to low seconds. The total retry budget (~5s across
// up to 5 attempts) is deliberately small and stays far inside both
// startSessionTimeout (30s) and the stall reaper's RESTART_STALL_TIMEOUT
// default (45s, #1732) -- it exists only to absorb a commit race measured in
// milliseconds, not to paper over a genuine problem.
const (
	startSessionRetryAttempts = 5
	startSessionRetryBackoff  = 1 * time.Second
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
// Start.
//
// The pending record is claimed (moved to 'started') *before* the Start is
// attempted -- ClaimForSession's atomic UPDATE...WHERE status='pending' is
// the entire NFR10 idempotency guarantee, since a redelivered or duplicated
// terminal message will find the record already claimed and do nothing.
// The deliberate trade this makes is at-most-once: a crash between the
// claim and the Start call leaves a 'started' record with no session and no
// automatic retry. That is intentional (FR10) -- a missed restart is
// recoverable by an operator, whereas two sessions racing to start against
// the same server_game_config_id is not. The stalled/never-started case is
// surfaced by a separate reaper/observability task, not this consumer.
func (h *SessionRestartConsumer) handleStatusUpdate(ctx context.Context, msg rmq.Message) error {
	var update hostrmq.SessionStatusUpdate
	if err := json.Unmarshal(msg.Body, &update); err != nil {
		// Malformed payload is a permanent error: log and drop rather than
		// requeue-loop it. Returning nil acks the message.
		h.logger.Warn("failed to unmarshal session status update", "error", err)
		return nil
	}

	// Fast path: the overwhelming majority of status.session.# traffic is
	// non-terminal (pending/starting/running/stopping) and must cost
	// nothing -- no repository access at all.
	switch update.Status {
	case manman.SessionStatusStopped, manman.SessionStatusCrashed, manman.SessionStatusLost:
	default:
		return nil
	}

	rec, err := h.pendingRepo.ClaimForSession(ctx, update.SessionID)
	if err != nil {
		// Unlike the failure paths below, this is a genuine (likely
		// transient) DB failure that happened before any claim took
		// effect, so let the caller's retry/requeue policy have a shot at
		// it instead of silently dropping the trigger.
		h.logger.Warn("failed to claim pending restart for session", "session_id", update.SessionID, "error", err)
		return err
	}
	if rec == nil {
		// No restart was pending for this session -- the common case, not
		// a warning.
		return nil
	}

	startCtx, cancel := context.WithTimeout(ctx, startSessionTimeout)
	defer cancel()

	var resp *pb.StartSessionResponse
	var startErr error
	var attempts int
	for attempts = 1; attempts <= startSessionRetryAttempts; attempts++ {
		resp, startErr = h.starter.StartSession(startCtx, &pb.StartSessionRequest{ServerGameConfigId: rec.ServerGameConfigID})
		if startErr == nil {
			break
		}
		var cordonErr *cordonError
		if errors.As(startErr, &cordonErr) {
			// A drain cordon rejection (#2364) is terminal, not the
			// transient commit-race FailedPrecondition this retry exists to
			// absorb: retrying against a drained host would just spin for
			// the whole backoff budget for no benefit. Fall through to the
			// failure path below, which marks the pending restart failed
			// with cordonErr's drain-specific message as the reason.
			break
		}
		if status.Code(startErr) != codes.FailedPrecondition {
			// Not the commit-race error this retry exists for: fail
			// immediately, exactly as before.
			break
		}
		if attempts == startSessionRetryAttempts {
			// Retry budget exhausted; fall through to the failure path
			// below without waiting again.
			break
		}
		select {
		case <-time.After(startSessionRetryBackoff):
		case <-startCtx.Done():
			// Context expired mid-retry: stop immediately rather than
			// sleeping past the deadline. startErr already holds the
			// last FailedPrecondition from the attempt above.
		}
		if startCtx.Err() != nil {
			break
		}
	}

	if startErr != nil {
		if markErr := h.pendingRepo.MarkFailed(ctx, rec.PendingRestartID, startErr.Error()); markErr != nil {
			h.logger.Warn("failed to mark pending restart as failed", "pending_restart_id", rec.PendingRestartID, "error", markErr)
		}
		h.logger.Warn("deferred restart start failed",
			"pending_restart_id", rec.PendingRestartID,
			"server_game_config_id", rec.ServerGameConfigID,
			"gating_session_id", rec.GatingSessionID,
			"attempts", attempts,
			"error", startErr)
		// Do not return the error: redelivery would find the record
		// already claimed and do nothing, so returning nil avoids
		// pointless retry churn.
		return nil
	}

	if err := h.pendingRepo.MarkStarted(ctx, rec.PendingRestartID, resp.Session.SessionId); err != nil {
		h.logger.Warn("failed to mark pending restart as started", "pending_restart_id", rec.PendingRestartID, "error", err)
		return nil
	}

	if attempts > 1 {
		h.logger.Warn("deferred restart start succeeded after retry",
			"pending_restart_id", rec.PendingRestartID,
			"server_game_config_id", rec.ServerGameConfigID,
			"attempts", attempts)
	}

	h.logger.Info("deferred restart start dispatched",
		"server_game_config_id", rec.ServerGameConfigID,
		"gating_session_id", rec.GatingSessionID,
		"started_session_id", resp.Session.SessionId)

	return nil
}
