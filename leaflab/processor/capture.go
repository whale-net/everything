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
