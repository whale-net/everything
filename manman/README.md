# ManMan - Manifest Management System

A microservices-based manifest management system for game server orchestration.

---

## 🚀 Quick Start

### Build Everything

```bash
# Build all manman services
bazel build //manman/...

# Build the Helm chart
bazel build //manman:manman_chart
```

### Deploy with Helm

⚠️ **Important**: The auto-generated chart has limitations for production use.  
See [CHART_LIMITATIONS.md](./CHART_LIMITATIONS.md) for details.

```bash
# Development deployment (with custom values)
helm install manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  -f manman/values-dev.yaml \
  --namespace manman \
  --create-namespace

# Or use the manual chart (recommended for production)
helm install manman-dev \
  manman/__manual_backup_of_old_chart/charts/manman-host \
  -f custom-values.yaml \
  --namespace manman
```

---

## 📦 Services

ManMan consists of 5 microservices:

### APIs

| Service | Type | Port | Description | Access |
|---------|------|------|-------------|--------|
| **Experience API** | external-api | 8000 | User-facing game server management | Public |
| **Worker DAL API** | external-api | 8000 | Worker data access layer | Public |
| **Status API** | internal-api | 8000 | Status monitoring and health | Internal |

### Workers

| Service | Type | Description |
|---------|------|-------------|
| **Status Processor** | worker | Background status event processor |
| **Worker** | worker | General background task processor |

### Jobs

| Service | Type | Description |
|---------|------|-------------|
| **Migration** | job | Database schema migrations (runs first) |

---

## 🏗️ Architecture

```
┌─────────────────┐     ┌─────────────────┐
│ Experience API  │────▶│  Status API     │ (internal)
│ (external)      │     │                 │
└────────┬────────┘     └────────┬────────┘
         │                       │
         │                       │
         ▼                       ▼
    ┌─────────────────────────────────┐
    │      PostgreSQL Database        │
    │  (with Alembic migrations)      │
    └─────────────────────────────────┘
         ▲                       ▲
         │                       │
┌────────┴────────┐     ┌────────┴────────┐
│ Worker DAL API  │     │ Status Processor│
│ (external)      │     │    (worker)     │
└─────────────────┘     └─────────────────┘
         ▲
         │
    ┌────┴─────┐
    │ RabbitMQ │
    └──────────┘
```

---

## ⚠️ Helm Chart Status

### Auto-Generated Chart (Current)

The composed Helm chart (`//manman:manman_chart`) is auto-generated from Bazel but has limitations:

**What Works:**
- ✅ All 5 services deployed correctly
- ✅ Migration job runs first (pre-install hook)
- ✅ Services and Ingresses created
- ✅ Basic health checks
- ✅ Resource limits

**What's Missing:**
- ❌ Per-app ingress configuration (host, TLS, annotations)
- ❌ Kubernetes Secret references (`valueFrom.secretKeyRef`)
- ❌ ConfigMap references (`envFrom`)
- ❌ Configurable replicas at build time (hardcoded to 1 or 2)
- ❌ Optional health checks
- ❌ Custom health check paths

**Workarounds:**
- Use `values-dev.yaml` for development (plain-text env vars)
- Use manual chart for production
- See [CHART_LIMITATIONS.md](./CHART_LIMITATIONS.md) for details

### Manual Chart (Production Ready)

The manual chart in `__manual_backup_of_old_chart/` has full production features:
- ✅ Environment variable configuration
- ✅ Secret references
- ✅ Custom health check paths
- ✅ Configurable replicas
- ✅ Ingress customization

**Recommendation:** Use manual chart for production until auto-generated chart is enhanced.

---

## 📚 Documentation

- **[CHART_LIMITATIONS.md](./CHART_LIMITATIONS.md)** - ⚠️ **READ THIS** - Critical limitations of auto-generated chart
- **[CHART_MIGRATION.md](./CHART_MIGRATION.md)** - Complete migration guide from manual to composed chart
- **[CHART_QUICKSTART.md](./CHART_QUICKSTART.md)** - Quick reference for daily operations
- **[CHART_COMPARISON.md](./CHART_COMPARISON.md)** - Side-by-side comparison of charts
- **[design/](./design/)** - Architecture and design documents
- **[values-dev.yaml](./values-dev.yaml)** - Development deployment values (with env vars)
- **[values-production.yaml](./values-production.yaml)** - Production deployment example
- **[k8s-secrets-example.yaml](./k8s-secrets-example.yaml)** - Kubernetes Secrets template

---

## 🛠️ Development

### Module Structure

