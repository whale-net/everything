package logging

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var (
	tracerProvider *sdktrace.TracerProvider
)

// Tracer returns a named tracer from the global TracerProvider.
// Use this to create spans in your application code:
//
//	tracer := logging.Tracer("mypackage")
//	ctx, span := tracer.Start(ctx, "operation-name")
//	defer span.End()
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// setupTracing creates an OTLP gRPC trace exporter and registers it as the
// global TracerProvider. Also sets up W3C trace context propagation.
func setupTracing(cfg Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(stripScheme(cfg.OTLPEndpoint)),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return fmt.Errorf("create OTLP trace exporter: %w", err)
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create trace resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	tracerProvider = tp
	return nil
}

// WrapDefaultHTTPTransport wraps http.DefaultTransport with otelhttp so any
// code that issues requests via http.DefaultClient (http.Get/http.Post, or a
// *http.Client left with a nil Transport) gets tracing spans and outbound
// W3C trace-context propagation without needing its own otelhttp-wrapped
// client. It's a no-op span-wise unless the process has configured tracing.
//
// Call this once at startup, after Configure, in services whose outbound
// HTTP calls go through http.DefaultClient rather than an explicitly
// constructed *http.Client (which should wrap its own Transport instead --
// see manmanv2/api/steam.NewSteamWorkshopClient for that pattern).
func WrapDefaultHTTPTransport() {
	http.DefaultTransport = otelhttp.NewTransport(http.DefaultTransport)
}

// shutdownTracing flushes pending spans and shuts down the TracerProvider.
func shutdownTracing(ctx context.Context) error {
	if tracerProvider != nil {
		err := tracerProvider.Shutdown(ctx)
		tracerProvider = nil
		return err
	}
	return nil
}
