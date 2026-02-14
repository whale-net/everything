package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/whale-net/everything/libs/go/logging"
)

func TestLoggingIntegration(t *testing.T) {
	var buf bytes.Buffer
	logging.Configure(logging.Config{
		ServiceName: "hello-go-logging",
		Domain:      "demo",
		Environment: "test",
		JSONFormat:  true,
		Writer:      &buf,
	})
	buf.Reset()

	logger := logging.Get("test")
	logger.Info("integration test", "key", "value")

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("expected valid JSON: %v", err)
	}
	if m["message"] != "integration test" {
		t.Errorf("expected message 'integration test', got %v", m["message"])
	}
	if m["app_name"] != "hello-go-logging" {
		t.Errorf("expected app_name 'hello-go-logging', got %v", m["app_name"])
	}
}

func TestTracingIntegration(t *testing.T) {
	var buf bytes.Buffer
	logging.Configure(logging.Config{
		ServiceName: "hello-go-logging",
		JSONFormat:  true,
		Writer:      &buf,
	})
	buf.Reset()

	// Set up in-memory tracing
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	tracer := logging.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "test-op")

	slog.Default().InfoContext(ctx, "traced log")
	span.End()

	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("expected valid JSON: %v", err)
	}
	if _, ok := m["trace_id"]; !ok {
		t.Error("expected trace_id in output")
	}
	if _, ok := m["span_id"]; !ok {
		t.Error("expected span_id in output")
	}

	// Verify span was exported
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Errorf("expected 1 span, got %d", len(spans))
	}
}

func TestMetricsIntegration(t *testing.T) {
	meter := logging.Meter("test")
	counter, err := meter.Int64Counter("test_counter")
	if err != nil {
		t.Fatalf("failed to create counter: %v", err)
	}
	// Should not panic
	counter.Add(context.Background(), 1)
}

func TestLoggingLevels(t *testing.T) {
	var buf bytes.Buffer
	logging.Configure(logging.Config{
		ServiceName: "hello-go-logging",
		Level:       slog.LevelDebug,
		JSONFormat:  false,
		Writer:      &buf,
	})
	buf.Reset()

	logger := logging.Get("levels")
	logger.Debug("debug msg")
	logger.Info("info msg")
	logger.Warn("warn msg")
	logger.Error("error msg")

	output := buf.String()
	for _, expected := range []string{"debug msg", "info msg", "warn msg", "error msg"} {
		if !bytes.Contains([]byte(output), []byte(expected)) {
			t.Errorf("expected output to contain %q", expected)
		}
	}
}
