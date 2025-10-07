# Helm Chart App Types Reference

This document provides a complete reference for all supported application types in the helm chart system.

## Overview

The helm chart system supports 4 app types, each generating different Kubernetes resources:

| App Type | Deployment | Service | Ingress | PDB | Job | Use Case |
|----------|-----------|---------|---------|-----|-----|----------|
| `external-api` | ✅ | ✅ | ✅ | ✅ | ❌ | Public HTTP APIs (REST, GraphQL) |
| `internal-api` | ✅ | ✅ | ❌ | ✅ | ❌ | Internal HTTP services |
| `worker` | ✅ | ❌ | ❌ | ✅ | ❌ | Background processors (queues, streams) |
| `job` | ❌ | ❌ | ❌ | ❌ | ✅ | One-time or scheduled batch tasks |

**Note**: All app types generate ConfigMap resources automatically. PDB (PodDisruptionBudget) generation is controlled by the `pdb.enabled` flag.

## 1. external-api

Public-facing HTTP APIs that need external ingress routing.

### What Gets Generated

- **Deployment**: Application pods with configurable replicas
- **Service**: ClusterIP service exposing the API
- **Ingress**: External ingress routing (1:1 per app)
- **PDB**: Pod disruption budget (if `pdb.enabled: true`)
- **ConfigMap**: Environment variables

### When to Use

- REST APIs that need to be accessible from outside the cluster
- GraphQL APIs with external clients
- Public-facing web services
- APIs that serve frontend applications

### Port Requirements

**REQUIRED**: Must define `port` in app configuration.

```python
# In BUILD.bazel
demo_app(
    name = "api_server",
    port = 8080,  # Required for external-api
    type = "external-api",
)
```

### Health Checks

External APIs should implement:
- **Readiness probe**: `/health/ready` or similar
- **Liveness probe**: `/health/live` or similar

The generated Deployment includes standard health checks:
```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8080
readinessProbe:
  httpGet:
    path: /health
    port: 8080
```

Customize via values:
```yaml
apps:
  api_server:
    livenessProbe:
      httpGet:
        path: /health/live
        port: 8080
    readinessProbe:
      httpGet:
        path: /health/ready
        port: 8080
```

### Ingress Routing (1:1 Pattern)

Each `external-api` app gets its own dedicated Ingress resource:

**Generated Structure**:
```yaml
# api_server-dev-ingress
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: api_server-dev-ingress
spec:
  rules:
  - host: api_server-dev.local
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: api_server-dev
            port:
              number: 8080
```

**Customization**:
```yaml
# values.yaml
global:
  ingress:
    enabled: true
    className: nginx
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
    tls:
      - secretName: api-tls
        hosts:
          - api_server-dev.local
```

### Example Configuration

```python
# BUILD.bazel
load("//tools:demo_app.bzl", "demo_app")

demo_app(
    name = "hello_fastapi",
    srcs = ["main.py"],
    deps = [
        "@pip//fastapi",
        "@pip//uvicorn",
    ],
    port = 8000,
    type = "external-api",
    visibility = ["//visibility:public"],
)

helm_chart(
    name = "fastapi_chart",
    app = ":hello_fastapi",
    environment = "dev",
)
```

**Generated values.yaml**:
```yaml
apps:
  hello_fastapi:
    enabled: true
    type: external-api
    image: "hello_fastapi:latest"
    replicas: 1
    port: 8000
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
```

### ArgoCD Sync-Wave

- **Sync-wave**: `0` (default application wave)
- **Dependencies**: Runs after Jobs (wave `-1`)

```yaml
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "0"
```

---

## 2. internal-api

Internal HTTP services for cluster-internal communication only.

### What Gets Generated

- **Deployment**: Application pods with configurable replicas
- **Service**: ClusterIP service for internal routing
- **PDB**: Pod disruption budget (if `pdb.enabled: true`)
- **ConfigMap**: Environment variables

**Not Generated**: No Ingress (cluster-internal only)

### When to Use

- Microservices that only need cluster-internal communication
- gRPC services between microservices
- Internal HTTP APIs for other services
- Backend services that don't need external access

### Port Requirements

**REQUIRED**: Must define `port` in app configuration.

```python
demo_app(
    name = "user_service",
    port = 9000,
    type = "internal-api",
)
```

### Service Discovery

Internal APIs are accessible via Kubernetes DNS:

```
<app-name>-<environment>.<namespace>.svc.cluster.local:<port>
```

**Example**:
```bash
# From another pod in the cluster
curl http://user_service-dev.default.svc.cluster.local:9000/users
```

Short form within same namespace:
```bash
curl http://user_service-dev:9000/users
```

### Health Checks

Similar to external-api, internal APIs should implement health endpoints:

```yaml
apps:
  user_service:
    livenessProbe:
      httpGet:
        path: /health
        port: 9000
    readinessProbe:
      httpGet:
        path: /health
        port: 9000
```

### Example Configuration

