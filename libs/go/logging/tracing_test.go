package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracer_ReturnsNamedTracer(t *testing.T) {
	tracer := Tracer("test-component")
	assert.NotNil(t, tracer)
}

func TestJSONHandler_IncludesTraceContext(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "trace-app",
		JSONFormat:  true,
	})

	// Set up an in-memory span exporter so we get real trace/span IDs.
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-op")
	defer span.End()

	// Log within the span context — slog needs the context to pick up the span.
	slog.Default().InfoContext(ctx, "inside span", "key", "val")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.Equal(t, "inside span", m["message"])
	assert.Contains(t, m, "trace_id", "JSON output should contain trace_id")
	assert.Contains(t, m, "span_id", "JSON output should contain span_id")
	assert.Contains(t, m, "trace_flags")

	// Verify they are non-zero hex strings
	assert.Len(t, m["trace_id"], 32, "trace_id should be 32 hex chars")
	assert.Len(t, m["span_id"], 16, "span_id should be 16 hex chars")
}

func TestJSONHandler_NoTraceContextWithoutSpan(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "no-trace-app",
		JSONFormat:  true,
	})

	slog.Info("no span")

	var m map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	assert.NotContains(t, m, "trace_id", "should not have trace_id without active span")
	assert.NotContains(t, m, "span_id")
}

func TestConsoleHandler_IncludesTraceContext(t *testing.T) {
	buf := configureFresh(t, Config{
		ServiceName: "trace-console",
		JSONFormat:  false,
	})

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "console-op")
	defer span.End()

	slog.Default().InfoContext(ctx, "traced console log")

	output := buf.String()
	assert.Contains(t, output, "trace_id=")
	assert.Contains(t, output, "span_id=")
}

func TestConsoleHandler_NoTraceContextWithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	mu.Lock()
	configured = false
	mu.Unlock()
	Configure(Config{
		ServiceName: "no-trace-console",
		JSONFormat:  false,
		Writer:      &buf,
	})
	buf.Reset()

	slog.Info("plain log")

	output := buf.String()
	assert.NotContains(t, output, "trace_id=")
	assert.NotContains(t, output, "span_id=")
}
