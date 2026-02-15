package logging

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
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
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return fmt.Errorf("create OTLP trace exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.Version),
			attribute.String("deployment.environment", cfg.Environment),
		),
	)
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

// shutdownTracing flushes pending spans and shuts down the TracerProvider.
func shutdownTracing(ctx context.Context) error {
	if tracerProvider != nil {
		err := tracerProvider.Shutdown(ctx)
		tracerProvider = nil
		return err
	}
	return nil
}
