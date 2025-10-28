# Gunicorn Configuration Library

Production-ready Gunicorn configuration utilities for FastAPI applications with integrated OTLP logging.

## Features

- **Production Defaults**: Worker management, request limits, graceful shutdown
- **Integrated Logging**: Automatic integration with `libs.python.logging` for OTLP export
- **Custom Uvicorn Worker**: Disables uvicorn's logging to use consolidated logging
- **Worker Management**: Automatic worker recycling to prevent memory leaks
- **Graceful Shutdown**: Proper timeout handling for zero-downtime deployments

## Usage

```python
from libs.python.gunicorn import get_gunicorn_config
from gunicorn.app.base import BaseApplication

# Create Gunicorn config
options = get_gunicorn_config(
    microservice_name="my-api",
    port=8000,
    workers=4,
    preload_app=True,  # Recommended for multi-worker setups
)

# Run with Gunicorn
class GunicornApplication(BaseApplication):
    def __init__(self, app_factory, options=None):
        self.options = options or {}
        self.app_factory = app_factory
        super().__init__()

    def load_config(self):
        for key, value in self.options.items():
            if key in self.cfg.settings and value is not None:
                self.cfg.set(key.lower(), value)

    def load(self):
        return self.app_factory()

GunicornApplication(create_app, options).run()
```

## Logging Integration

The gunicorn configuration automatically integrates with the consolidated logging library (`libs.python.logging`):

1. **Custom Uvicorn Worker**: Uses `libs.python.gunicorn.uvicorn_worker.UvicornWorker` by default, which disables uvicorn's own logging configuration
2. **Post-Fork Hook**: Configures logging in each worker process using `configure_logging()` from `libs.python.logging`
3. **Environment Variables**: Reads `LOG_OTLP`, `LOG_LEVEL`, `LOG_JSON_FORMAT` from environment to auto-configure

This ensures that all logs (access logs, error logs, application logs) are sent via OTLP when enabled.

### Environment Variables

- `LOG_OTLP`: Enable OTLP logging (true/false)
- `LOG_LEVEL`: Logging level (DEBUG/INFO/WARNING/ERROR/CRITICAL)
- `LOG_JSON_FORMAT`: Use JSON formatting for console logs (true/false)
- `APP_NAME`, `APP_VERSION`, `APP_ENV`: Auto-detected by consolidated logging

## Configuration Options

### Required Parameters

- `microservice_name`: Service name for log identification (e.g., "experience-api")

### Optional Parameters

- `port`: Port to bind to (default: 8000)
- `workers`: Number of worker processes (default: 4, increased for better concurrency)
- `worker_class`: Worker class (default: "libs.python.gunicorn.uvicorn_worker.UvicornWorker")
- `threads`: Number of threads per worker for blocking operations (default: 2)
- `preload_app`: Preload app before forking (default: True)
- `enable_otel`: OTEL flag (deprecated, use `LOG_OTLP` env var instead)

## Production Defaults

The configuration includes production-ready defaults:

- **Worker Count**: 4 workers by default for improved concurrency
- **Thread Pool**: 2 threads per worker for blocking operations
- **Worker Recycling**: Workers restart after 1000 requests (±100 jitter) to prevent memory leaks
- **Timeouts**: 120s worker timeout (increased for long-running requests), 30s graceful shutdown
- **Keepalive**: 5s HTTP keepalive (increased for persistent connections)
- **Logging**: Structured access logs with service name, stdout/stderr output, OTLP integration

## Integration with CLI Providers

This library is designed to work with the CLI provider pattern:

```python
@app.command()
@logging_params  # Configures logging before starting server
def start_api(ctx: typer.Context, port: int = 8000, workers: int = 1):
    """Start the API server."""
    # Logging already configured by @logging_params decorator
    # Each gunicorn worker will also configure logging via post_fork hook
    
    # Get Gunicorn config
    options = get_gunicorn_config(
        microservice_name="my-api",
        port=port,
        workers=workers,
    )
    
    # Start server
    GunicornApplication(create_app, options).run()
```

## See Also

- **Logging**: `libs/python/logging` - OTLP-first logging configuration
- **CLI Providers**: `libs/python/cli/providers` - Auto-configuration decorators
- **RabbitMQ**: `libs/python/rmq` - RabbitMQ connection management
