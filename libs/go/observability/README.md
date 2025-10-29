# Go Observability Library

**Console + OTLP-based logging and tracing** for Go applications with automatic environment detection and full OpenTelemetry semantic conventions support.

## Features

- **Auto-Detection from Environment**: APP_NAME, APP_VERSION, APP_ENV automatically used
- **OTLP Primary Backend**: Logs and traces sent to OpenTelemetry collector with full context
- **OTEL Semantic Conventions**: HTTP, K8s, service attributes follow standards
- **Zero-Config in Apps**: Just call `observability.ConfigureAll()` - everything else auto-detected
- **Context Management**: Thread-safe context storage for request/operation data
- **Trace Correlation**: Automatic trace_id/span_id linking between logs and traces
- **Kubernetes Aware**: Auto-detects pod, node, namespace
- **Type-Safe**: Full type hints and struct-based context
- **Console Debug**: Optional simple text output for local development

## Quick Start

### 1. Add Dependency

In your `BUILD.bazel`:

```starlark
go_binary(
    name = "my_app",
    srcs = ["main.go"],
    deps = [
        "//libs/go/observability",
    ],
)
```

### 2. Configure at Startup (Zero-Config - Auto-Detection)

```go
package main

import (
    "context"
    "log"
    
    "github.com/whale-net/everything/libs/go/observability"
)

func main() {
    // SIMPLEST: Everything auto-detected from environment
    if err := observability.ConfigureAll(); err != nil {
        log.Fatalf("Failed to configure observability: %v", err)
    }
    defer observability.ShutdownAll(context.Background())
    
    // OTLP is enabled by default, all metadata auto-detected from:
    // - APP_NAME, APP_VERSION, APP_DOMAIN, APP_TYPE (from release_app)
    // - APP_ENV (from Helm)
    // - POD_NAME, NAMESPACE (from Kubernetes downward API)
    
    logger := observability.DefaultLogger()
    logger.Info("Application started")
}
```

### 3. Use Logging with Context

```go
import (
    "context"
    
    "github.com/whale-net/everything/libs/go/observability"
)

func handleRequest(ctx context.Context) {
    logger := observability.DefaultLogger()
    
    // Create observability context
    obsCtx := observability.NewContext()
    obsCtx.RequestID = "req-123"
    obsCtx.UserID = "user-456"
    obsCtx.HTTPMethod = "POST"
    obsCtx.HTTPPath = "/api/orders"
    
    // Add to context
    ctx = observability.WithContext(ctx, obsCtx)
    
    // All logs include context automatically
    logger.InfoContext(ctx, "Processing request")
    logger.InfoContext(ctx, "Order created", "order_id", "ord-789")
}
```

### 4. Use Distributed Tracing

```go
import (
    "context"
    
    "github.com/whale-net/everything/libs/go/observability"
)

func processOrder(ctx context.Context, orderID string) error {
    // Start a span
    ctx, span := observability.StartSpanWithContext(ctx, "process-order")
    defer span.End()
    
    logger := observability.DefaultLogger()
    
    // Logs are automatically correlated with trace
    logger.InfoContext(ctx, "Processing order", "order_id", orderID)
    
    // Do work...
    
    return nil
}
```

## Environment Variables (Auto-Detected)

Set by `release_app` macro + Helm charts - **you don't need to set these in code**:

### Core Metadata
- `APP_NAME`: Application name (e.g., "hello-go")
- `APP_VERSION`: Application version (e.g., "v1.2.3")
- `APP_DOMAIN`: Application domain (e.g., "demo", "api")
- `APP_TYPE`: Application type (external-api, internal-api, worker, job)
- `APP_ENV` / `ENVIRONMENT`: Environment (dev, staging, prod)
- `GIT_COMMIT` / `COMMIT_SHA`: Git commit SHA

### Kubernetes Context (from Downward API)
- `POD_NAME`: Kubernetes pod name
- `NAMESPACE` / `POD_NAMESPACE`: Kubernetes namespace
- `NODE_NAME`: Kubernetes node name
- `CONTAINER_NAME`: Container name

