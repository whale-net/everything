# Consolidated Structured Logging

**OTLP-first structured logging** with automatic environment detection and full OpenTelemetry semantic conventions support.

## Primary Use Case: OpenTelemetry (OTLP)

This library is designed **OTLP-first** with **automatic environment detection** - your application metadata is discovered from environment variables (set by Bazel build + Helm charts), so you don't need to hardcode anything in your application code.

## Features

- **Auto-Detection from Environment**: APP_NAME, APP_VERSION, APP_ENV automatically used
- **OTLP Primary Backend**: All logs sent to OpenTelemetry collector with full context
- **OTEL Semantic Conventions**: HTTP, K8s, service attributes follow standards
- **Zero-Config in Apps**: Just call `configure_logging()` - everything else auto-detected
- **Request Context**: User ID, request ID, correlation as log attributes
- **Trace Correlation**: Automatic trace_id/span_id linking
- **Kubernetes Aware**: Auto-detects pod, node, namespace
- **Type-Safe**: Full type hints and dataclass-based context
- **Console Debug**: Optional simple text output for local development

## Environment Variables (Auto-Detected)

Set by `release_app` macro + Helm charts - **you don't need to set these in code**:

### Core Metadata
- `APP_NAME`: Application name (e.g., "hello-fastapi")
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

## Quick Start

### 1. Configure at Startup (Zero-Config - Auto-Detection)

```python
from libs.python.logging import configure_logging

# SIMPLEST: Everything auto-detected from environment
configure_logging()

# OTLP is enabled by default, all metadata auto-detected from:
# - APP_NAME, APP_VERSION, APP_DOMAIN, APP_TYPE (from release_app)
# - APP_ENV (from Helm)
# - POD_NAME, NAMESPACE (from Kubernetes downward API)
```

### 2. Override Only What You Need

```python
from libs.python.logging import configure_logging

# Override specific values, auto-detect the rest
configure_logging(
    service_name="custom-name",  # Override auto-detected APP_NAME
    log_level="DEBUG",
    enable_otlp=False,  # Disable OTLP if needed (worker running externally)
)
```

### 3. Full Manual Control (Legacy Pattern)

```python
from libs.python.logging import configure_logging

# Explicit configuration (old pattern - not needed anymore)
configure_logging(
    service_name="my-app",
    service_version="v1.2.3",
    deployment_environment="production",
    log_level="INFO",
    enable_otlp=True,
    json_format=False,
)
```

```python
import logging

# Standard Python logging works - context is automatic!
logger = logging.getLogger(__name__)

# All logs sent to OTLP with resource + log attributes
logger.info("Server started")

# Add request-specific context as OTLP log attributes
logger.info("Processing request", extra={
    "request_id": "req-123",
    "user_id": "user-456",
})
```

### 3. Or Use Enhanced Context Logger

```python
from libs.python.logging import get_logger, update_context

logger = get_logger(__name__)

# Set context once - automatically added to all logs
update_context(
    request_id="req-abc-123",
    user_id="user-789",
    http_method="POST",
    http_path="/api/orders",
)

# All subsequent logs include this context as OTLP attributes
logger.info("Validating payload")
logger.info("Creating order")
```

## FastAPI Integration

```python
from fastapi import FastAPI, Request
from libs.python.logging import configure_logging, get_logger, update_context
import uuid

app = FastAPI()
logger = get_logger(__name__)

# Configure at startup
@app.on_event("startup")
async def startup():
    configure_logging(
        app_name="my-api",
        domain="api",
        app_type="external-api",
        enable_otlp=True,
    )

# Middleware to set request context
@app.middleware("http")
async def logging_middleware(request: Request, call_next):
    # Set context for this request
    update_context(
        request_id=str(uuid.uuid4()),
        http_method=request.method,
        http_path=request.url.path,
        client_ip=request.client.host,
    )
    
    logger.info("Request received")
    response = await call_next(request)
    
    update_context(http_status_code=response.status_code)
    logger.info("Request completed")
    
    return response

@app.get("/")
async def root():
    # Context is automatically included
    logger.info("Handling root request")
    return {"message": "Hello"}
```

## Worker/Job Integration

```python
from libs.python.logging import configure_logging, get_logger, update_context

logger = get_logger(__name__)

def process_task(task_id: str):
    # Set task context
    update_context(
        task_id=task_id,
        worker_id="worker-1",
        operation="process_batch",
    )
    
    logger.info("Starting task")
    
    # Process items
    for item_id in get_items():
        update_context(resource_id=item_id)
        logger.debug("Processing item")
    
    logger.info("Task completed")

if __name__ == "__main__":
    configure_logging(
        app_name="my-worker",
        domain="background",
        app_type="worker",
    )
    
    process_task("task-123")
```

## Environment Variables

The library auto-detects context from these environment variables:

```bash
# Application metadata
export APP_ENV=production          # or ENVIRONMENT
export APP_NAME=my-app
export APP_DOMAIN=api
export APP_TYPE=external-api
export APP_VERSION=v1.2.3
export GIT_COMMIT=abc123def        # or COMMIT_SHA

# Kubernetes (usually set by downward API)
export POD_NAME=my-app-xyz
export POD_NAMESPACE=production
export NODE_NAME=node-1
export CONTAINER_NAME=main

# Helm
export HELM_CHART_NAME=my-chart
export HELM_RELEASE_NAME=my-release

# OpenTelemetry
export OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4317
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://collector:4317

# Platform
export PLATFORM=linux/arm64
export ARCHITECTURE=arm64
export BAZEL_TARGET=//demo/my-app:my-app
```

