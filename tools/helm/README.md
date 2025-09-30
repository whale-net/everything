# Helm Chart Composition System

## Overview

The Helm Chart Composition System is a Bazel-integrated tool that automatically generates Kubernetes Helm charts from application metadata. It's designed to compose multiple applications into a single, production-ready Helm chart with sensible defaults and flexible configuration.

## Features

- **Automatic Chart Generation**: Converts app metadata into complete Helm charts
- **Multi-App Composition**: Combine multiple applications into a single chart
- **Smart Defaults**: Automatic configuration based on app type (external-api, internal-api, worker, job)
- **Ingress Management**: Flexible ingress modes (single or per-service)
- **Resource Management**: Configurable CPU/memory requests and limits
- **Health Checks**: Automatic health check configuration for API services
- **Environment Support**: Development, staging, and production configurations

## Architecture

### Components

1. **Type System** (`types.go`): Core types and validation
   - `AppType`: External API, Internal API, Worker, Job
   - `ResourceConfig`: CPU and memory configurations
   - `HealthCheckConfig`: Readiness and liveness probes

2. **Composer** (`composer.go`): Chart generation engine
   - Metadata loading and validation
   - Chart.yaml generation
   - values.yaml generation with custom YAML formatting
   - Template copying and organization

3. **CLI** (`cmd/helm_composer/main.go`): Command-line interface
   - Flag-based configuration
   - Metadata file processing
   - Chart output management

4. **Bazel Rule** (`helm.bzl`): Build system integration
   - `helm_chart` macro for declarative chart generation
   - Automatic dependency management
   - Tarball packaging for distribution

## Usage

### Using the Bazel Rule (Recommended)

```python
load("//tools/helm:helm.bzl", "helm_chart")

helm_chart(
    name = "my_app_chart",
    apps = [
        "//path/to/app1:app1_metadata",
        "//path/to/app2:app2_metadata",
    ],
    chart_name = "my-application",
    chart_version = "1.0.0",
    namespace = "production",
    environment = "production",
    ingress_host = "api.example.com",
    ingress_mode = "single",  # or "per-service"
)
```

Build the chart:
```bash
bazel build //path/to:my_app_chart
```

This generates:
- `bazel-bin/path/to/my-application.tar.gz` - Packaged chart
- `bazel-bin/path/to/my-application_chart/` - Chart directory

### Using the CLI Directly

```bash
bazel run //tools/helm:helm_composer -- \
  --metadata path/to/app1_metadata.json,path/to/app2_metadata.json \
  --chart-name my-app \
  --version 1.0.0 \
  --environment production \
  --namespace prod \
  --output /output/path \
  --ingress-host api.example.com \
  --ingress-mode single \
  --template-dir tools/helm/templates
```

## Configuration

### helm_chart Rule Attributes

| Attribute | Type | Default | Description |
|-----------|------|---------|-------------|
| `apps` | `label_list` | **required** | List of `app_metadata` targets |
| `chart_name` | `string` | **required** | Name of the Helm chart |
| `chart_version` | `string` | `"0.1.0"` | Chart version (SemVer) |
| `namespace` | `string` | **required** | Kubernetes namespace |
| `environment` | `string` | `"development"` | Target environment |
| `ingress_host` | `string` | `""` | Ingress hostname |
| `ingress_mode` | `string` | `"single"` | `"single"` or `"per-service"` |

### App Types and Behavior

#### External API (`external-api`)
- Creates Deployment
- Creates Service (ClusterIP)
- Adds to Ingress
- Configures health checks
- Sets replicas >= 2 for HA

#### Internal API (`internal-api`)
- Creates Deployment
- Creates Service (ClusterIP)
- No Ingress exposure
- Configures health checks

#### Worker (`worker`)
- Creates Deployment
- No Service
- No Ingress
- No health checks

#### Job (`job`)
- Creates Job (future: CronJob support)
- No Service
- No Ingress

### Resource Defaults

```yaml
resources:
  requests:
    cpu: 50m
    memory: 256Mi
  limits:
    cpu: 100m
    memory: 512Mi
```

Override in app metadata:
```json
{
  "name": "my-app",
  "resources": {
    "requests_cpu": "100m",
    "requests_memory": "512Mi",
    "limits_cpu": "200m",
    "limits_memory": "1Gi"
  }
}
```

### Health Check Defaults

```yaml
healthCheck:
  path: /health
  port: 8000
  initialDelaySeconds: 10
  periodSeconds: 10
```

## Examples

### Example 1: Single API Service

```python
helm_chart(
    name = "api_chart",
    apps = ["//api/users:users_metadata"],
    chart_name = "users-api",
    namespace = "production",
    environment = "production",
    ingress_host = "users.api.example.com",
    chart_version = "1.2.3",
)
```

