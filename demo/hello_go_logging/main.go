// Demo application showing Go structured logging, tracing, and metrics.
//
// Mirrors the Python hello_logging demo. All service metadata is auto-detected
// from environment variables set by container images and Helm charts.
package main

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/whale-net/everything/libs/go/logging"
)

func main() {
	// Configure logging, tracing, and metrics.
	// In production, set EnableOTLP/EnableTracing/EnableMetrics to true
	// and point OTLPEndpoint at your collector.
	logging.Configure(logging.Config{
		ServiceName:   "hello-go-logging",
		Domain:        "demo",
		Environment:   "development",
		Level:         slog.LevelDebug,
		JSONFormat:    false, // human-readable for local dev
		EnableOTLP:    false, // set true when OTLP collector is available
		EnableTracing: false,
		EnableMetrics: false,
	})
	defer logging.Shutdown(context.Background())

	logger := logging.Get("main")

	// --- Logging ---
	logger.Debug("debug message - detailed diagnostic info")
	logger.Info("info message - general information")
	logger.Warn("warning message - something unexpected")
	logger.Error("error message - something went wrong")

	logger.Info("processing user request",
		"request_id", "req-123-456",
		"user_id", "user-789",
		"operation", "create_order",
	)

	// --- Tracing ---
	demonstrateTracing()

	// --- Metrics ---
	demonstrateMetrics()

	// --- Combined: logging inside a traced, metered request ---
	simulateRequestHandler()
}

func demonstrateTracing() {
	tracer := logging.Tracer("demo")

	// Create a parent span
	ctx, span := tracer.Start(context.Background(), "demo-operation")
	defer span.End()

	logger := logging.Get("tracing")
	// Log with context so trace_id/span_id appear in output
	slog.Default().InfoContext(ctx, "inside traced operation",
		"logger", "tracing",
		"step", "parent",
	)

	// Create a child span
	_, childSpan := tracer.Start(ctx, "child-work")
	time.Sleep(25 * time.Millisecond)
	childSpan.End()

	logger.Info("tracing demo complete")
}

func demonstrateMetrics() {
	meter := logging.Meter("demo")

	// Counter
	requestCounter, _ := meter.Int64Counter("demo_requests_total",
		otelmetric.WithDescription("Total demo requests"),
	)

	// Histogram
	latencyHist, _ := meter.Float64Histogram("demo_request_duration_seconds",
		otelmetric.WithDescription("Request duration in seconds"),
	)

	ctx := context.Background()

	// Record some metrics
	requestCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("method", "GET"),
		attribute.String("path", "/api/demo"),
	))
	requestCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("method", "POST"),
		attribute.String("path", "/api/demo"),
	))

	latencyHist.Record(ctx, 0.042)
	latencyHist.Record(ctx, 0.156)

	logging.Get("metrics").Info("metrics demo complete",
		"counters_recorded", 2,
		"histograms_recorded", 2,
	)
}

func simulateRequestHandler() {
	tracer := logging.Tracer("handler")
	meter := logging.Meter("handler")

	requestCounter, _ := meter.Int64Counter("handler_requests_total")
	latencyHist, _ := meter.Float64Histogram("handler_request_duration_seconds")

	// Start a span for the request
	ctx, span := tracer.Start(context.Background(), "handle-request")
	defer span.End()

	start := time.Now()

	// Create a request-scoped logger
	logger := logging.Get("handler").With(
		"request_id", "req-abc-123",
		"http_method", "POST",
		"http_path", "/api/orders",
		"user_id", "user-42",
	)

	// Log within the span context for trace correlation
	slog.Default().With(
		"logger", "handler",
		"request_id", "req-abc-123",
	).InfoContext(ctx, "received HTTP request")

	time.Sleep(50 * time.Millisecond)

	// Child span for validation
	_, valSpan := tracer.Start(ctx, "validate-payload")
	time.Sleep(25 * time.Millisecond)
	valSpan.End()

	logger.Info("request completed", "http_status", 201)

	// Record metrics
	duration := time.Since(start).Seconds()
	requestCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("method", "POST"),
		attribute.Int("status", 201),
	))
	latencyHist.Record(ctx, duration)
}