## Output Formats

### OTLP Export (PRIMARY - Production Use)

All logs are sent to OpenTelemetry collector as structured OTLP log records:

**Resource Attributes** (set once per service instance):
```json
{
  "service.name": "my-api",
  "service.namespace": "api",
  "service.version": "v1.2.3",
  "deployment.environment": "production",
  "k8s.pod.name": "my-api-xyz",
  "k8s.namespace.name": "production",
  "vcs.commit.id": "abc123def456"
}
```

**Log Record** (per log call):
```json
{
  "timestamp": "2025-10-23T10:30:45.123Z",
  "severity_number": 9,  // INFO
  "severity_text": "INFO",
  "body": "Processing request",
  "attributes": {
    "request.id": "req-abc-123",
    "enduser.id": "user-789",
    "http.request.method": "POST",
    "http.route": "/api/orders",
    "http.response.status_code": 201,
    "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
    "span_id": "00f067aa0ba902b7"
  }
}
```

### Console Output (DEBUG - Development Only)

Simple text format for local debugging:
```
2025-10-23 10:30:45 - [my-api] main - INFO - Processing request
```

Or JSON if `json_format=True`:
```json
{
  "timestamp": "2025-10-23 10:30:45",
  "severity": "INFO",
  "message": "Processing request",
  "app_name": "my-api",
  "request_id": "req-abc-123"
}
```

## Architecture: OTLP-First Design

```
┌─────────────────────────────────────────────────────────┐
│ Your App: logger.info("message", extra={...})          │
└─────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────┐
│ Python logging.Logger                                    │
│ + LogContext (thread-safe contextvars)                  │
└─────────────────────────────────────────────────────────┘
                          │
                          ├──────────────────┬────────────┐
                          ▼                  ▼            ▼
    ┌─────────────────────────────┐ ┌──────────────┐ ┌──────────┐
    │ OTELContextHandler          │ │ Console      │ │ (others) │
    │ (PRIMARY)                   │ │ (debug only) │ │          │
    └─────────────────────────────┘ └──────────────┘ └──────────┘
                │                           │
                ▼                           ▼
    ┌─────────────────────────────┐ ┌──────────────┐
    │ OTLP SDK                    │ │ Simple Text  │
    │ - Maps to OTEL semantics    │ │ Formatter    │
    │ - Resource attributes       │ └──────────────┘
    │ - Log record attributes     │
    │ - Batch processing          │
    └─────────────────────────────┘
                │
                ▼
    ┌─────────────────────────────────────┐
    │ OTLP Collector (gRPC)               │
    │ ↓                                   │
    │ Grafana Loki (logs)                 │
    │ Grafana Tempo (traces)              │
    │ Prometheus (metrics)                │
    │                                     │
    │ Automatic correlation via           │
    │ trace_id/span_id                    │
    └─────────────────────────────────────┘
```

### Why OTLP-First?

1. **Structured Observability**: Logs correlated with traces and metrics
2. **Semantic Conventions**: Industry-standard attribute names
3. **Efficient Transport**: Batched gRPC with protobuf encoding
4. **Vendor Neutral**: Works with any OTLP-compatible backend
5. **Rich Context**: All attributes preserved, not flattened to text

### Custom Context Attributes

```python
from libs.python.logging import update_context

# Add custom attributes
update_context(
    tenant_id="tenant-123",
    organization_id="org-456",
    custom={
        "feature_flag": "new_checkout",
        "experiment_id": "exp-789",
    }
)
```

### Temporary Context

```python
from libs.python.logging import get_logger
from libs.python.logging.context import LogContext, set_context, clear_context

logger = get_logger(__name__)

# Save current context
old_context = get_context()

# Set temporary context
temp_context = LogContext(request_id="temp-123")
set_context(temp_context)

logger.info("Using temporary context")

# Restore
set_context(old_context)
```

### Error Logging with Context

```python
logger = get_logger(__name__)

try:
    result = process_payment()
except PaymentError as e:
    logger.exception(
        "Payment processing failed",
        extra={
            "error_code": "PAYMENT_DECLINED",
            "payment_id": "pay-123",
            "amount": 99.99,
        }
    )
```

## Demo Application

Run the demo to see all features:

```bash
# Run demo
bazel run //demo/hello_logging:hello_logging

# Run tests
bazel test //demo/hello_logging:test_main
```

## Migration from Old Logging

### Before (manman/src/logging_config.py)

```python
from manman.src.logging_config import setup_logging

setup_logging(
    level=logging.INFO,
    microservice_name="my-service",
    enable_otel=True,
)

logger = logging.getLogger(__name__)
logger.info("Message")
```

### After (libs.python.logging)

```python
from libs.python.logging import configure_logging, get_logger

configure_logging(
    app_name="my-service",
    domain="api",
    log_level="INFO",
    enable_otlp=True,
)

logger = get_logger(__name__)
logger.info("Message")
```

## Architecture

The library consists of:

- **`context.py`**: Thread-safe context storage with all standard attributes
- **`config.py`**: Configuration and setup
- **`formatters.py`**: JSON and colored console formatters
- **`factory.py`**: Logger factory with automatic context injection

## Dependencies

```python
deps = [
    "//libs/python/logging",
]
```

Required packages (already in pyproject.toml):
- `opentelemetry-api`
- `opentelemetry-sdk`
- `opentelemetry-exporter-otlp`
