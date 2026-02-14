# Go Structured Logging, Tracing & Metrics

Observability library for Go apps in the everything monorepo. Uses `log/slog` (stdlib) for logging and OpenTelemetry for tracing and metrics. Matches the JSON format of `libs/python/logging`.

## Usage

```go
import "github.com/whale-net/everything/libs/go/logging"

// At startup — configure all three signals
logging.Configure(logging.Config{
    ServiceName:   "my-app",
    Domain:        "api",
    Environment:   "production",
    JSONFormat:    true,
    EnableOTLP:    true,   // structured log export
    EnableTracing: true,   // distributed tracing
    EnableMetrics: true,   // metrics collection
})
defer logging.Shutdown(context.Background())
```

### Logging

```go
logger := logging.Get("mypackage")
logger.Info("handling request", "request_id", "abc-123", "user_id", "u-42")

// Log within a span context for automatic trace_id/span_id injection
slog.Default().InfoContext(ctx, "inside span", "key", "val")
```

### Tracing

```go
tracer := logging.Tracer("mypackage")
ctx, span := tracer.Start(ctx, "handle-request")
defer span.End()

// Child spans
ctx, child := tracer.Start(ctx, "validate-payload")
// ... work ...
child.End()
```

### Metrics

```go
meter := logging.Meter("mypackage")

// Counter
counter, _ := meter.Int64Counter("requests_total")
counter.Add(ctx, 1, metric.WithAttributes(attribute.String("method", "GET")))

// Histogram
hist, _ := meter.Float64Histogram("request_duration_seconds")
hist.Record(ctx, 0.042)
```

## Environment Auto-Detection

When `Config` fields are empty, they are read from environment variables (same as the Python lib):

| Env Var | Config Field |
|---------|-------------|
| `APP_NAME` | `ServiceName` |
| `APP_DOMAIN` | `Domain` |
| `APP_TYPE` | `AppType` |
| `APP_VERSION` | `Version` |
| `APP_ENV` / `ENVIRONMENT` | `Environment` |
| `GIT_COMMIT` / `COMMIT_SHA` | `CommitSHA` |
| `POD_NAME` / `HOSTNAME` | `PodName` |
| `NAMESPACE` / `POD_NAMESPACE` | `Namespace` |
| `NODE_NAME` | `NodeName` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `OTLPEndpoint` |

## JSON Output Format

Matches the Python `StructuredFormatter`. When a span is active, `trace_id` and `span_id` are injected automatically:

```json
{
  "timestamp": "2025-01-15T10:30:00.000Z",
  "severity": "INFO",
  "message": "handling request",
  "app_name": "my-app",
  "domain": "api",
  "environment": "production",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7",
  "source": {"function": "main.handler", "file": "main.go", "line": 42},
  "request_id": "abc-123"
}
```

## Console Output Format

```
2025-01-15T10:30:00Z - [my-app] INFO - handling request request_id=abc-123 trace_id=4bf92f... span_id=00f067...
```

## Bazel Dependency

```starlark
deps = ["//libs/go/logging"]
```

## Demo

```bash
bazel run //demo/hello_go_logging:hello-go-logging
bazel test //demo/hello_go_logging:main_test
```
