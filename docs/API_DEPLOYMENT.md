# API Deployment Configuration Guide

## Overview

The `libs/python/api_deployment` module provides production-ready deployment configuration for Python API applications. It simplifies running FastAPI and other ASGI applications with gunicorn and uvicorn workers, offering sensible defaults and easy customization.

## Why Use This Configuration?

Running Python ASGI applications in production requires more than just using `uvicorn` directly:

1. **Process Management**: Gunicorn manages multiple worker processes for better concurrency
2. **Worker Recycling**: Prevents memory leaks by restarting workers after serving a certain number of requests
3. **Graceful Shutdowns**: Handles SIGTERM properly for zero-downtime deployments
4. **Production Logging**: Configured for container-based deployments
5. **Resource Management**: Auto-scales workers based on available CPU cores

## Quick Start

### Method 1: Simple Integration (Recommended)

The easiest way to add production deployment to your application:

```python
from fastapi import FastAPI
from libs.python.api_deployment.cli import run_from_cli

app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "hello world"}

if __name__ == "__main__":
    run_from_cli("main:app", app_name="my-api")
```

Run in development mode:
```bash
python main.py
```

Run in production mode:
```bash
python main.py --production
```

### Method 2: Direct Usage

For more control over the configuration:

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
        workers=4,
    )
```

### Method 3: Custom Configuration

For advanced use cases:

```python
from fastapi import FastAPI
from libs.python.api_deployment import get_default_gunicorn_config
from gunicorn.app.base import BaseApplication

app = FastAPI()

class CustomGunicornApp(BaseApplication):
    def __init__(self, app, options=None):
        self.options = options or {}
        self.application = app
        super().__init__()
    
    def load_config(self):
        for key, value in self.options.items():
            if key in self.cfg.settings:
                self.cfg.set(key.lower(), value)
    
    def load(self):
        return self.application

if __name__ == "__main__":
    config = get_default_gunicorn_config(
        app_name="my-api",
        port=8000,
        workers=4,
    )
    
    # Add custom settings
    config["worker_tmp_dir"] = "/dev/shm"
    config["forwarded_allow_ips"] = "*"
    
    gunicorn_app = CustomGunicornApp(app, config)
    gunicorn_app.run()
```

## Configuration Reference

### Default Configuration

The module provides these defaults:

| Setting | Default Value | Description |
|---------|---------------|-------------|
| `host` | `0.0.0.0` | Bind to all interfaces |
| `port` | `8000` | Default port |
| `workers` | `(CPU * 2) + 1` | Auto-calculated worker count |
| `worker_class` | `uvicorn.workers.UvicornWorker` | ASGI worker class |
| `worker_connections` | `1000` | Max concurrent connections per worker |
| `timeout` | `30` | Worker timeout in seconds |
| `keepalive` | `2` | Keepalive seconds |
| `max_requests` | `1000` | Max requests before worker restart |
| `max_requests_jitter` | `100` | Random jitter for max_requests |
| `preload_app` | `False` | Don't preload application |
| `accesslog` | `-` | Log to stdout |
| `errorlog` | `-` | Log to stderr |
| `loglevel` | `info` | Logging level |

### Customizing Configuration

You can override any default by passing keyword arguments:

```python
from libs.python.api_deployment import get_default_gunicorn_config

config = get_default_gunicorn_config(
    app_name="my-api",
    host="127.0.0.1",
    port=8080,
    workers=8,
    timeout=60,
    log_level="debug",
    # Add any gunicorn setting
    worker_tmp_dir="/dev/shm",
    forwarded_allow_ips="*",
)
```

## CLI Options

When using `run_from_cli()`, these command-line options are available:

```bash
python main.py [options]

Options:
  --host HOST           Host to bind to (default: 0.0.0.0)
  --port PORT           Port to bind to (default: 8000)
  --production          Run with gunicorn (default: uvicorn for development)
  --workers N           Number of workers (default: auto-calculate)
  --timeout SECONDS     Worker timeout (default: 30)
  --log-level LEVEL     Logging level: debug, info, warning, error, critical
```

## Container Deployment

### Dockerfile

```dockerfile
FROM python:3.13-slim

WORKDIR /app

# Copy application code
COPY . /app

# Install dependencies
RUN pip install --no-cache-dir -r requirements.txt

# Expose port
EXPOSE 8000

# Run in production mode with gunicorn
CMD ["python", "main.py", "--production"]
```

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-api
spec:
  replicas: 3
  selector:
    matchLabels:
      app: my-api
  template:
    metadata:
      labels:
        app: my-api
    spec:
      containers:
      - name: api
        image: my-api:latest
        command: ["python", "main.py"]
        args: ["--production", "--workers", "2"]
        ports:
        - containerPort: 8000
          name: http
        resources:
          requests:
            cpu: 100m
            memory: 256Mi
          limits:
            cpu: 500m
            memory: 512Mi
        livenessProbe:
          httpGet:
            path: /health
            port: 8000
          initialDelaySeconds: 10
          periodSeconds: 30
        readinessProbe:
          httpGet:
            path: /health
            port: 8000
          initialDelaySeconds: 5
          periodSeconds: 10
```

