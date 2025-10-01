# ManMan Helm Chart Enhancements

## Overview
This document describes the enhancements made to the ManMan Helm chart generation system to support production-ready deployments with configurable parameters.

## Implemented Features

### 1. Configurable Replicas ✅
**Problem**: Charts previously had hardcoded replicas (2 for APIs, 1 for workers).

**Solution**: 
- Added `replicas` parameter to `release_app` macro in `tools/release.bzl`
- Composer reads replicas from `app_metadata.json`
- Falls back to sensible defaults if not specified (2 for APIs, 1 for workers/jobs)

**Usage**:
```starlark
release_app(
    name = "experience_api",
    binary_target = "//manman/src/host:experience_api",
    app_type = "external-api",
    replicas = 1,  # Configure per app
)
```

**Result**: All ManMan services now configured with `replicas = 1` for development.

### 2. Per-App Ingress Configuration ✅
**Problem**: No way to configure custom ingress hosts, TLS secrets, or per-app settings.

**Solution**:
- Added `ingress_host` and `ingress_tls_secret` parameters to `release_app` macro
- Composer reads ingress config from metadata and adds to values.yaml per-app section
- Updated ingress template to use per-app config when available
- Falls back to generated host names if not specified

**Usage**:
```starlark
release_app(
    name = "experience_api",
    binary_target = "//manman/src/host:experience_api",
    app_type = "external-api",
    ingress_host = "experience.manman.local",
    ingress_tls_secret = "manman-tls",
)
```

**Generated values.yaml**:
```yaml
apps:
  experience_api:
    # ... other config
    ingress:
      host: experience.manman.local
      tlsSecretName: manman-tls
```

### 3. Configurable Health Checks ✅
**Problem**: Health checks always enabled with hardcoded `/health` path.

**Solution**:
- Added `health_check_enabled` and `health_check_path` parameters to `release_app`
- Composer respects health check configuration from metadata
- Health checks can be disabled by setting `health_check_enabled = false`
- Custom paths supported via `health_check_path` parameter

**Usage**:
```starlark
release_app(
    name = "status_api",
    binary_target = "//manman/src/host:status_api",
    app_type = "internal-api",
    health_check_enabled = true,
    health_check_path = "/api/health",
)
```

### 4. Port Configuration ✅
**Problem**: Port was hardcoded to 8000 for all APIs.

**Solution**:
- Added `port` parameter to `release_app` macro
- Composer reads port from metadata
- Falls back to 8000 for APIs if not specified

**Usage**:
```starlark
release_app(
    name = "experience_api",
    binary_target = "//manman/src/host:experience_api",
    app_type = "external-api",
    port = 8080,  # Custom port
)
```

### 5. Per-App Startup Commands and Arguments ✅
**Problem**: All ManMan services use the same binary entrypoint but need different startup commands.

**Solution**:
- Added `command` and `args` parameters to `release_app` macro
- Composer reads command/args from metadata and includes in values.yaml
- Deployment templates use args to specify which service to start
- Allows multiple services to share the same container image with different behavior

**Usage**:
```starlark
release_app(
    name = "experience_api",
    binary_target = "//manman/src/host:experience_api",
    app_type = "external-api",
    args = ["start-experience-api"],  # CLI command to run
)

release_app(
    name = "status_api",
    binary_target = "//manman/src/host:status_api",
    app_type = "internal-api",
    args = ["start-status-api"],  # Different CLI command, same image
)
```

**Generated Deployment**:
```yaml
spec:
  containers:
    - name: experience_api
      image: "ghcr.io/manman-experience_api:latest"
      args:
        - start-experience-api
```

**ManMan Services**: All services use `main.py` as entrypoint with different args:
- `experience_api`: `["start-experience-api"]`
- `status_api`: `["start-status-api"]`
- `worker_dal_api`: `["start-worker-dal-api"]`
- `status_processor`: `["start-status-processor"]`
- `migration`: `["run-migration"]`

## Current ManMan Configuration

All ManMan services now have explicit configuration:

