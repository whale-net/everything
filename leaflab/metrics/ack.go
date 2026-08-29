// Package metrics is NFR15's observability surface: it records the
// measurements NFR15 requires be observable -- push-to-ack duration per
// board and in aggregate, and pre-aggregated tier lag -- through
// libs/go/logging's shared OTel MeterProvider (logging.Meter), the same
// mechanism every other leaflab process's metrics go through.
//
// Instruments here are declared once, at package init, and shared by every
// caller in a process rather than re-created per call: an OTel histogram
// is itself the aggregate view (querying it with no attribute filter is
// "in aggregate"; filtering by board_id is "per board"), so one instrument
// satisfies both halves of "measurable per board and in aggregate" -- no
// separate aggregate-only instrument is needed.
//
// Wiring RecordPushToAck into leaflab/processor's ack write path
// (handleConfigAck -> AckDeviceConfig) and RecordTierLag into whichever
// process computes a pre-aggregated tier is Implementation-phase work; this
// package only declares the instruments and the recording functions so
// that wiring has something to call.
package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/whale-net/everything/libs/go/logging"
)

const meterName = "leaflab"

var (
	meter = logging.Meter(meterName)

	// pushToAckDuration is NFR15's "time from push to ack is measurable
	// per board and in aggregate" -- one histogram, attributed by
	// device_id (see RecordPushToAck), whose no-filter view is the
	// aggregate and whose device_id-filtered view is per-board.
	pushToAckDuration, _ = meter.Float64Histogram(
		"leaflab_config_push_to_ack_duration_seconds",
		metric.WithDescription("Time from a device config push to the board's ack being written, per board and in aggregate (NFR15)."),
		metric.WithUnit("s"),
	)

	// tierLag is NFR15's "pre-aggregated tiers lag raw readings by no
	// more than 5 minutes at p95" -- attributed by tier name so each
	// pre-aggregated tier's own lag is queryable independently.
	tierLag, _ = meter.Float64Histogram(
		"leaflab_reading_tier_lag_seconds",
		metric.WithDescription("Lag between a raw reading and its reflection in a pre-aggregated tier (NFR15)."),
		metric.WithUnit("s"),
	)
)

// RecordPushToAck records d, the duration between a device config version's
// pushed_at and acked_at, attributed by deviceID (the board's own
// self-reported identifier, the same dimension leaflab's logging already
// keys on -- not the internal board_id primary key). Call this once per
// ack, from the writer that observes both timestamps (leaflab/processor's
// handleConfigAck) -- never from a caller that only observes one side of
// the interval.
func RecordPushToAck(ctx context.Context, deviceID string, d time.Duration) {
	pushToAckDuration.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("device_id", deviceID),
	))
}

// RecordTierLag records d, the lag between a raw reading's recorded_at and
// its reflection in the named pre-aggregated tier, attributed by tier.
func RecordTierLag(ctx context.Context, tier string, d time.Duration) {
	tierLag.Record(ctx, d.Seconds(), metric.WithAttributes(
		attribute.String("tier", tier),
	))
}
