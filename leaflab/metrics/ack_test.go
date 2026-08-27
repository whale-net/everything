// NFR15: "time from push to ack is measurable per board and in
// aggregate" and "pre-aggregated tiers lag raw readings by no more than 5
// minutes at p95". This file proves the two instruments actually record
// data through a real OTel MeterProvider (a manual reader, so no network
// export needed -- same pattern as libs/go/logging/metrics_test.go), and
// that RecordPushToAck's device_id attribute is what makes "per board"
// distinct from "in aggregate" (the unfiltered histogram view). The
// literal p95 <= 2s / <= 5min bounds themselves are an operational
// alerting concern over the exported histogram in production, not
// something a unit test can assert without a real workload -- this file's
// job is proving the measurement exists and is correctly attributed, so
// that bound is observable at all. leaflab/api's
// nfr15_ack_broadcast_integration_test.go separately measures a real
// push-to-ack-observable-via-API duration against the 2s bound end to end.
package metrics

import (
	"context"
	"os"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// manualReader is installed exactly once for this whole test binary by
// TestMain, below. otel's global MeterProvider only ever delegates
// existing instruments (this package's package-level pushToAckDuration/
// tierLag, created once at package init) to the *first* real provider
// passed to otel.SetMeterProvider -- see go.opentelemetry.io/otel/
// internal/global's delegateMeterOnce; a *second* call updates only what
// future otel.Meter() lookups resolve to, not instruments that already
// delegated once. So every test in this file shares one reader/provider
// rather than each installing (and shutting down) its own, and
// distinguishes its own data by the distinct device_id/tier attribute
// values it records -- ManualReader's default cumulative temporality means
// every Collect call returns every distinct attribute set ever recorded in
// this process, not just what happened since the last collect.
var manualReader *sdkmetric.ManualReader

func TestMain(m *testing.M) {
	manualReader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(manualReader))
	otel.SetMeterProvider(mp)
	code := m.Run()
	_ = mp.Shutdown(context.Background())
	os.Exit(code)
}

// collect returns a fresh snapshot of every metric this test binary's
// shared manualReader has ever observed.
func collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := manualReader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return rm
}

// findHistogram locates the named metric among every collected
// ResourceMetrics scope, failing the test if it is not present.
func findHistogram(t *testing.T, rm *metricdata.ResourceMetrics, name string) metricdata.Histogram[float64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			h, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %q data type = %T, want metricdata.Histogram[float64]", name, m.Data)
			}
			return h
		}
	}
	t.Fatalf("metric %q not found among %d scope(s)", name, len(rm.ScopeMetrics))
	return metricdata.Histogram[float64]{}
}

// findDataPointByAttr returns the data point among h.DataPoints carrying
// attribute key=value, failing the test if none matches -- lets each test
// find only the data point(s) it itself recorded, ignoring any other
// test's, since manualReader accumulates across the whole binary (see
// TestMain's doc comment).
func findDataPointByAttr(t *testing.T, h metricdata.Histogram[float64], key, value string) metricdata.HistogramDataPoint[float64] {
	t.Helper()
	for _, dp := range h.DataPoints {
		for _, attr := range dp.Attributes.ToSlice() {
			if string(attr.Key) == key && attr.Value.AsString() == value {
				return dp
			}
		}
	}
	t.Fatalf("no data point found with attribute %s=%q among %d data point(s)", key, value, len(h.DataPoints))
	return metricdata.HistogramDataPoint[float64]{}
}

// TestRecordPushToAck_RecordsDurationAttributedByDeviceID proves
// RecordPushToAck's histogram carries a real data point with the recorded
// duration and a device_id attribute -- the dimension RecordPushToAck's
// doc comment says makes the same instrument's no-filter view "in
// aggregate" and its device_id-filtered view "per board".
func TestRecordPushToAck_RecordsDurationAttributedByDeviceID(t *testing.T) {
	ctx := context.Background()

	// Unique per test-run device_id values so findDataPointByAttr below
	// cannot accidentally match a data point some other test in this
	// binary recorded against the shared manualReader (see TestMain).
	const deviceA, deviceB = "leaflab-board-metrics-test-a", "leaflab-board-metrics-test-b"
	RecordPushToAck(ctx, deviceA, 750*time.Millisecond)
	RecordPushToAck(ctx, deviceB, 1250*time.Millisecond)

	rm := collect(t)
	h := findHistogram(t, &rm, "leaflab_config_push_to_ack_duration_seconds")

	dpA := findDataPointByAttr(t, h, "device_id", deviceA)
	if dpA.Count != 1 {
		t.Errorf("board A DataPoint.Count = %d, want 1", dpA.Count)
	}
	if dpA.Sum != 0.75 {
		t.Errorf("board A DataPoint.Sum = %v seconds, want 0.75 (750ms recorded)", dpA.Sum)
	}

	dpB := findDataPointByAttr(t, h, "device_id", deviceB)
	if dpB.Count != 1 {
		t.Errorf("board B DataPoint.Count = %d, want 1", dpB.Count)
	}

	// "In aggregate": collecting with no attribute filter (exactly what
	// findHistogram/collect above did) is itself the aggregate view --
	// both boards' data points are present in the same unfiltered
	// histogram, per this package's own doc comment on why one instrument
	// satisfies both halves of the requirement.
	if len(h.DataPoints) < 2 {
		t.Errorf("aggregate (unfiltered) view has only %d data point(s), want at least 2 (both boards represented)", len(h.DataPoints))
	}
}

// TestRecordTierLag_RecordsDurationAttributedByTier proves RecordTierLag's
// histogram carries a data point attributed by tier name, so a specific
// pre-aggregated tier's lag is independently queryable.
func TestRecordTierLag_RecordsDurationAttributedByTier(t *testing.T) {
	ctx := context.Background()

	const tier = "leaflab-metrics-test-5m"
	RecordTierLag(ctx, tier, 90*time.Second)

	rm := collect(t)
	h := findHistogram(t, &rm, "leaflab_reading_tier_lag_seconds")

	dp := findDataPointByAttr(t, h, "tier", tier)
	if dp.Count != 1 {
		t.Errorf("DataPoint.Count = %d, want 1", dp.Count)
	}
	if dp.Sum != 90 {
		t.Errorf("DataPoint.Sum = %v seconds, want 90 (90*time.Second recorded)", dp.Sum)
	}
}
