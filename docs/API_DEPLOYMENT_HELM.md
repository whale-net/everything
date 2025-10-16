# API Deployment with Helm Charts

This guide shows how to integrate the `api_deployment` module with the Everything monorepo's Helm chart system.

## Overview

The api_deployment module provides production-ready gunicorn configuration, while the Helm chart system handles Kubernetes deployment. They work together seamlessly:

1. **api_deployment**: Handles application server configuration (gunicorn/uvicorn)
2. **Helm charts**: Handles Kubernetes resource deployment (Deployment, Service, Ingress)

## Integration with release_app

When using `release_app` in your BUILD.bazel, you can specify command and args for container deployment:

```starlark
# demo/my_api/BUILD.bazel
load("//tools/bazel:release.bzl", "release_app")

release_app(
    name = "my-api",
    language = "python",
    domain = "demo",
    description = "My API with production deployment configuration",
    app_type = "external-api",
    port = 8000,
    health_check_enabled = True,
    health_check_path = "/health",
    # Use api_deployment for production deployment
    command = ["python", "demo/my_api/main.py"],
    args = ["--production", "--workers", "2"],
)
```

## Helm Chart Configuration

The Helm chart will automatically use the command and args specified in release_app:

```yaml
# Generated in values.yaml
apps:
  my-api:
    command: ["python", "demo/my_api/main.py"]
    args: ["--production", "--workers", "2"]
    port: 8000
    healthCheck:
      path: /health
```

## Dynamic Worker Configuration

For production deployments, you may want to configure workers based on environment:

### Option 1: Environment Variables in Helm Values

```yaml
# values.yaml
apps:
  my-api:
    env:
      WORKERS: "4"
    command: ["python", "demo/my_api/main.py"]
    args:
      - "--production"
      - "--workers"
      - "$(WORKERS)"
```

### Option 2: Different Configurations per Environment

```starlark
# Use different args for different environments
release_app(
    name = "my-api",
    # Development: 2 workers
    # Production: set via Helm values override
    args = ["--production", "--workers", "2"],
)
```

Then override in production:

```yaml
# values-production.yaml
apps:
  my-api:
    args:
      - "--production"
      - "--workers"
      - "8"
```

## Complete Example

### 1. Application Code

```python
# demo/my_api/main.py
from fastapi import FastAPI
from libs.python.api_deployment.cli import run_from_cli

app = FastAPI()

@app.get("/")
def read_root():
    return {"message": "hello"}

@app.get("/health")
def health_check():
    return {"status": "healthy"}

if __name__ == "__main__":
    run_from_cli("demo.my_api.main:app", app_name="my-api")
```

### 2. BUILD.bazel Configuration

```starlark
# demo/my_api/BUILD.bazel
load("@rules_python//python:defs.bzl", "py_binary")
load("//tools/bazel:release.bzl", "release_app")

py_binary(
    name = "my-api",
    srcs = ["main.py"],
    main = "main.py",
    deps = [
        "@pypi//:fastapi",
        "@pypi//:uvicorn",
        "//libs/python/api_deployment",
    ],
    visibility = ["//visibility:public"],
)

release_app(
    name = "my-api",
    language = "python",
    domain = "demo",
    description = "My API with deployment configuration",
    app_type = "external-api",
    port = 8000,
    replicas = 3,
    health_check_enabled = True,
    health_check_path = "/health",
    command = ["python", "demo/my_api/main.py"],
    args = ["--production"],
)
```

### 3. Helm Chart Generation

```bash
# Generate Helm chart
bazel build //demo/my_api:my-api_chart

# Resulting Helm chart will have:
# - Deployment with proper command/args
# - Service exposing port 8000
# - Ingress for external access
# - Health checks configured
```

### 4. Deployment

```bash
# Install chart
helm install my-api bazel-bin/demo/my_api/my-api_chart/

# Or with custom values
helm install my-api bazel-bin/demo/my_api/my-api_chart/ \
  --set apps.my-api.replicas=5 \
  --set apps.my-api.args[1]="--workers" \
  --set apps.my-api.args[2]="4"
```

## Best Practices

### 1. Always Use Health Checks

```python
@app.get("/health")
def health_check():
    return {"status": "healthy"}

@app.get("/ready")
def readiness_check():
    # Check dependencies
    return {"status": "ready"}
```

### 2. Set Appropriate Resource Limits

Match worker count to CPU limits:

```yaml
apps:
  my-api:
    resources:
      limits:
        cpu: 1000m  # 1 CPU
    args: ["--production", "--workers", "3"]  # (1 * 2) + 1
```

### 3. Use ConfigMaps for Configuration

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: my-api-config
data:
  WORKERS: "4"
  LOG_LEVEL: "info"

---
# In deployment
env:
- name: WORKERS
  valueFrom:
    configMapKeyRef:
      name: my-api-config
      key: WORKERS
```

### 4. Monitor Worker Health

Gunicorn automatically restarts unhealthy workers, but you should monitor:
- Worker timeout errors
- Memory usage per worker
- Request processing time

## Troubleshooting

### Workers Not Starting

Check container logs:
```bash
kubectl logs -f deployment/my-api
```

Common issues:
- Incorrect module path in command
- Missing dependencies
- Import errors

### High Memory Usage

Reduce workers or enable more aggressive recycling:
```python
run_from_cli(
    "demo.my_api.main:app",
    max_requests=500,  # Restart workers more frequently
)
```

### Slow Startup

Workers may take time to initialize. Increase startup probe delay:
```yaml
livenessProbe:
  initialDelaySeconds: 30  # Give workers time to start
```

## See Also

- [API Deployment Guide](../../docs/API_DEPLOYMENT.md)
- [Helm Chart Documentation](../../tools/helm/README.md)
- [Release System Documentation](../../AGENTS.md#release-system-architecture)