### Helm Context
- `HELM_CHART_NAME`: Helm chart name
- `HELM_RELEASE_NAME`: Helm release name

### OpenTelemetry
- `OTEL_EXPORTER_OTLP_ENDPOINT`: OTLP collector endpoint (default: localhost:4317)
- `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`: Override for logs endpoint
- `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`: Override for traces endpoint

## Configuration Options

### Manual Configuration

If you need to override auto-detected values:

```go
// Logging configuration
logConfig := &observability.LogConfig{
    ServiceName:    "custom-name",  // Override auto-detected APP_NAME
    ServiceVersion: "v2.0.0",
    Environment:    "production",
    Level:          slog.LevelDebug,
    EnableConsole:  true,
    JSONFormat:     false,  // Simple text for development
    EnableOTLP:     true,   // OTLP-first
    OTLPEndpoint:   "collector:4317",
}

logger, err := observability.ConfigureLogging(logConfig)
if err != nil {
    log.Fatalf("Failed to configure logging: %v", err)
}

// Tracing configuration
traceConfig := &observability.TraceConfig{
    ServiceName:    "custom-name",
    ServiceVersion: "v2.0.0",
    Environment:    "production",
    EnableOTLP:     true,
    OTLPEndpoint:   "collector:4317",
    SampleRate:     1.0,  // Sample everything
}

provider, err := observability.ConfigureTracing(traceConfig)
if err != nil {
    log.Fatalf("Failed to configure tracing: %v", err)
}
```

## Package Structure

The observability library consists of three main packages:

### `context`
Thread-safe context storage for observability data:
- Application metadata (name, version, environment)
- Infrastructure metadata (Kubernetes, Helm)
- Request/operation context (request ID, user ID, etc.)
- Custom attributes

### `logging`
Structured logging with slog:
- Console output (text or JSON)
- OTLP export to OpenTelemetry collector
- Automatic context injection
- Multiple log levels (Debug, Info, Warn, Error)

### `tracing`
Distributed tracing with OpenTelemetry:
- OTLP export to OpenTelemetry collector
- Span creation and management
- Trace context propagation
- Configurable sampling rates

## Usage Patterns

### HTTP Server Example

```go
package main

import (
    "context"
    "log"
    "net/http"
    
    "github.com/whale-net/everything/libs/go/observability"
)

func main() {
    // Configure observability
    if err := observability.ConfigureAll(); err != nil {
        log.Fatalf("Failed to configure observability: %v", err)
    }
    defer observability.ShutdownAll(context.Background())
    
    logger := observability.DefaultLogger()
    
    http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        // Create request context
        ctx, span := observability.StartSpanWithContext(r.Context(), "handle-request")
        defer span.End()
        
        obsCtx := observability.NewContext()
        obsCtx.RequestID = r.Header.Get("X-Request-ID")
        obsCtx.HTTPMethod = r.Method
        obsCtx.HTTPPath = r.URL.Path
        obsCtx.ClientIP = r.RemoteAddr
        obsCtx.UserAgent = r.UserAgent()
        
        ctx = observability.WithContext(ctx, obsCtx)
        
        logger.InfoContext(ctx, "Request received")
        
        w.Write([]byte("Hello, World!"))
        
        obsCtx.HTTPStatusCode = http.StatusOK
        logger.InfoContext(ctx, "Request completed")
    })
    
    logger.Info("Server starting", "port", 8080)
    if err := http.ListenAndServe(":8080", nil); err != nil {
        logger.ErrorContext(context.Background(), "Server failed", "error", err)
    }
}
```

### Worker Example

```go
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/whale-net/everything/libs/go/observability"
)

func main() {
    // Configure observability
    if err := observability.ConfigureAll(); err != nil {
        log.Fatalf("Failed to configure observability: %v", err)
    }
    defer observability.ShutdownAll(context.Background())
    
    logger := observability.DefaultLogger()
    logger.Info("Worker started")
    
    // Process tasks
    for i := 0; i < 10; i++ {
        processTask(context.Background(), i)
        time.Sleep(time.Second)
    }
}

func processTask(ctx context.Context, taskNum int) {
    ctx, span := observability.StartSpanWithContext(ctx, "process-task")
    defer span.End()
    
    logger := observability.DefaultLogger()
    
    obsCtx := observability.NewContext()
    obsCtx.TaskID = string(rune(taskNum))
    obsCtx.Operation = "process-batch"
    
    ctx = observability.WithContext(ctx, obsCtx)
    
    logger.InfoContext(ctx, "Task started")
    
    // Do work...
    time.Sleep(100 * time.Millisecond)
    
    logger.InfoContext(ctx, "Task completed")
}
```

