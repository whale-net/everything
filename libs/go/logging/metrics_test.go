package logging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMeter_ReturnsNamedMeter(t *testing.T) {
	meter := Meter("test-component")
	assert.NotNil(t, meter)
}

func TestMeter_Int64Counter(t *testing.T) {
	// Use an in-memory reader to verify metrics are recorded.
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	counter, err := meter.Int64Counter("test_requests_total")
	require.NoError(t, err)

	ctx := context.Background()
	counter.Add(ctx, 1, otelmetric.WithAttributes(attribute.String("method", "GET")))
	counter.Add(ctx, 3, otelmetric.WithAttributes(attribute.String("method", "POST")))

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	// Verify we got metric data
	require.NotEmpty(t, rm.ScopeMetrics)
	require.NotEmpty(t, rm.ScopeMetrics[0].Metrics)

	m := rm.ScopeMetrics[0].Metrics[0]
	assert.Equal(t, "test_requests_total", m.Name)

	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64] data type")
	assert.Len(t, sum.DataPoints, 2)
}

func TestMeter_Float64Histogram(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer mp.Shutdown(context.Background())

	meter := mp.Meter("test")
	hist, err := meter.Float64Histogram("request_duration_seconds")
	require.NoError(t, err)

	ctx := context.Background()
	hist.Record(ctx, 0.15)
	hist.Record(ctx, 0.42)
	hist.Record(ctx, 1.23)

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(ctx, &rm))

	require.NotEmpty(t, rm.ScopeMetrics)
	require.NotEmpty(t, rm.ScopeMetrics[0].Metrics)

	m := rm.ScopeMetrics[0].Metrics[0]
	assert.Equal(t, "request_duration_seconds", m.Name)

	h, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64] data type")
	require.Len(t, h.DataPoints, 1)
	assert.Equal(t, uint64(3), h.DataPoints[0].Count)
}
