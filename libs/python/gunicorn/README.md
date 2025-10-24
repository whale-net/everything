# Gunicorn Configuration Library

Production-ready Gunicorn configuration utilities for FastAPI applications.

## Features

- **Production Defaults**: Worker management, request limits, graceful shutdown
- **Logging Integration**: Structured access logs with service identification
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

## Configuration Options

### Required Parameters

- `microservice_name`: Service name for log identification (e.g., "experience-api")

### Optional Parameters

- `port`: Port to bind to (default: 8000)
- `workers`: Number of worker processes (default: 1)
- `worker_class`: Worker class (default: "uvicorn.workers.UvicornWorker")
- `preload_app`: Preload app before forking (default: True)
- `enable_otel`: OTEL flag (currently unused, kept for compatibility)

## Production Defaults

The configuration includes production-ready defaults:

- **Worker Recycling**: Workers restart after 1000 requests (±100 jitter) to prevent memory leaks
- **Timeouts**: 30s worker timeout, 30s graceful shutdown
- **Keepalive**: 2s HTTP keepalive
- **Logging**: Structured access logs with service name, stdout/stderr output

## Integration with CLI Providers

This library is designed to work with the CLI provider pattern:

```python
@app.command()
def start_api(ctx: typer.Context, port: int = 8000, workers: int = 1):
    """Start the API server."""
    # Logging already configured by @logging_params decorator
    # RabbitMQ already initialized by @rmq_params decorator
    
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
