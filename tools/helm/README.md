# Bazel Helm Chart Generation System

**Production Ready** ✅ | **Version 1.0** | **Updated**: September 30, 2025

Generate complete, production-ready Helm charts from application definitions - no manual YAML needed.

---

## Quick Start

### 1. Define Your App

Add `app_type` to your `release_app` in `BUILD.bazel`:

```starlark
load("//tools:release.bzl", "release_app")

release_app(
    name = "my_api",
    language = "python",
    domain = "services",
    description = "My API service",
    app_type = "external-api",  # ← Choose: external-api, internal-api, worker, or job
)
```

### 2. Create a Chart

```starlark
load("//tools/helm:helm.bzl", "helm_chart")

helm_chart(
    name = "my_chart",
    apps = ["//services/my_api:my_api_metadata"],
    chart_name = "my-api",
    namespace = "production",
    environment = "prod",
    chart_version = "1.0.0",
)
```

### 3. Build & Deploy

```bash
# Build the chart
bazel build //services:my_chart

# Validate it
helm lint bazel-bin/services/my-api_chart/my-api

# Preview resources
helm template test bazel-bin/services/my-api_chart/my-api

# Deploy to cluster
helm install my-release bazel-bin/services/my-api_chart/my-api \
  --namespace production --create-namespace
```

---

## App Types

Choose the right type for your application:

| Type | Gets | Use For |
|------|------|---------|
| **external-api** | Deployment + Service + Ingress | Public APIs, web services accessible from outside cluster |
| **internal-api** | Deployment + Service (+ optional Ingress*) | Internal services, cluster-only APIs |
| **worker** | Deployment | Background workers, queue processors |
| **job** | Job (with hooks) | Migrations, batch tasks, one-time operations |

*Internal APIs can optionally expose Ingress by setting `exposeIngress: true` for debugging.

**See [APP_TYPES.md](./APP_TYPES.md) for complete reference.**

---

## Common Patterns

### Single External API

```starlark
helm_chart(
    name = "user_api_chart",
    apps = ["//api/users:users_metadata"],
    chart_name = "users-api",
    namespace = "production",
    environment = "prod",
    chart_version = "2.0.0",
)
```

**Generates**: 1 Deployment, 1 Service, 1 Ingress  
**Ingress**: `users-prod-ingress` at `users-prod.local`

### Multiple APIs (Each Gets Own Ingress)

```starlark
helm_chart(
    name = "api_platform",
    apps = [
        "//api/users:users_metadata",      # → users-prod-ingress
        "//api/products:products_metadata", # → products-prod-ingress  
        "//api/orders:orders_metadata",    # → orders-prod-ingress
    ],
    chart_name = "api-platform",
    namespace = "production",
    environment = "prod",
    chart_version = "1.0.0",
)
```

**Generates**: 3 Deployments, 3 Services, 3 Ingresses (1:1 mapping)

### Full Stack (Mixed Types)

```starlark
helm_chart(
    name = "platform_chart",
    apps = [
        "//api/users:users_metadata",         # external-api
        "//api/analytics:analytics_metadata", # internal-api
        "//workers/email:email_metadata",     # worker
        "//migrations/db:db_metadata",        # job
    ],
    chart_name = "platform",
    namespace = "production",
    environment = "prod",
    chart_version = "3.0.0",
)
```

**Generates**: 
- 3 Deployments (users, analytics, email)
- 2 Services (users, analytics)
- 1 Ingress (users only)
- 1 Job (db migration, runs first)

---

## How It Works

```
release_app(app_type="external-api")
    ↓
app_metadata.json
    ↓
helm_chart(apps=[...])
    ↓
Go Composer Tool
    ↓
Chart.yaml + values.yaml + templates/*.yaml
```

### What Gets Generated

```
my-chart/
├── Chart.yaml          # Chart metadata
├── values.yaml        # Configuration (customizable at deploy time)
└── templates/
    ├── deployment.yaml    # For external-api, internal-api, worker
    ├── service.yaml       # For external-api, internal-api
    ├── ingress.yaml       # For external-api (1:1 per app)
    ├── job.yaml          # For job types
    └── pdb.yaml          # PodDisruptionBudgets
```

---

## ArgoCD Integration

Generated charts include sync-wave annotations for proper ordering:

```
Wave -1: Jobs (migrations)
  ↓ (jobs must complete)
Wave 0:  Deployments, Services, Ingress
  ↓ (wait for health checks)
Done ✅
```