### Docker Compose

```yaml
version: '3.8'

services:
  api:
    build: .
    command: python main.py --production --workers 4
    ports:
      - "8000:8000"
    environment:
      - LOG_LEVEL=info
    restart: unless-stopped
```

## Best Practices

### Worker Count

**Default formula**: `(CPU cores * 2) + 1`

This works well for I/O-bound applications (most APIs). Adjust based on your workload:

- **CPU-bound apps**: Use `CPU cores + 1`
- **I/O-bound apps**: Use `2 * CPU cores + 1` or more
- **Containers**: Base on container CPU limits, not host CPU

Example for a container with 2 CPU limit:
```bash
python main.py --production --workers 5  # (2 * 2) + 1
```

### Timeout Configuration

Default 30 seconds is suitable for most APIs. Consider:

- **Fast APIs**: 30s is fine
- **Slower operations**: Increase to 60s or more
- **Long-running tasks**: Use background tasks instead

```python
# For slower operations
run_with_gunicorn("main:app", timeout=60)
```

### Resource Limits

Always configure worker recycling to prevent memory leaks:

```python
config = get_default_gunicorn_config(
    max_requests=1000,           # Restart after 1000 requests
    max_requests_jitter=100,     # Add randomness (900-1100 range)
)
```

### Logging

The configuration logs to stdout/stderr by default, which is ideal for:
- Docker/Kubernetes (logs are captured by container runtime)
- Cloud platforms (CloudWatch, Stackdriver, Azure Monitor)
- Log aggregation systems (ELK, Splunk, Datadog)

### Health Checks

Always implement health check endpoints:

```python
@app.get("/health")
def health_check():
    return {"status": "healthy"}

@app.get("/ready")
def readiness_check():
    # Check database, dependencies, etc.
    return {"status": "ready"}
```

Use these in your Kubernetes probes:
- **Liveness**: `/health` - Is the app running?
- **Readiness**: `/ready` - Is the app ready to serve traffic?

## Migration from Existing Apps

### From Direct Uvicorn

**Before:**
```python
if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8000)
```

**After:**
```python
if __name__ == "__main__":
    from libs.python.api_deployment.cli import run_from_cli
    run_from_cli("main:app", app_name="my-api")
```

### From Uvicorn Command Line

**Before:**
```bash
uvicorn main:app --host 0.0.0.0 --port 8000
```

**After:**
```bash
python main.py --production
```

### From Manual Gunicorn

**Before:**
```bash
gunicorn main:app -w 4 -k uvicorn.workers.UvicornWorker
```

**After:**
```bash
python main.py --production --workers 4
```

## Examples

See the following examples in the repository:

1. **Minimal Example**: `demo/hello_fastapi/example_minimal.py`
   - Simplest possible integration
   
2. **Full CLI Example**: `demo/hello_fastapi/main_with_deployment.py`
   - Uses CLI helper for development and production modes
   
3. **Original Example**: `demo/hello_fastapi/main.py`
   - Shows traditional uvicorn approach for comparison

## Troubleshooting

### Workers Not Starting

**Issue**: Workers fail to start or timeout immediately

**Solutions**:
- Check if the app module path is correct
- Ensure the ASGI app variable name is correct
- Check for import errors in your application
- Verify uvicorn is installed: `pip install uvicorn`

### Memory Issues

**Issue**: High memory usage or OOM errors

**Solutions**:
- Reduce worker count
- Enable worker recycling:
  ```python
  run_with_gunicorn("main:app", max_requests=500)
  ```
- Check for memory leaks in your application code

### Slow Response Times

**Issue**: API is slow under load

**Solutions**:
- Increase worker count
- Increase worker connections:
  ```python
  config = get_default_gunicorn_config(worker_connections=2000)
  ```
- Profile your application for bottlenecks

### Import Errors

**Issue**: `ModuleNotFoundError` when running

**Solutions**:
- Ensure the module path uses dots not slashes: `main:app` not `main/app`
- Check that the module is in your Python path
- For Bazel builds, use the full module path: `demo.hello_fastapi.main:app`

## Dependencies

The module requires:

```toml
dependencies = [
    "gunicorn>=21.0.0",
    "uvicorn[standard]>=0.27.0",
]
```

Install with:
```bash
pip install gunicorn uvicorn[standard]
```

## Further Reading

- [Gunicorn Documentation](https://docs.gunicorn.org/)
- [Uvicorn Deployment Guide](https://www.uvicorn.org/deployment/)
- [FastAPI Deployment](https://fastapi.tiangolo.com/deployment/)
- [Production ASGI Deployments](https://www.uvicorn.org/deployment/#running-with-gunicorn)
