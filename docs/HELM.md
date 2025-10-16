# Helm Chart Generation

This monorepo provides an **automatic Helm chart generation system** that creates production-ready Kubernetes charts from your application metadata.

## Quick Start

```starlark
load("//tools/helm:helm.bzl", "helm_chart")

# Generate a Helm chart from your apps
helm_chart(
    name = "my_app_chart",
    apps = [
        "//api/users:users_metadata",
        "//workers/email:email_metadata",
    ],
    chart_name = "my-application",
    chart_version = "1.0.0",
    namespace = "production",
    environment = "production",
    ingress_host = "api.example.com",
    ingress_mode = "single",  # or "per-service"
)
```

Build and deploy:
```bash
# Build the chart
bazel build //path/to:my_app_chart

# Extract the chart tarball
tar -xzf bazel-bin/path/to/my-application.tar.gz

# Deploy with Helm
helm install my-app ./my-application
```

## Architecture

The chart generation system uses a **Go-based template composition engine** that converts app metadata into complete Helm charts:

```
┌─────────────────┐    ┌─────────────────┐    ┌──────────────────┐
│ Bazel Rule      │    │ helm_composer   │    │ Helm Chart       │
│ (helm.bzl)      │───▶│ (Go Binary)     │───▶│ Package          │
│                 │    │                 │    │                  │
└─────────────────┘    └─────────────────┘    └──────────────────┘
        │                       │                       │
        │                       │                       │
        ▼                       ▼                       ▼
App Metadata Files      Template Files          Chart.yaml
Chart Configuration     (.tmpl)                 values.yaml
                                               templates/
```

**Key Features:**
- **Automatic Configuration**: Smart defaults based on app type (API, worker, job)
- **Multi-App Composition**: Combine multiple applications in one chart
- **Flexible Ingress**: Single host or per-service ingress modes
- **Resource Management**: Configurable CPU/memory limits
- **Health Checks**: Optional health check configuration for APIs (disabled by default)
- **Production-Ready**: Generates validated, deployable Helm charts

## App Types

The system recognizes four app types with different behaviors:

| Type | Deployment | Service | Ingress | Health Checks |
|------|-----------|---------|---------|---------------|
| `external-api` | ✅ | ✅ ClusterIP | ✅ | Optional (disabled by default) |
| `internal-api` | ✅ | ✅ ClusterIP | ❌ | Optional (disabled by default) |
| `worker` | ✅ | ❌ | ❌ | ❌ |
| `job` | ✅ Job | ❌ | ❌ | ❌ |

## Examples

### Single API Service

```starlark
helm_chart(
    name = "api_chart",
    apps = ["//api/users:users_metadata"],
    chart_name = "users-api",
    namespace = "production",
    ingress_host = "users.api.example.com",
)
```

### Multi-App Platform

```starlark
helm_chart(
    name = "platform_chart",
    apps = [
        "//api/users:users_metadata",
        "//api/orders:orders_metadata",
        "//workers/email:email_worker_metadata",
    ],
    chart_name = "platform",
    namespace = "production",
    ingress_host = "platform.example.com",
    ingress_mode = "single",  # /users/*, /orders/* paths
)
```

### Per-Service Ingress

```starlark
helm_chart(
    name = "microservices_chart",
    apps = [
        "//services/auth:auth_metadata",
        "//services/payments:payments_metadata",
    ],
    chart_name = "microservices",
    namespace = "production",
    ingress_host = "example.com",
    ingress_mode = "per-service",  # auth.example.com, payments.example.com
)
```

### Workers Only

```starlark
helm_chart(
    name = "workers_chart",
    apps = [
        "//workers/email:email_metadata",
        "//workers/reports:reports_metadata",
    ],
    chart_name = "background-workers",
    namespace = "workers",
    # No ingress for workers
)
```

## Configuration

The `helm_chart` rule accepts these attributes:

| Attribute | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `apps` | `label_list` | ✅ | - | List of `app_metadata` targets |
| `chart_name` | `string` | ✅ | - | Helm chart name |
| `namespace` | `string` | ✅ | - | Kubernetes namespace |
| `chart_version` | `string` | ❌ | `"0.1.0"` | Chart version (SemVer) |
| `environment` | `string` | ❌ | `"development"` | Environment (dev/staging/prod) |
| `ingress_host` | `string` | ❌ | `""` | Ingress hostname |
| `ingress_mode` | `string` | ❌ | `"single"` | `"single"` or `"per-service"` |

## Testing and Validation

The system includes comprehensive testing:

```bash
# Run unit tests (9 test cases)
bazel test //tools/helm:composer_test

# Run integration tests (helm lint validation)
bazel test //tools/helm:integration_test

# Build example charts
bazel build //demo:demo_chart //demo:fastapi_chart //demo:workers_chart
```

All generated charts are validated with `helm lint` and template rendering to ensure correctness.

## Documentation

For comprehensive documentation including:
- CLI usage and flags
- Advanced configuration options
- Resource customization
- Health check configuration
- Troubleshooting guide

See: **[tools/helm/README.md](../tools/helm/README.md)**

For additional documentation on Helm releases, see: **[HELM_RELEASE.md](HELM_RELEASE.md)**