### ArgoCD Application

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: my-platform
spec:
  source:
    repoURL: https://github.com/org/repo
    path: bazel-bin/platform/platform_chart/platform
  destination:
    server: https://kubernetes.default.svc
    namespace: production
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

Jobs run first, then apps deploy.

---

## Manual Kubernetes Manifests

Add custom Kubernetes resources (ConfigMaps, Secrets, NetworkPolicies, etc.) to your charts:

```starlark
# Define your manifests
filegroup(
    name = "k8s_manifests",
    srcs = [
        "configmap.yaml",
        "networkpolicy.yaml",
    ],
    tags = ["k8s_manifests"],
)

# Include in chart
helm_chart(
    name = "my_chart_with_manifests",
    apps = [":app_metadata"],
    manual_manifests = [":k8s_manifests"],  # ← Add manifests here
    chart_name = "my-app",
    namespace = "production",
)
```

**What gets templated:**
- `namespace` → `.Values.global.namespace`
- `environment` label → `.Values.global.environment`
- Wrapped in `.Values.manifests.enabled` toggle

**Supported resources:** ConfigMaps, Secrets, NetworkPolicies, PVCs, ServiceAccounts, RBAC, and more!

**See [K8S_MANIFESTS.md](./K8S_MANIFESTS.md) for complete guide and examples.**

---

## Customizing Values

### At Build Time

Set in `release_app`:
```starlark
release_app(
    name = "my_api",
    app_type = "external-api",
    port = 8080,
    replicas = 3,
    # Custom resource requests and limits
    resources_requests_cpu = "200m",
    resources_requests_memory = "512Mi",
    resources_limits_cpu = "500m",
    resources_limits_memory = "1Gi",
)
```

**Resource Defaults**: If not specified, resources are automatically set based on `app_type` and `language`:
- **Python apps**: Optimized for lower memory (64Mi request / 256Mi limit)
- **Go apps**: Standard resources (256Mi request / 512Mi limit)
- **external-api/internal-api**: 50m CPU request / 100m limit
- **worker**: 50m CPU request / 100m limit
- **job**: 100m CPU request / 200m limit

You can customize any or all resource fields - unspecified fields will use the defaults.

### At Deploy Time

```bash
# Override replicas
helm install my-release ./chart --set apps.my_api.replicas=5

# Set ingress class
helm install my-release ./chart --set ingress.className=nginx

# Add annotations
helm install my-release ./chart \
  --set 'ingress.annotations.cert-manager\.io/cluster-issuer=letsencrypt'

# Use custom values file
helm install my-release ./chart -f my-values.yaml
```

### Values Structure

```yaml
global:
  namespace: production
  environment: prod

apps:
  my_api:
    type: external-api
    image: ghcr.io/org/my_api
    imageTag: v1.2.3
    port: 8000
    replicas: 2
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 1000m
        memory: 512Mi
    healthCheck:
      path: /health
      initialDelaySeconds: 10
      periodSeconds: 10
    ingress:
      host: my-api.example.com
      tlsSecretName: my-api-tls  # Will become my-api-tls-prod
      annotations:
        # Per-app annotations (merged with ingressDefaults.annotations)
        nginx.ingress.kubernetes.io/backend-protocol: "GRPC"
        nginx.ingress.kubernetes.io/grpc-backend: "true"

# Default settings applied to all ingresses (one ingress per external-api)
ingressDefaults:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
```

**Note**: Each external-api app gets its own dedicated Ingress resource. TLS secret names automatically include the environment suffix (e.g., `my-api-tls-prod`).

### Per-App Ingress Annotations

You can specify annotations at two levels:

1. **Global annotations** (`ingressDefaults.annotations`) - Applied to all ingresses
2. **Per-app annotations** (`apps.{app_name}.ingress.annotations`) - Applied to specific app's ingress

Per-app annotations are merged with global annotations. If the same annotation key appears in both, the per-app value takes precedence.

**Example Use Case: Mixed gRPC and HTTP Services**

```yaml
# Global annotations for all services
ingressDefaults:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
    external-dns.alpha.kubernetes.io/ttl: "120"

apps:
  # gRPC API - needs gRPC protocol annotation
  my_grpc_api:
    type: external-api
    port: 8080
    ingress:
      host: grpc-api.example.com
      annotations:
        nginx.ingress.kubernetes.io/backend-protocol: "GRPC"
        nginx.ingress.kubernetes.io/grpc-backend: "true"

  # HTTP Web UI - needs HTTP protocol (or no protocol annotation)
  my_web_ui:
    type: external-api
    port: 3000
    ingress:
      host: ui.example.com
      annotations:
        nginx.ingress.kubernetes.io/backend-protocol: "HTTP"
```