```python
# BUILD.bazel
demo_app(
    name = "user_service",
    srcs = ["main.py"],
    deps = [
        "@pip//fastapi",
        "@pip//uvicorn",
        "//libs/python:utils",
    ],
    port = 9000,
    type = "internal-api",
)

helm_chart(
    name = "user_service_chart",
    app = ":user_service",
    environment = "dev",
)
```

**Generated values.yaml**:
```yaml
apps:
  user_service:
    enabled: true
    type: internal-api
    image: "user_service:latest"
    replicas: 1
    port: 9000
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
```

### ArgoCD Sync-Wave

- **Sync-wave**: `0` (default application wave)
- **Dependencies**: Runs after Jobs (wave `-1`)

---

## 3. worker

Background processors that consume from queues or streams.

### What Gets Generated

- **Deployment**: Worker pods with configurable replicas
- **PDB**: Pod disruption budget (if `pdb.enabled: true`)
- **ConfigMap**: Environment variables

**Not Generated**: No Service (workers don't expose ports), No Ingress

### When to Use

- Message queue consumers (RabbitMQ, Kafka, SQS)
- Stream processors (Kafka Streams, Flink)
- Scheduled background tasks (periodic processing)
- Data pipeline workers
- Email/notification processors

### Port Requirements

**OPTIONAL**: Workers typically don't expose ports, but can define a port for metrics or health checks.

```python
# Worker without port
demo_app(
    name = "email_worker",
    type = "worker",
)

# Worker with metrics port
demo_app(
    name = "kafka_consumer",
    port = 9090,  # Prometheus metrics
    type = "worker",
)
```

### Health Checks

Workers can use exec-based health checks or TCP socket checks:

```yaml
apps:
  email_worker:
    livenessProbe:
      exec:
        command:
        - python
        - -c
        - "import sys; sys.exit(0)"
    readinessProbe:
      exec:
        command:
        - python
        - -c
        - "import sys; sys.exit(0)"
```

Or TCP checks if a port is defined:
```yaml
apps:
  kafka_consumer:
    livenessProbe:
      tcpSocket:
        port: 9090
```

### Scaling Considerations

Workers often benefit from horizontal scaling:

```yaml
apps:
  kafka_consumer:
    replicas: 5
    resources:
      requests:
        memory: "256Mi"
        cpu: "250m"
      limits:
        memory: "512Mi"
        cpu: "500m"
```

Consider using HPA (HorizontalPodAutoscaler) based on:
- Queue depth (RabbitMQ)
- Consumer lag (Kafka)
- Custom metrics (Prometheus)

### Example Configuration

```python
# BUILD.bazel
demo_app(
    name = "email_worker",
    srcs = ["worker.py"],
    deps = [
        "@pip//celery",
        "@pip//redis",
        "//libs/python:utils",
    ],
    type = "worker",
)

helm_chart(
    name = "email_worker_chart",
    app = ":email_worker",
    environment = "prod",
)
```

**Generated values.yaml**:
```yaml
apps:
  email_worker:
    enabled: true
    type: worker
    image: "email_worker:latest"
    replicas: 3
    resources:
      requests:
        memory: "128Mi"
        cpu: "100m"
      limits:
        memory: "256Mi"
        cpu: "200m"
```

### ArgoCD Sync-Wave

- **Sync-wave**: `0` (default application wave)
- **Dependencies**: Runs after Jobs (wave `-1`)

---

## 4. job

One-time or scheduled batch tasks that run to completion.

### What Gets Generated

- **Job**: Kubernetes Job with Helm hooks
- **ConfigMap**: Environment variables

**Not Generated**: No Deployment, No Service, No Ingress, No PDB

### When to Use

- Database migrations
- Schema updates
- Data backups
- One-time data imports
- Scheduled batch processing (use CronJob separately)
- Setup/initialization tasks

### Helm Hooks

Jobs use Helm hooks to run at specific lifecycle points:

**Default hooks** (automatically added):
```yaml
metadata:
  annotations:
    helm.sh/hook: pre-install,pre-upgrade
    helm.sh/hook-weight: "0"
    helm.sh/hook-delete-policy: before-hook-creation
    argocd.argoproj.io/sync-wave: "-1"
```

**Hook types**:
- `pre-install`: Runs before initial install
- `pre-upgrade`: Runs before chart upgrade
- `post-install`: Runs after initial install (customize via values)
- `post-upgrade`: Runs after upgrade (customize via values)

### Job Behavior

```yaml
spec:
  backoffLimit: 3       # Retry 3 times on failure
  ttlSecondsAfterFinished: 86400  # Delete after 24 hours
```

Customize via values:
```yaml
apps:
  db_migration:
    backoffLimit: 5
    ttlSecondsAfterFinished: 3600  # 1 hour
```

### Port Requirements

**NOT APPLICABLE**: Jobs don't expose ports.

### Example Configuration

```python
# BUILD.bazel
demo_app(
    name = "db_migration",
    srcs = ["migrate.py"],
    deps = [
        "@pip//alembic",
        "@pip//sqlalchemy",
        "//libs/python:utils",
    ],
    type = "job",
)

helm_chart(
    name = "db_migration_chart",
    app = ":db_migration",
    environment = "prod",
)
```

**Generated values.yaml**:
```yaml
apps:
  db_migration:
    enabled: true
    type: job
    image: "db_migration:latest"
    backoffLimit: 3
    restartPolicy: Never
    resources:
      requests:
        memory: "256Mi"
        cpu: "200m"
      limits:
        memory: "512Mi"
        cpu: "400m"
```

### ArgoCD Sync-Wave

- **Sync-wave**: `-1` (runs before applications)
- **Purpose**: Ensures migrations complete before apps start

### Disabling Jobs

Jobs (like migrations) can be disabled via the `enabled` flag in values:

```yaml
apps:
  db_migration:
    enabled: false  # Skip running this job
```

**Common use cases**:
- **Skip migrations in specific environments**: Disable in dev, enable in prod
- **One-time setup**: Disable job after initial setup is complete
- **Rollback scenarios**: Temporarily disable migrations during rollback
- **Testing**: Disable migrations when testing other services

**Example: Conditional migration deployment**
```bash
# Dev environment - skip migrations
cat > values-dev.yaml <<EOF
apps:
  db_migration:
    enabled: false
EOF
helm install myapp-dev ./chart -f values-dev.yaml

# Production - run migrations
helm install myapp-prod ./chart  # enabled: true by default
```

### CronJob Pattern

For scheduled jobs, define a CronJob separately:

```yaml
# Not automatically generated - add manually
apiVersion: batch/v1
kind: CronJob
metadata:
  name: daily-backup
spec:
  schedule: "0 2 * * *"  # 2am daily
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: backup
            image: db_migration:latest
            command: ["python", "backup.py"]
          restartPolicy: OnFailure
```

---

## App Type Selection Guide

### Decision Tree

```
Does your app need external HTTP access?
├─ Yes → external-api
└─ No
   ├─ Does it expose HTTP internally?
   │  └─ Yes → internal-api
   └─ No
      ├─ Does it run continuously?
      │  └─ Yes → worker
      └─ No → job
```

### Common Patterns

#### Pattern: Public API + Internal Service
```yaml
apps:
  api_gateway:
    type: external-api
    port: 8080
  user_service:
    type: internal-api
    port: 9000
```

#### Pattern: API + Workers + Migration
```yaml
apps:
  api_server:
    type: external-api
    port: 8000
  task_processor:
    type: worker
    replicas: 5
  db_migration:
    type: job
```

#### Pattern: Full Stack Application
```yaml
apps:
  frontend_api:
    type: external-api
    port: 8080
  backend_api:
    type: internal-api
    port: 9000
  cache_warmer:
    type: worker
  schema_update:
    type: job
```

---

## Resource Requirements by Type

### external-api (Recommended)
```yaml
resources:
  requests:
    memory: "256Mi"
    cpu: "200m"
  limits:
    memory: "512Mi"
    cpu: "500m"
```

### internal-api (Recommended)
```yaml
resources:
  requests:
    memory: "256Mi"
    cpu: "200m"
  limits:
    memory: "512Mi"
    cpu: "500m"
```

### worker (Recommended)
```yaml
resources:
  requests:
    memory: "512Mi"
    cpu: "500m"
  limits:
    memory: "1Gi"
    cpu: "1000m"
```

### job (Recommended)
```yaml
resources:
  requests:
    memory: "512Mi"
    cpu: "500m"
  limits:
    memory: "2Gi"
    cpu: "1000m"
```

**Note**: Adjust based on actual workload requirements.

### Language-Specific Optimizations

#### Python Applications

Python applications automatically receive optimized memory settings to improve resource efficiency:

```yaml
resources:
  requests:
    memory: "64Mi"   # Reduced from default
    cpu: "50m"       # Standard
  limits:
    memory: "256Mi"  # Reduced from default
    cpu: "100m"      # Standard
```

These optimizations are automatically applied when `language = "python"` is specified in the `release_app` configuration. The CPU settings remain at standard levels while memory is optimized for typical Python application footprints.

---

## Validation

### Check Generated Resources

```bash
# Template the chart
helm template my-app ./charts/my-app

# Check for expected resources
helm template my-app ./charts/my-app | grep -E "kind: (Deployment|Service|Ingress|Job)"
```

### Lint the Chart

```bash
helm lint ./charts/my-app
```

### Test Installation

```bash
helm install --dry-run --debug my-app ./charts/my-app
```

---

## See Also

- [README.md](README.md) - Quick start guide and common patterns
- [TEMPLATES.md](TEMPLATES.md) - Template development guide
- [MIGRATION.md](MIGRATION.md) - Migration guide from old patterns
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) - Full implementation details
