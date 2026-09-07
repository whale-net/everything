package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/whale-net/everything/manmanv2/api/repository"
)

// PendingRestartReaper is the time-based stall bound for durable restart
// (Track B -- #1712 FR11, NFR11, NFR12). SessionRestartConsumer
// (session_restart_consumer.go, #1731) only ever resolves a pending_restarts
// row on the happy path: a gating Stop that reaches a terminal status. A Stop
// that never converges (host manager gone, container wedged) leaves that row
// 'pending' forever with nothing to resolve it -- exactly the "stuck pending
// forever" failure FR11 exists to prevent, merely relocated from a goroutine
// into a table. This reaper is that resolver.
//
// It is deliberately a periodic ticker, not event-driven -- #1712's NFR9
// ("not a periodic sweep") scopes only to the normal-path dispatch
// (SessionRestartConsumer above); NFR12 explicitly permits a time-based
// safety net here. Do not try to make this event-driven.
//
// Modelled on SessionStatusHandler.StartStaleSessionChecker
// (manmanv2/processor/handlers/session_status.go): a ticker goroutine,
// select on ctx.Done(), INFO log on start/stop.
type PendingRestartReaper struct {
	pendingRepo repository.PendingRestartRepository
	interval    time.Duration
	logger      *slog.Logger
}

// NewPendingRestartReaper constructs a reaper that, once Start(ctx) is
// called, expires stalled pending_restarts rows every interval. It does not
// start ticking on construction -- call Start(ctx) for that.
func NewPendingRestartReaper(
	pendingRepo repository.PendingRestartRepository,
	interval time.Duration,
	logger *slog.Logger,
) *PendingRestartReaper {
	return &PendingRestartReaper{
		pendingRepo: pendingRepo,
		interval:    interval,
		logger:      logger,
	}
}

// Start spawns the ticker goroutine. It returns immediately; the goroutine
// runs until ctx is cancelled.
//
// See #1732's Implementation section for the full per-tick contract (WARNING
// per expired record with sgc id/gating session id/record id, ERROR + skip
// on ExpireStalled failure, silent on zero expired). tick is a stub pending
// Implementation.
func (r *PendingRestartReaper) Start(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	go func() {
		defer ticker.Stop()
		r.logger.Info("starting pending restart reaper", "interval", r.interval)

		for {
			select {
			case <-ctx.Done():
				r.logger.Info("stopping pending restart reaper")
				return
			case <-ticker.C:
				r.tick(ctx)
			}
		}
	}()
}

// tick runs a single expiry pass. Errors must never kill the goroutine --
// the next tick retries.
func (r *PendingRestartReaper) tick(ctx context.Context) {
	// TODO(#1732 Implementation): call r.pendingRepo.ExpireStalled(ctx,
	// time.Now()); on error, log ERROR and return (next tick retries); for
	// each expired record, log WARNING with server_game_config_id,
	// gating_session_id, pending_restart_id, created_at, stall_deadline, and
	// an explicit message that the restart's Stop never reached a terminal
	// state so the Start was not dispatched and the deployment remains
	// stopped. Zero expired records is silent.
	_ = ctx
}
