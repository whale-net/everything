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

```bash
# Build the chart first
bazel build //manman:manman_chart

# Development deployment
helm install manman-dev \
  bazel-bin/manman/host-services_chart/host-services \
  --namespace manman \
  --create-namespace

# With custom values
helm install manman-dev \
  bazel-bin/manman/host-services_chart/host-services \
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

## 📚 Documentation

- **[design/](./design/)** - Architecture and design documents

---

## 🛠️ Development

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
└── design/                 # Design documents
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
bazel-bin/manman/host-services_chart/host-services/
```

#### Deploy

```bash
# Install
helm install manman-dev \
  bazel-bin/manman/host-services_chart/host-services \
  --namespace manman \
  --create-namespace

# Upgrade
helm upgrade manman-dev \
  bazel-bin/manman/host-services_chart/host-services

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
  bazel-bin/manman/host-services_chart/host-services \
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
  bazel-bin/manman/host-services_chart/host-services \
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
helm upgrade manman-dev bazel-bin/manman/host-services_chart/host-services
```

**Health checks failing**
```bash
# Port forward and test directly
kubectl port-forward -n manman deployment/experience_api-dev 8000:8000
curl http://localhost:8000/health

# Should return 200 OK
```

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
      bazel-bin/manman/host-services_chart/host-services \
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
    path: bazel-bin/manman/host-services_chart/host-services
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
5. Validate: `helm lint bazel-bin/manman/host-services_chart/host-services`
6. Submit PR

---

## 📞 Support

For questions or issues:

1. Consult [design/](./design/) documents for architecture details
2. Check Bazel build logs: `bazel build //manman:manman_chart --verbose_failures`
