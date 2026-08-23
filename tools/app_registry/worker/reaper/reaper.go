// Package reaper implements the AR-7b (issue #558) stale-artifact sweep: a
// periodic loop, run alongside outbox.Drainer in app-registry-worker, that
// expires stale "allocated" and "publishing" artifact rows to "failed" with
// reason "stale". See ARCHITECTURE.md "The reaper is not optional" and
// PLAN.md's AR-7b scope.
//
// This ships in the SAME phase as the allocated/publishing states
// themselves, not later: an "allocated" row is written up front by the
// release plan step, before anything is pushed, for every domain -- a
// cancelled or killed run would otherwise hold a version number in the
// (owner_id, kind, version) unique index forever, permanently blocking that
// owner's next release.
package reaper

import (
	"context"
	"log/slog"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// Reaper periodically calls repository.ArtifactRepository.ExpireStale
// against a direct Postgres connection -- mirroring outbox.Drainer's Store
// field, this is deliberately not routed through the gRPC API, so the
// reaper keeps sweeping even if the app-registry-api process itself is
// down.
type Reaper struct {
	// Store is the artifact repository, backed by the same Postgres
	// database the App Registry API writes to (see
	// server/repository/postgres).
	Store repository.ArtifactRepository
	// Timeout is how long a row may sit in "allocated" or "publishing"
	// (measured from state_changed_at) before the next sweep moves it to
	// "failed" with reason "stale". See ENV.md's ARTIFACT_REAPER_TIMEOUT.
	Timeout time.Duration
	// PollInterval is the delay between sweep passes. See ENV.md's
	// ARTIFACT_REAPER_POLL_INTERVAL.
	PollInterval time.Duration
	// Logger receives per-pass and per-error events. If nil, slog.Default()
	// is used.
	Logger *slog.Logger
}

func (rp *Reaper) logger() *slog.Logger {
	if rp.Logger != nil {
		return rp.Logger
	}
	return slog.Default()
}

// Run sweeps once immediately -- so a worker restart doesn't leave an
// already-stale row idle for a full PollInterval, mirroring
// outbox.Drainer.Run's same reasoning -- then again on every PollInterval
// tick until ctx is cancelled. A sweep error is logged, not fatal: the next
// tick tries again, matching ARCHITECTURE.md's "the registry can be down
// without blocking a deploy" posture applied to the worker's own
// dependencies.
func (rp *Reaper) Run(ctx context.Context) error {
	ticker := time.NewTicker(rp.PollInterval)
	defer ticker.Stop()

	rp.sweepOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			rp.sweepOnce(ctx)
		}
	}
}

func (rp *Reaper) sweepOnce(ctx context.Context) {
	n, err := rp.Store.ExpireStale(ctx, rp.Timeout)
	if err != nil {
		rp.logger().Error("artifact reaper sweep failed", "error", err)
		return
	}
	if n > 0 {
		rp.logger().Info("expired stale artifacts", "count", n, "timeout", rp.Timeout)
	}
}