```starlark
# Experience API - External facing
release_app(
    name = "experience_api",
    binary_target = "//manman/src/host:experience_api",
    app_type = "external-api",
    replicas = 1,
    port = 8000,
    ingress_host = "experience.manman.local",
    ingress_tls_secret = "manman-tls",
    args = ["start-experience-api"],
)

# Worker DAL API - External facing
release_app(
    name = "worker_dal_api",
    binary_target = "//manman/src/host:worker_dal_api",
    app_type = "external-api",
    replicas = 1,
    port = 8000,
    ingress_host = "dal.manman.local",
    ingress_tls_secret = "manman-tls",
    args = ["start-worker-dal-api"],
)

# Status API - Internal only
release_app(
    name = "status_api",
    binary_target = "//manman/src/host:status_api",
    app_type = "internal-api",
    replicas = 1,
    port = 8000,
    args = ["start-status-api"],
)

# Background processors
release_app(
    name = "status_processor",
    binary_target = "//manman/src/host:status_processor",
    app_type = "worker",
    replicas = 1,
    args = ["start-status-processor"],
)

# Migration job
release_app(
    name = "migration",
    binary_target = "//manman/src/host:migration",
    app_type = "job",
    replicas = 1,
    args = ["run-migration"],
)
```

## Validation

Chart builds successfully and passes helm lint:
```bash
$ bazel build //manman:manman_chart
Successfully generated Helm chart: manman-host (version 0.2.0)

$ helm lint bazel-bin/manman/manman-host_chart/manman-host/
1 chart(s) linted, 0 chart(s) failed
```

Generated resources:
- 4 Deployments (2 external APIs, 1 internal API, 1 worker) - all with custom args
- 3 Services (APIs only)
- 2 Ingresses (external APIs only, with custom hosts)
- 1 Job (migration) - with custom args
- 4 PodDisruptionBudgets

## Future Enhancements

### Environment Variables from Secrets
**Status**: Planned

Add support for environment variables sourced from Kubernetes secrets:
```starlark
release_app(
    name = "experience_api",
    env_from_secrets = {
        "DATABASE_URL": "manman-db:url",
        "RABBITMQ_URL": "manman-rabbitmq:url",
    },
)
```

This would generate deployment manifests with `envFrom` and `secretKeyRef` configurations.

### Resource Limits/Requests Configuration
**Status**: Planned

Allow customizing resource requests and limits per app via `release_app`:
```starlark
release_app(
    name = "experience_api",
    resources = {
        "requests": {"cpu": "100m", "memory": "512Mi"},
        "limits": {"cpu": "200m", "memory": "1Gi"},
    },
)
```

### Horizontal Pod Autoscaling
**Status**: Planned

Add HPA support for APIs that need dynamic scaling based on load.

## Testing the Chart

### Build and Inspect
```bash
# Build the chart
bazel build //manman:manman_chart

# Inspect generated resources
helm template manman bazel-bin/manman/manman-host_chart/manman-host/

# Check specific app config
helm template manman bazel-bin/manman/manman-host_chart/manman-host/ \
  --show-only templates/deployment.yaml | grep -A 20 experience_api
```

### Deploy to Kubernetes
```bash
# Install the chart
helm install manman bazel-bin/manman/manman-host_chart/manman-host/ \
  --namespace manman \
  --create-namespace

# Override values for production
helm install manman bazel-bin/manman/manman-host_chart/manman-host/ \
  --namespace manman \
  --set apps.experience_api.replicas=3 \
  --set apps.worker_dal_api.replicas=3
```

## Implementation Summary

### Modified Files
- `tools/release.bzl`: Added replicas, port, health_check_*, ingress_*, command, args parameters
- `tools/helm/composer.go`: 
  - Added `AppIngressConfig` struct
  - Enhanced `buildAppConfig()` to read configuration from metadata
  - Updated custom YAML writer to output per-app ingress config and command/args
- `tools/helm/templates/ingress.yaml.tmpl`: Use per-app ingress config
- `tools/helm/templates/deployment.yaml.tmpl`: Already supports command/args from values
- `manman/BUILD.bazel`: Added explicit configuration with args to all 5 services

### Metadata JSON Structure
```json
{
  "name": "experience_api",
  "app_type": "external-api",
  "port": 8000,
  "replicas": 1,
  "health_check": {
    "enabled": true,
    "path": "/health"
  },
  "ingress": {
    "host": "experience.manman.local",
    "tls_secret_name": "manman-tls"
  },
  "args": ["start-experience-api"]
}
```

## Benefits

1. **Production Ready**: Chart can now be configured for production deployments
2. **Flexible**: Each service can have custom configuration
3. **Maintainable**: Configuration in BUILD files, version controlled with code
4. **Scalable**: Easy to adjust replicas per environment
5. **Secure**: Per-app ingress allows proper TLS and host configuration
6. **Multi-Service**: Supports multiple services from same image with different startup args
