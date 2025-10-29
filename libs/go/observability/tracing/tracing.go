// Package tracing provides distributed tracing with OpenTelemetry.
// It supports console and OTLP export for traces.
package tracing

import (
	"context"
	"fmt"
	"os"

	obscontext "github.com/whale-net/everything/libs/go/observability/context"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

// Config holds tracing configuration
type Config struct {
	// Service identification (auto-detected from environment if not provided)
	ServiceName    string
	ServiceVersion string
	Environment    string
	
	// OTLP export
	EnableOTLP   bool
	OTLPEndpoint string
	
	// Sampling
	SampleRate float64 // 0.0 to 1.0, where 1.0 = sample everything
}

// DefaultConfig returns a Config with defaults and auto-detection from environment
func DefaultConfig() *Config {
	obsCtx := obscontext.FromEnvironment()
	
	otlpEndpoint := getEnv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"))
	
	return &Config{
		ServiceName:    obsCtx.AppName,
		ServiceVersion: obsCtx.Version,
		Environment:    obsCtx.Environment,
		EnableOTLP:     true,
		OTLPEndpoint:   otlpEndpoint,
		SampleRate:     1.0, // Sample everything by default
	}
}

var (
	tracerProvider *sdktrace.TracerProvider
)

// Configure sets up tracing based on the provided configuration.
// It should be called once at application startup.
func Configure(cfg *Config) (trace.TracerProvider, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	
	// Get or create observability context
	obsCtx := obscontext.GetGlobalContext()
	if obsCtx.AppName == "" {
		obsCtx = obscontext.FromEnvironment()
		obscontext.SetGlobalContext(obsCtx)
	}
	
	// Override with config values if provided
	if cfg.ServiceName != "" {
		obsCtx.AppName = cfg.ServiceName
	}
	if cfg.ServiceVersion != "" {
		obsCtx.Version = cfg.ServiceVersion
	}
	if cfg.Environment != "" {
		obsCtx.Environment = cfg.Environment
	}
	
	// Create resource with OpenTelemetry semantic conventions
	resourceAttrs := []resource.Option{
		resource.WithAttributes(
			semconv.ServiceName(obsCtx.AppName),
			semconv.ServiceVersion(obsCtx.Version),
			semconv.DeploymentEnvironment(obsCtx.Environment),
		),
	}
	
	// Add domain/namespace if available
	if obsCtx.Domain != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.ServiceNamespace(obsCtx.Domain)))
	}
	
	// Add Kubernetes attributes
	if obsCtx.PodName != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SPodName(obsCtx.PodName)))
	}
	if obsCtx.Namespace != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SNamespaceName(obsCtx.Namespace)))
	}
	if obsCtx.NodeName != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SNodeName(obsCtx.NodeName)))
	}
	if obsCtx.ContainerName != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.K8SContainerName(obsCtx.ContainerName)))
	}
	
	// Add commit SHA
	if obsCtx.CommitSha != "" {
		resourceAttrs = append(resourceAttrs,
			resource.WithAttributes(semconv.ServiceInstanceID(obsCtx.CommitSha[:8])))
	}
	
	res, err := resource.New(context.Background(), resourceAttrs...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}
	
	var spanProcessors []sdktrace.SpanProcessor
	
	// Setup OTLP exporter if enabled
	if cfg.EnableOTLP {
		exporter, err := otlptracegrpc.New(
			context.Background(),
			otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}
		
		spanProcessors = append(spanProcessors, sdktrace.NewBatchSpanProcessor(exporter))
	}
	
	// Create tracer provider with sampling
	var sampler sdktrace.Sampler
	if cfg.SampleRate >= 1.0 {
		sampler = sdktrace.AlwaysSample()
	} else if cfg.SampleRate <= 0.0 {
		sampler = sdktrace.NeverSample()
	} else {
		sampler = sdktrace.TraceIDRatioBased(cfg.SampleRate)
	}
	
	tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	
	// Add span processors
	for _, processor := range spanProcessors {
		tracerProvider.RegisterSpanProcessor(processor)
	}
	
	// Set as global tracer provider
	otel.SetTracerProvider(tracerProvider)
	
	// Set propagators for distributed tracing
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	
	return tracerProvider, nil
}

// Tracer returns a tracer for the given name
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// StartSpan starts a new span with the given name and context
func StartSpan(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	tracer := otel.Tracer("github.com/whale-net/everything")
	return tracer.Start(ctx, spanName, opts...)
}

// StartSpanWithContext starts a new span and enriches it with observability context
func StartSpanWithContext(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	ctx, span := StartSpan(ctx, spanName, opts...)
	
	// Add observability context as span attributes
	obsCtx := obscontext.FromContext(ctx)
	if obsCtx != nil {
		if obsCtx.RequestID != "" {
			span.SetAttributes(semconv.HTTPRequestIDKey.String(obsCtx.RequestID))
		}
		if obsCtx.UserID != "" {
			span.SetAttributes(semconv.EnduserID(obsCtx.UserID))
		}
		if obsCtx.HTTPMethod != "" {
			span.SetAttributes(semconv.HTTPRequestMethodKey.String(obsCtx.HTTPMethod))
		}
		if obsCtx.HTTPPath != "" {
			span.SetAttributes(semconv.HTTPRoute(obsCtx.HTTPPath))
		}
		if obsCtx.ClientIP != "" {
			span.SetAttributes(semconv.ClientAddress(obsCtx.ClientIP))
		}
	}
	
	return ctx, span
}

// Shutdown flushes any buffered spans and shuts down the tracer provider
func Shutdown(ctx context.Context) error {
	if tracerProvider != nil {
		return tracerProvider.Shutdown(ctx)
	}
	return nil
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