### Custom Attributes

```go
obsCtx := observability.NewContext()
obsCtx.RequestID = "req-123"

// Add custom attributes
obsCtx.Custom["tenant_id"] = "tenant-456"
obsCtx.Custom["feature_flag"] = "new_ui"
obsCtx.Custom["experiment_id"] = "exp-789"

ctx = observability.WithContext(ctx, obsCtx)

// Custom attributes are automatically included in logs and traces
logger.InfoContext(ctx, "Processing with custom context")
```

## OpenTelemetry Semantic Conventions

The library automatically follows OpenTelemetry semantic conventions:

### Resource Attributes (set once per service instance)
```json
{
  "service.name": "my-app",
  "service.namespace": "api",
  "service.version": "v1.2.3",
  "deployment.environment": "production",
  "k8s.pod.name": "my-app-xyz",
  "k8s.namespace.name": "production"
}
```

### Log/Span Attributes (per operation)
```json
{
  "request.id": "req-abc-123",
  "enduser.id": "user-789",
  "http.request.method": "POST",
  "http.route": "/api/orders",
  "http.response.status_code": 201,
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7"
}
```

## Testing

```bash
# Test all packages
bazel test //libs/go/observability/...

# Test specific package
bazel test //libs/go/observability/context:context_test
bazel test //libs/go/observability/logging:logging_test
bazel test //libs/go/observability/tracing:tracing_test
```

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│ Your App: logger.InfoContext(ctx, "message")           │
│           StartSpanWithContext(ctx, "operation")        │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ Observability Library                                   │
│ + ObservabilityContext (thread-safe context storage)    │
│ + Logging (slog + OTLP handler)                        │
│ + Tracing (OpenTelemetry SDK)                          │
└─────────────────────────────────────────────────────────┘
                          │
                          ├──────────────────┬────────────┐
                          ▼                  ▼            ▼
    ┌─────────────────────────────┐ ┌──────────────┐ ┌──────────┐
    │ OTLP Exporter               │ │ Console      │ │          │
    │ (PRIMARY)                   │ │ (debug only) │ │          │
    └─────────────────────────────┘ └──────────────┘ └──────────┘
                │                           │
                ▼                           ▼
    ┌─────────────────────────────┐ ┌──────────────┐
    │ OTLP Collector (gRPC)       │ │ stdout/      │
    │ ↓                           │ │ stderr       │
    │ Grafana Loki (logs)         │ └──────────────┘
    │ Grafana Tempo (traces)      │
    │                             │
    │ Automatic correlation via   │
    │ trace_id/span_id            │
    └─────────────────────────────┘
```

## Migration from Standard Library

### Before (standard log)
```go
import "log"

log.Println("Processing request")
```

### After (observability)
```go
import "github.com/whale-net/everything/libs/go/observability"

logger := observability.DefaultLogger()
logger.Info("Processing request")
```

### With Context
```go
import "github.com/whale-net/everything/libs/go/observability"

obsCtx := observability.NewContext()
obsCtx.RequestID = "req-123"
ctx = observability.WithContext(ctx, obsCtx)

logger := observability.DefaultLogger()
logger.InfoContext(ctx, "Processing request")
```

## Dependencies

The library uses:
- `go.opentelemetry.io/otel` - OpenTelemetry API
- `go.opentelemetry.io/otel/sdk` - OpenTelemetry SDK
- `go.opentelemetry.io/otel/exporters/otlp` - OTLP exporters
- `log/slog` - Structured logging (Go 1.21+)

All dependencies are managed in `MODULE.bazel`.

## License

See repository LICENSE file.
