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

**Ingress Template Enhancement**:
- Uses `$app.ingress.host` if provided
- Uses `$app.ingress.tlsSecretName` for TLS configuration
- Falls back to `{{ $appName }}-{{ $.Values.global.environment }}.local` if not specified

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

**Behavior**:
- APIs default to health checks at `/health`
- Workers and jobs have no health checks by default
- Can be customized or disabled per app

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
)

# Status API - Internal only
release_app(
    name = "status_api",
    binary_target = "//manman/src/host:status_api",
    app_type = "internal-api",
    replicas = 1,
    port = 8000,
)

# Background processors
release_app(
    name = "status_processor",
    binary_target = "//manman/src/host:status_processor",
    app_type = "worker",
    replicas = 1,
)

# Migration job
release_app(
    name = "migration",
    binary_target = "//manman/src/host:migration",
    app_type = "job",
    replicas = 1,
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
- 4 Deployments (2 external APIs, 1 internal API, 1 worker)
- 3 Services (APIs only)
- 2 Ingresses (external APIs only, with custom hosts)
- 1 Job (migration)
- 4 PodDisruptionBudgets

## Still To Do

### Environment Variables and Secrets
**Status**: Not yet implemented

**Required**:
- Add `env_vars` dict parameter to `release_app`
- Support for `envFrom` with `secretRef` in deployment template
- Allow mixing of direct env vars and secret references

**Proposed Usage**:
```starlark
release_app(
    name = "experience_api",
    env_vars = {
        "DATABASE_URL": "valueFrom:secret:manman-db:url",
        "RABBITMQ_URL": "valueFrom:secret:manman-rabbitmq:url",
        "LOG_LEVEL": "info",
    },
)
```

### Deployment Template Enhancement
**Status**: Planned

**Required**:
- Update `deployment.yaml.tmpl` to render env vars from values.yaml
- Support both direct values and secret references
- Add `envFrom` support for entire secret mounting

## Testing the Chart

### Build and Inspect
```bash
# Build the chart
bazel build //manman:manman_chart

# Inspect generated resources
helm template manman bazel-bin/manman/manman-host_chart/manman-host/

# Check specific app config
helm template manman bazel-bin/manman/manman-host_chart/manman-host/ \
  --show-only templates/ingress.yaml
```

### Deploy to Kubernetes
```bash
# Install the chart
helm install manman bazel-bin/manman/manman-host_chart/manman-host/ \
  --namespace manman \
  --create-namespace

# Override replicas for production
helm install manman bazel-bin/manman/manman-host_chart/manman-host/ \
  --namespace manman \
  --set apps.experience_api.replicas=3 \
  --set apps.worker_dal_api.replicas=3
```

### Customize with values file
```yaml
# custom-values.yaml
apps:
  experience_api:
    replicas: 3
    resources:
      requests:
        cpu: 100m
        memory: 512Mi
      limits:
        cpu: 200m
        memory: 1Gi
    ingress:
      host: experience.production.example.com
      tlsSecretName: prod-tls-cert

helm install manman ./chart --values custom-values.yaml
```

## Implementation Details

### Modified Files
- `tools/release.bzl`: Added replicas, port, health_check_*, ingress_* parameters
- `tools/helm/composer.go`: 
  - Added `AppIngressConfig` struct
  - Enhanced `buildAppConfig()` to read configuration from metadata
  - Updated custom YAML writer to output per-app ingress config
- `tools/helm/templates/ingress.yaml.tmpl`: Use per-app ingress config
- `manman/BUILD.bazel`: Added explicit configuration to all services

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
  }
}
```

## Benefits

1. **Production Ready**: Chart can now be configured for production deployments
2. **Flexible**: Each service can have custom configuration
3. **Maintainable**: Configuration in BUILD files, version controlled with code
4. **Scalable**: Easy to adjust replicas per environment
5. **Secure**: Per-app ingress allows proper TLS and host configuration

## Next Steps

1. Implement environment variable and secrets support
2. Add resource limits/requests configuration
3. Consider adding HorizontalPodAutoscaler support
4. Add ServiceMonitor for Prometheus integration
5. Document upgrade path from manual chart to composed chart