The ManMan codebase has been refactored into granular Bazel targets for better modularity and build performance. See [REFACTORING.md](./REFACTORING.md) and [TARGET_DEPENDENCIES.md](./TARGET_DEPENDENCIES.md) for details.

**Module Overview**:
- `//manman/src` - Core module (models, config, logging, utilities)
- `//manman/src/repository` - Repository layer (database, RabbitMQ, messaging, API client)
- `//manman/src/worker` - Worker services (core, server, service, main)
- `//manman/src/host` - Host APIs (shared, experience, status, worker DAL, status processor, main)
- `//manman/src/migrations` - Database migrations

Each module is split into focused libraries plus an aggregate library for backward compatibility.

### Project Structure

```
manman/
├── BUILD.bazel              # Release apps and Helm chart definition
├── src/                     # Source code
│   ├── host/               # API services and main entry point
│   │   ├── api/           # FastAPI applications
│   │   └── main.py        # CLI with all service commands
│   ├── worker/            # Background worker service
│   ├── repository/        # Data access layer
│   ├── migrations/        # Alembic database migrations
│   └── BUILD.bazel        # Build targets
├── __manual_backup_of_old_chart/  # Legacy manual Helm chart (backup)
└── docs/                   # Documentation
```

### Running Services Locally

```bash
# Experience API
bazel run //manman/src/host:experience_api

# Status API  
bazel run //manman/src/host:status_api

# Worker DAL API
bazel run //manman/src/host:worker_dal_api

# Status Processor
bazel run //manman/src/host:status_processor

# Run migrations
bazel run //manman/src/host:migration
```

### Running Tests

```bash
# All tests
bazel test //manman/...

# Specific test suites
bazel test //manman/src:manman_core_test
bazel test //manman/src/host:manman_host_test
```

---

## 🐳 Container Images

### Building Images

```bash
# Build all images
bazel build //manman:experience_api_image
bazel build //manman:status_api_image
bazel build //manman:worker_dal_api_image
bazel build //manman:status_processor_image
bazel build //manman:migration_image
```

### Running Containers Locally

```bash
# Load and run
bazel run //manman:experience_api_image_load
docker run --rm -p 8000:8000 experience_api:latest
```

---

## ☸️ Kubernetes Deployment

### Using Helm Chart (Recommended)

The manman services are deployed using a **composed Helm chart** that bundles all 5 services together.

#### Chart Location

```bash
# Build first
bazel build //manman:manman_chart

# Chart generated at:
bazel-bin/manman/manman-host_chart/manman-host/
```

#### Deploy

```bash
# Install
helm install manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  --namespace manman \
  --create-namespace

# Upgrade
helm upgrade manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host

# Uninstall
helm uninstall manman-dev --namespace manman
```

#### Customize Deployment

```yaml
# custom-values.yaml
apps:
  experience_api:
    replicas: 5
    resources:
      limits:
        memory: "1Gi"
        cpu: "1000m"
```

```bash
helm install manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  -f custom-values.yaml
```

### Resource Names in Kubernetes

When deployed, services are named as `{service}-{environment}`:

- `experience_api-dev`
- `status_api-dev`
- `worker_dal_api-dev`
- `status_processor-dev`
- `migration-dev` (job)

### Service Discovery

```bash
# From within the cluster
http://experience_api-dev-service.manman.svc.cluster.local:8000
http://status_api-dev-service.manman.svc.cluster.local:8000
http://worker_dal_api-dev-service.manman.svc.cluster.local:8000
```

---

## 🔄 Migration System

ManMan uses Alembic for database migrations.

### Migration Job

The migration job runs automatically before other services start:

- **Helm hook**: `pre-install,pre-upgrade`
- **ArgoCD sync-wave**: `-1` (runs first)
- **Command**: `host run-migration`

### Manual Migration Operations

```bash
# Run migrations
bazel run //manman/src/host:migration

# Create new migration (dev only)
bazel run //manman/src/host:migration -- create-migration "add user table"

# Downgrade
bazel run //manman/src/host:migration -- run-downgrade <revision>
```

### Migration Files

```
manman/src/migrations/
├── versions/           # Migration scripts
├── env.py             # Alembic environment config
└── script.py.mako     # Migration template
```

---

## 🔧 Configuration

### Environment Variables

All services require these environment variables:

```bash
# Database
MANMAN_POSTGRES_URL=postgresql+psycopg2://user:pass@host:5432/db

# RabbitMQ
MANMAN_RABBITMQ_HOST=rabbitmq.example.com
MANMAN_RABBITMQ_PORT=5672
MANMAN_RABBITMQ_USER=user
MANMAN_RABBITMQ_PASSWORD=pass
MANMAN_RABBITMQ_ENABLE_SSL=false
MANMAN_RABBITMQ_SSL_HOSTNAME=

# Environment
APP_ENV=dev  # dev, staging, prod

# OpenTelemetry (optional)
MANMAN_LOG_OTLP=false
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=
```

### Configuration in Kubernetes

Set via Helm values or ConfigMap:

```yaml
apps:
  experience_api:
    env:
      - name: MANMAN_POSTGRES_URL
        value: "postgresql://..."
      - name: APP_ENV
        value: "production"
```

---

## 📊 Monitoring

### Health Checks

All services expose a `/health` endpoint:

```bash
# Experience API
curl http://localhost:8000/health

# Status API
curl http://localhost:8000/health
```

### Health Check Configuration

```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8000
  initialDelaySeconds: 10
  periodSeconds: 10

readinessProbe:
  httpGet:
    path: /health
    port: 8000
  initialDelaySeconds: 10
  periodSeconds: 10
```

### OpenTelemetry Support

ManMan supports OpenTelemetry for distributed tracing and logging:

```bash
# Enable OTLP logging
--log-otlp

# Set endpoints
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://otel-collector:4317
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://otel-collector:4317
```

---

## 🔐 Security

### RabbitMQ SSL

Enable SSL for RabbitMQ connections:

```bash
MANMAN_RABBITMQ_ENABLE_SSL=true
MANMAN_RABBITMQ_SSL_HOSTNAME=rabbitmq.secure.example.com
```

### Production Considerations

- Use Kubernetes Secrets for sensitive values
- Enable TLS for Ingress endpoints
- Configure NetworkPolicies to restrict traffic
- Use PodSecurityPolicies or Pod Security Standards
- Set appropriate resource limits

---

## 📈 Scaling

### Horizontal Scaling

Scale services via Helm values:

```bash
helm upgrade manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  --set apps.experience_api.replicas=10 \
  --set apps.status_processor.replicas=5
```

### Recommended Replica Counts

| Service | Dev | Staging | Production |
|---------|-----|---------|------------|
| Experience API | 2 | 3 | 5-10 |
| Status API | 2 | 2 | 3-5 |
| Worker DAL API | 2 | 3 | 5-10 |
| Status Processor | 2 | 3 | 5+ |

---

## 🐛 Troubleshooting

### Common Issues

**Services won't start**
```bash
# Check logs
kubectl logs -n manman deployment/experience_api-dev

# Common causes:
# - Missing environment variables
# - Database connection failed
# - RabbitMQ connection failed
```

**Migration job failed**
```bash
# View migration logs
kubectl logs -n manman job/migration-dev

# Delete and retry
kubectl delete job -n manman migration-dev
helm upgrade manman-dev bazel-bin/manman/manman-host_chart/manman-host
```

**Health checks failing**
```bash
# Port forward and test directly
kubectl port-forward -n manman deployment/experience_api-dev 8000:8000
curl http://localhost:8000/health

# Should return 200 OK
```

See [CHART_QUICKSTART.md](./CHART_QUICKSTART.md) for more troubleshooting steps.

---

## 🚢 CI/CD

### GitHub Actions Integration

```yaml
- name: Build manman services
  run: bazel build //manman/...

- name: Build Helm chart
  run: bazel build //manman:manman_chart

- name: Deploy to dev
  run: |
    helm upgrade --install manman-dev \
      bazel-bin/manman/manman-host_chart/manman-host \
      --namespace manman \
      --create-namespace \
      --wait
```

### ArgoCD Integration

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: manman
spec:
  source:
    path: bazel-bin/manman/manman-host_chart/manman-host
  destination:
    namespace: manman
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
```

---

## 📝 License

See repository root for license information.

---

## 🤝 Contributing

1. Create feature branch
2. Make changes
3. Run tests: `bazel test //manman/...`
4. Build chart: `bazel build //manman:manman_chart`
5. Validate: `helm lint bazel-bin/manman/manman-host_chart/manman-host`
6. Submit PR

---

## 📞 Support

For questions or issues:

1. Check [CHART_QUICKSTART.md](./CHART_QUICKSTART.md) for quick answers
2. Review [CHART_MIGRATION.md](./CHART_MIGRATION.md) for detailed info
3. Consult [design/](./design/) documents for architecture details
4. Check Bazel build logs: `bazel build //manman:manman_chart --verbose_failures`
