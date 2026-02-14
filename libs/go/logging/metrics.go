package logging

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

var (
	meterProvider *sdkmetric.MeterProvider
)

// Meter returns a named meter from the global MeterProvider.
// Use this to create instruments (counters, histograms, gauges):
//
//	meter := logging.Meter("mypackage")
//	counter, _ := meter.Int64Counter("requests_total")
//	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("method", "GET")))
func Meter(name string) metric.Meter {
	return otel.Meter(name)
}

// MetricExportInterval controls how often metrics are exported to the
// OTLP collector. Defaults to 60 seconds if not set.
const DefaultMetricExportInterval = 60 * time.Second

// setupMetrics creates an OTLP gRPC metric exporter and registers it as
// the global MeterProvider with periodic export.
func setupMetrics(cfg Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return fmt.Errorf("create OTLP metric exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", cfg.Version),
			attribute.String("deployment.environment", cfg.Environment),
		),
	)
	if err != nil {
		return fmt.Errorf("create metric resource: %w", err)
	}

	interval := DefaultMetricExportInterval
	if cfg.MetricExportInterval > 0 {
		interval = cfg.MetricExportInterval
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(
			sdkmetric.NewPeriodicReader(exporter,
				sdkmetric.WithInterval(interval),
			),
		),
		sdkmetric.WithResource(res),
	)

	otel.SetMeterProvider(mp)
	meterProvider = mp
	return nil
}

// shutdownMetrics flushes pending metrics and shuts down the MeterProvider.
func shutdownMetrics(ctx context.Context) error {
	if meterProvider != nil {
		err := meterProvider.Shutdown(ctx)
		meterProvider = nil
		return err
	}
	return nil
}
