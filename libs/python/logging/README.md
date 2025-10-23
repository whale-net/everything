# Consolidated Structured Logging

A standardized logging library with rich context and OpenTelemetry integration.

## Features

- **Automatic Context Injection**: Environment, domain, app name, app type, and more
- **OpenTelemetry Integration**: Automatic trace/span correlation
- **Kubernetes Aware**: Auto-detects pod, node, namespace from environment
- **Request Correlation**: Track requests across services with correlation IDs
- **Flexible Formatting**: JSON for production, colored text for development
- **Type-Safe**: Full type hints and dataclass-based context

## Standard Attributes

Every log automatically includes:

### Application Metadata
- `environment` - dev, staging, prod
- `domain` - api, web, worker, etc.
- `app_name` - hello-fastapi, manman-worker
- `app_type` - external-api, internal-api, worker, job
- `version` - v1.2.3 or commit SHA
- `commit_sha` - Full git commit

### Kubernetes Context
- `pod_name` - K8s pod name
- `container_name` - Container name
- `node_name` - K8s node name
- `namespace` - K8s namespace

### Request/Operation Context
- `request_id` - Request identifier
- `correlation_id` - Cross-service correlation
- `user_id` - User identifier
- `session_id` - Session identifier
- `operation` - Operation being performed
- `resource_id` - Resource being operated on

### HTTP Context (for APIs)
- `http_method` - GET, POST, etc.
- `http_path` - Request path
- `http_status_code` - Response status
- `client_ip` - Client IP address
- `user_agent` - Client user agent

### Worker Context
- `worker_id` - Worker identifier
- `task_id` - Task identifier
- `job_id` - Job identifier

### OpenTelemetry
- `trace_id` - Trace identifier
- `span_id` - Span identifier
- `trace_flags` - Trace flags

### Source Location
- `module` - Python module
- `function` - Function name
- `line` - Line number
- `file` - File path

### Platform
- `platform` - linux/amd64, linux/arm64
- `architecture` - amd64, arm64
- `bazel_target` - Bazel build target

## Quick Start

### 1. Configure at Startup

```python
from libs.python.logging import configure_logging

# Configure once in your app's main entry point
configure_logging(
    app_name="my-app",
    domain="api",
    app_type="external-api",
    environment="production",
    version="v1.2.3",
    log_level="INFO",
    enable_otlp=True,  # Enable OpenTelemetry export
    json_format=True,  # JSON for production
)
```

### 2. Get Logger and Use

```python
from libs.python.logging import get_logger

logger = get_logger(__name__)

# Basic logging - context is automatic
logger.info("Server started")

# Add request-specific context
logger.info("Processing request", extra={
    "request_id": "req-123",
    "user_id": "user-456",
})
```

### 3. Update Context Per-Request

```python
from libs.python.logging import update_context, get_logger

logger = get_logger(__name__)

# Set context for this request (thread-safe)
update_context(
    request_id="req-abc-123",
    user_id="user-789",
    http_method="POST",
    http_path="/api/orders",
)

# All subsequent logs include this context automatically
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

### JSON (Production)

```json
{
  "timestamp": "2025-10-23T10:30:45.123Z",
  "severity": "INFO",
  "severity_number": 20,
  "message": "Processing request",
  "environment": "production",
  "domain": "api",
  "app_name": "my-api",
  "app_type": "external-api",
  "version": "v1.2.3",
  "request_id": "req-abc-123",
  "user_id": "user-789",
  "http_method": "POST",
  "http_path": "/api/orders",
  "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
  "span_id": "00f067aa0ba902b7",
  "source": {
    "module": "main",
    "function": "create_order",
    "line": 45,
    "file": "/app/main.py"
  }
}
```

### Colored Console (Development)

```
2025-10-23 10:30:45 - [my-api | production | req=abc123] INFO - main.create_order:45 - Processing request
```

## Advanced Usage

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
