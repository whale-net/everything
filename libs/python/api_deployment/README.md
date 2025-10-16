# API Deployment Configuration

This module provides production-ready deployment configuration for Python API applications using gunicorn with uvicorn workers.

## Features

- **Production-Ready Defaults**: Sensible configuration for running FastAPI and other ASGI applications in production
- **Auto-Scaling Workers**: Automatically calculates optimal number of workers based on CPU cores
- **Flexible Configuration**: Easy to customize all gunicorn settings
- **CLI Support**: Built-in command-line interface for running applications
- **Development Mode**: Supports both development (uvicorn) and production (gunicorn) modes
- **Proper Logging**: Configured for containerized deployments with stdout/stderr logging

## Quick Start

### Basic Usage

```python
from fastapi import FastAPI
from libs.python.api_deployment import run_with_gunicorn

app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "hello world"}

if __name__ == "__main__":
    run_with_gunicorn(
        "main:app",
        app_name="my-api",
        port=8000,
    )
```

### Using the CLI Helper

```python
from fastapi import FastAPI
from libs.python.api_deployment.cli import run_from_cli

app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "hello world"}

if __name__ == "__main__":
    # Supports both development and production modes via CLI flags
    run_from_cli("main:app", app_name="my-api")
```

Then run:
```bash
# Development mode (uvicorn)
python main.py

# Production mode (gunicorn)
python main.py --production

# Custom configuration
python main.py --production --workers 4 --port 8080 --log-level debug
```

### Custom Configuration

```python
from libs.python.api_deployment import get_default_gunicorn_config, run_with_gunicorn

# Get default configuration and customize it
config = get_default_gunicorn_config(
    app_name="my-api",
    port=8080,
    workers=4,
    timeout=60,
    log_level="debug",
)

# Add custom gunicorn settings
config["worker_tmp_dir"] = "/dev/shm"
config["forwarded_allow_ips"] = "*"

# Run with custom configuration
run_with_gunicorn(
    "main:app",
    **config
)
```

## Configuration Options

### `get_default_gunicorn_config()`

Returns a dictionary with gunicorn configuration. Key parameters:

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `app_name` | str | "api" | Application name for logging |
| `host` | str | "0.0.0.0" | Host to bind to |
| `port` | int | 8000 | Port to bind to |
| `workers` | int | auto | Number of worker processes (auto: `2 * CPU + 1`) |
| `worker_class` | str | "uvicorn.workers.UvicornWorker" | Gunicorn worker class |
| `log_level` | str | "info" | Logging level |
| `timeout` | int | 30 | Worker timeout in seconds |
| `keepalive` | int | 2 | Keepalive time in seconds |
| `max_requests` | int | 1000 | Max requests per worker before restart |
| `max_requests_jitter` | int | 100 | Jitter for max_requests |

Additional configuration options can be passed as keyword arguments and will be merged into the configuration.

### Default Configuration

The default configuration includes:

```python
{
    "bind": "0.0.0.0:8000",
    "workers": (CPU_COUNT * 2) + 1,  # Auto-calculated
    "worker_class": "uvicorn.workers.UvicornWorker",
    "worker_connections": 1000,
    "timeout": 30,
    "keepalive": 2,
    "max_requests": 1000,
    "max_requests_jitter": 100,
    "preload_app": False,
    "accesslog": "-",  # stdout
    "errorlog": "-",   # stderr
    "loglevel": "info",
    "capture_output": True,
    "enable_stdio_inheritance": True,
}
```

## CLI Options

When using `create_deployment_cli()` or `run_from_cli()`, the following command-line options are available:

```bash
--host HOST               Host to bind to (default: 0.0.0.0)
--port PORT               Port to bind to (default: 8000)
--production              Run in production mode with gunicorn
--workers N               Number of gunicorn workers (default: auto)
--timeout SECONDS         Worker timeout (default: 30)
--log-level LEVEL         Logging level: debug, info, warning, error, critical
```

## Container Deployment

This configuration is optimized for container deployments:

### Dockerfile Example

```dockerfile
FROM python:3.13-slim

WORKDIR /app
COPY . /app

RUN pip install -r requirements.txt

# Run with gunicorn in production mode
CMD ["python", "main.py", "--production"]
```

### Kubernetes Deployment Example

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-api
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: api
        image: my-api:latest
        ports:
        - containerPort: 8000
        args:
        - "--production"
        - "--workers"
        - "2"  # Adjust based on container resources
        resources:
          requests:
            cpu: 100m
            memory: 256Mi
          limits:
            cpu: 500m
            memory: 512Mi
```

## Best Practices

### Worker Count

The default worker formula `(2 * CPU + 1)` is a good starting point for most applications. However:

- **CPU-bound apps**: Use `CPU + 1` workers
- **I/O-bound apps**: Use `2 * CPU + 1` or more workers
- **Containers**: Set workers based on container CPU limits, not host CPU count

### Timeouts

- Default 30s timeout is suitable for most APIs
- Increase for long-running operations
- Consider using background tasks for operations > 30s

### Resource Limits

Always set `max_requests` and `max_requests_jitter` to prevent memory leaks from affecting workers indefinitely:

```python
config = get_default_gunicorn_config(
    max_requests=1000,
    max_requests_jitter=100,  # Workers restart between 900-1100 requests
)
```

### Logging

Logs are sent to stdout/stderr by default, which is ideal for:
- Docker containers
- Kubernetes pods
- Cloud logging systems (CloudWatch, Stackdriver, etc.)

## Integration with Existing Apps

To integrate with existing FastAPI applications:

1. **Update the main entry point**:

```python
# Before
if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000)

# After
if __name__ == "__main__":
    from libs.python.api_deployment.cli import run_from_cli
    run_from_cli("main:app", app_name="my-api")
```

2. **Update container command**:

```dockerfile
# Before
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8000"]

# After
CMD ["python", "main.py", "--production"]
```

3. **Or use directly in Kubernetes**:

```yaml
command: ["python", "main.py"]
args: ["--production", "--workers", "2"]
```

## Dependencies

This module requires:
- `gunicorn` - For production WSGI/ASGI server
- `uvicorn` - For ASGI application support (via `uvicorn.workers.UvicornWorker`)

Install them with:
```bash
pip install gunicorn uvicorn
```

Or add to your `pyproject.toml`:
```toml
dependencies = [
    "gunicorn>=21.0.0",
    "uvicorn[standard]>=0.27.0",
]
```

## See Also

- [Gunicorn Configuration](https://docs.gunicorn.org/en/stable/configure.html)
- [Uvicorn Deployment](https://www.uvicorn.org/deployment/)
- [FastAPI Deployment](https://fastapi.tiangolo.com/deployment/)