This configuration results in:
- **my_grpc_api** ingress gets: cert-manager, external-dns, AND gRPC-specific annotations
- **my_web_ui** ingress gets: cert-manager, external-dns, AND HTTP-specific annotations

---

## Bazel Commands

```bash
# Build a chart
bazel build //path/to:chart_name

# Build all charts
bazel build //...

# Find all charts
bazel query "kind('helm_chart', //...)"

# Run tests
bazel test //tools/helm:all
```

---

## Validation

### Lint Your Chart

```bash
helm lint bazel-bin/path/chart_name_chart/chart_name
```

### Preview Resources

```bash
# See all resources that will be created
helm template test bazel-bin/path/chart_name_chart/chart_name

# Check specific resource types
helm template test ./chart | grep "^kind:"

# See ingress configuration
helm template test ./chart | grep -A 20 "kind: Ingress"
```

### Dry Run Deploy

```bash
helm install my-release ./chart --dry-run --debug
```

---

## Troubleshooting

### Chart Build Fails

```bash
# Verify metadata exists
bazel query //path/to/app:app_name_metadata

# Rebuild with details
bazel build //path:chart --verbose_failures
```

### Wrong App Type Resources

Check your `app_type` in `release_app`:
- `external-api` → Deployment + Service + Ingress
- `internal-api` → Deployment + Service (no Ingress)
- `worker` → Deployment only
- `job` → Job only

### Lint Warnings About Underscores

Warnings like `hello_fastapi-production` are cosmetic. Charts work fine.

To fix: use hyphens in app names (`hello-fastapi` vs `hello_fastapi`).

### Values Not Applied

Use correct path:
```bash
--set apps.my_app.replicas=5      # ✅ App-specific
--set ingress.className=nginx      # ✅ Ingress config
--set my_app.replicas=5           # ❌ Wrong path
```

---

## Examples

All examples in `demo/BUILD.bazel`:

```bash
# Single external API
bazel build //demo:fastapi_chart

# External API with manual manifests (ConfigMap, NetworkPolicy)
bazel build //demo:fastapi_with_manifests_chart

# Single internal API  
bazel build //demo:internal_api_chart

# Worker
bazel build //demo:worker_chart

# Job
bazel build //demo:job_chart

# Multi-app (all types)
bazel build //demo:multi_app_chart
```

---

## Performance

- Single app chart: ~0.5s
- Multi-app (4 apps): ~0.7s  
- Bazel caching: ~0.2s on rebuild

---

## Documentation

- **[APP_TYPES.md](./APP_TYPES.md)** - Complete app type reference
- **[K8S_MANIFESTS.md](./K8S_MANIFESTS.md)** - Manual Kubernetes manifests guide
- **[TEMPLATES.md](./TEMPLATES.md)** - Template development guide
- **[MIGRATION.md](./MIGRATION.md)** - Migration from manual charts
- **[MILESTONE_5_COMPLETE.md](./MILESTONE_5_COMPLETE.md)** - Multi-app composition details

---

## Development

```bash
# Run all tests
bazel test //tools/helm:all

# Build composer
bazel build //tools/helm:composer

# Run composer directly
bazel run //tools/helm:composer -- --help
```

---

## FAQ

**Q: Can I customize the templates?**  
A: Yes, templates are in `tools/helm/templates/`. See [TEMPLATES.md](./TEMPLATES.md).

**Q: How do I add environment variables?**  
A: Set them in deploy-time values or add to `release_app` metadata.

**Q: Can multiple external-apis share one Ingress?**  
A: No, each external-api gets its own Ingress (1:1 mapping). This allows independent hosts and TLS configs.

**Q: Can I run multiple environments in the same cluster?**  
A: Yes! All resources include the environment suffix. Deploy with different release names and environment values:
```bash
helm install app-dev ./chart/ --set global.environment=dev
helm install app-staging ./chart/ --set global.environment=staging
helm install app-prod ./chart/ --set global.environment=prod
```
TLS secrets, services, deployments, and ingresses will all be uniquely named per environment.

**Q: Where do I set resource limits?**  
A: Override in values.yaml or at deploy time with `--set`.

**Q: Do I need to write Kubernetes YAML?**  
A: No! Define your app type, build the chart, done.

---

**Next Steps**:
1. Review [APP_TYPES.md](./APP_TYPES.md) to choose your app type
2. See [MIGRATION.md](./MIGRATION.md) if migrating from manual charts  
3. Check demo examples in `demo/BUILD.bazel`
