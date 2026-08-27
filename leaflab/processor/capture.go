package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/whale-net/everything/leaflab/api/capture"
)

// captureCompleterInterval is how often the processor ticks
// capture.Completer.RunPending (FR20 phase two). It is well inside
// migration 022's capture_completion_window (tiers.CaptureCompletionWindow,
// 1 hour) -- the deadline every capture has to complete after its bucket
// closes -- so a bucket gets several ticks' worth of chances to be picked
// up before that window closes, even if a single RunPending call is slow
// or fails outright.
const captureCompleterInterval = 5 * time.Minute

// capturePartialRetentionInterval is how often the processor ticks
// capture.Completer.PruneExpiredPartials (boundary_partial's differential
// retention, migration 033's "Retention" comment, FR20.2). Retention is
// bounded by tiers.FiveMinuteRetention (90 days), so unlike the completer
// -- which races NFR5's tight raw-retention deadline -- pruning has no
// benefit from a short tick: a day's slack against a 90-day window is
// immaterial, and ticking this rarely keeps the extra DELETE off the hot
// 5-minute cadence the completer already uses.
const capturePartialRetentionInterval = 24 * time.Hour

// runCaptureCompleter ticks completer.RunPending on captureCompleterInterval
// until ctx is cancelled. It runs as its own goroutine inside
// leaflab/processor (started from run(), main.go) rather than as a separate
// scheduled job -- see leaflab/api/capture's package doc comment for why:
// the processor already holds the long-lived pgxpool RunPending needs and
// is deployed single-replica (BUILD.bazel's release_app app_type =
// "worker"), which is what keeps NFR5's ordering constraint easy to reason
// about against migration 022's refresh/retention ordering, without a
// second scheduling interval layered on top.
//
// A RunPending error -- including NFR5's ErrPendingNearRetention -- is
// logged loudly and the loop keeps ticking: a transient DB blip on one tick
// must not permanently stop every future attempt to catch up, and
// ErrPendingNearRetention is itself meant to keep firing on every tick
// until whatever is blocking completion is fixed, not to crash the one
// process able to resolve it.
func runCaptureCompleter(ctx context.Context, logger *slog.Logger, completer *capture.Completer) {
	ticker := time.NewTicker(captureCompleterInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := completer.RunPending(ctx); err != nil {
				logger.Error("boundary capture completer run failed", "err", err)
			}
		}
	}
}

// runCapturePartialRetention ticks completer.PruneExpiredPartials on
// capturePartialRetentionInterval until ctx is cancelled -- boundary_partial's
// differential retention (migration 033's "Retention" comment, FR20.2):
// five_minute-tier partials older than tiers.FiveMinuteRetention are
// dropped; hourly-tier partials are never touched. Runs as its own
// goroutine in this same process for the same reason the completer does
// (see runCaptureCompleter's doc comment) -- it needs no scheduling
// infrastructure beyond the pgxpool this process already holds -- but on
// its own, much longer ticker, since retention has none of the
// completer's tight NFR5 ordering deadline.
//
// A PruneExpiredPartials error is logged loudly and the loop keeps
// ticking: a transient DB blip on one tick must not permanently stop every
// future attempt to catch up, since falling behind here only ever means
// old five_minute partials linger a little longer, never data loss.
func runCapturePartialRetention(ctx context.Context, logger *slog.Logger, completer *capture.Completer) {
	ticker := time.NewTicker(capturePartialRetentionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := completer.PruneExpiredPartials(ctx); err != nil {
				logger.Error("boundary_partial retention prune failed", "err", err)
			}
		}
	}
}