### Example 2: Multi-App with Single Ingress

```python
helm_chart(
    name = "platform_chart",
    apps = [
        "//api/users:users_metadata",
        "//api/orders:orders_metadata",
        "//workers/email:email_worker_metadata",
    ],
    chart_name = "platform",
    namespace = "production",
    environment = "production",
    ingress_host = "platform.example.com",
    ingress_mode = "single",
)
```

Generates ingress with paths:
- `/users/*` → users service
- `/orders/*` → orders service

### Example 3: Per-Service Ingress

```python
helm_chart(
    name = "microservices_chart",
    apps = [
        "//services/auth:auth_metadata",
        "//services/payments:payments_metadata",
    ],
    chart_name = "microservices",
    namespace = "production",
    environment = "production",
    ingress_host = "example.com",
    ingress_mode = "per-service",
)
```

Generates separate ingress for each API:
- `auth.example.com` → auth service
- `payments.example.com` → payments service

### Example 4: Workers Only

```python
helm_chart(
    name = "workers_chart",
    apps = [
        "//workers/email:email_metadata",
        "//workers/reports:reports_metadata",
    ],
    chart_name = "background-workers",
    namespace = "workers",
    environment = "production",
)
```

No ingress generated (workers don't expose services).

## Generated Chart Structure

```
my-chart/
├── Chart.yaml              # Chart metadata
├── values.yaml            # Configurable values
└── templates/
    ├── deployment.yaml    # App deployments
    ├── service.yaml       # Services (for APIs)
    ├── ingress.yaml       # Ingress rules (if APIs exist)
    ├── job.yaml          # Jobs (if job apps exist)
    └── pdb.yaml          # Pod Disruption Budgets
```

### Sample Chart.yaml

```yaml
apiVersion: v2
name: my-application
description: Composed Helm chart for multiple applications
type: application
version: 1.0.0
appVersion: "1.0.0"
```

### Sample values.yaml

```yaml
global:
  namespace: production
  environment: production

apps:
  my_app:
    image: ghcr.io/org/my-app
    imageTag: v1.0.0
    replicas: 2
    resources:
      requests:
        cpu: 50m
        memory: 256Mi
      limits:
        cpu: 100m
        memory: 512Mi
    healthCheck:
      path: /health
      port: 8000
      initialDelaySeconds: 10
      periodSeconds: 10

ingress:
  enabled: true
  mode: single
  host: api.example.com
```

## Testing

### Unit Tests

```bash
# Test composer functionality
bazel test //tools/helm:composer_test

# Test type system
bazel test //tools/helm:types_test
```

### Integration Tests

```bash
# Full end-to-end test with helm lint
bazel test //tools/helm:integration_test
```

### Manual Testing

```bash
# Build a chart
bazel build //demo:demo_chart

# Extract and inspect
tar -xzf bazel-bin/demo/demo-apps.tar.gz -C /tmp
helm lint /tmp/demo-apps

# Render templates (dry run)
helm template test-release /tmp/demo-apps

# Test deployment (requires cluster)
helm install test-release /tmp/demo-apps --dry-run
```

## Troubleshooting

### Chart fails helm lint

Check that all required fields are in app metadata:
- `name`, `app_type`, `version`
- For APIs: `port`, `health_check_path`

### Ingress not generated

Ensure at least one app has `app_type: "external-api"`.

### Wrong resource limits

Override in app metadata JSON or customize `values.yaml` after generation.

### Template rendering errors

Verify template syntax:
```bash
helm template test /tmp/chart --debug
```

## Implementation Details

### Custom YAML Formatting

The composer uses a custom YAML formatter (`formatYAML`) instead of external libraries to:
- Maintain clean, readable output
- Avoid unnecessary dependencies
- Ensure consistent formatting

### Template Functions

Available in templates via `{{toYaml .field}}`:
- Proper indentation handling
- Map formatting with key-value pairs
- List formatting with YAML list syntax

### Metadata Collection

The Bazel rule automatically:
1. Collects metadata JSON files from dependencies
2. Passes file paths to helm_composer
3. Composer loads and validates each metadata file
4. Generates unified chart configuration

## Future Enhancements

- [ ] CronJob support for scheduled jobs
- [ ] ConfigMap and Secret generation
- [ ] ServiceMonitor for Prometheus
- [ ] HorizontalPodAutoscaler support
- [ ] NetworkPolicy generation
- [ ] Multi-namespace deployments
- [ ] Helm hooks for migrations

## See Also

- [AGENT.md](../../AGENT.md) - Repository architecture and patterns
- [Release System](../release.bzl) - Integration with release automation
- [Demo Apps](../../demo/) - Example applications with helm_chart targets
