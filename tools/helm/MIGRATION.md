# Migration Guide: Helm Chart System

This guide shows you how to migrate apps to use the new helm chart composition system.

## Quick Migration Checklist

- [ ] Add `type` attribute to app definition (BUILD.bazel)
- [ ] Add `helm_chart` rule for each app
- [ ] Build the chart with Bazel
- [ ] Validate with `helm lint` and `helm template`
- [ ] Test deployment in dev environment

---

## Migration Steps

### Step 1: Add App Type to Existing Apps

Update your app definitions in `BUILD.bazel` to include the `type` attribute.

**Before**:
```python
py_binary(
    name = "hello_python",
    srcs = ["main.py"],
    deps = [
        "//libs/python:utils",
        "@pip//fastapi",
    ],
)
```

**After**:
```python
demo_app(
    name = "hello_python",
    srcs = ["main.py"],
    deps = [
        "//libs/python:utils",
        "@pip//fastapi",
    ],
    port = 8000,           # Required for API types
    type = "external-api",  # NEW: Define app type
)
```

#### Choosing the Right Type

- **external-api**: Needs external HTTP access (REST, GraphQL)
- **internal-api**: HTTP service for cluster-internal use only
- **worker**: Background processor (queues, streams)
- **job**: One-time or pre-install task (migrations, setup)

See [APP_TYPES.md](APP_TYPES.md) for detailed guidance.

### Step 2: Add helm_chart Rule

Add a `helm_chart` rule after your app definition:

```python
load("//tools:demo_app.bzl", "demo_app")
load("//tools:helm.bzl", "helm_chart")  # NEW

demo_app(
    name = "hello_python",
    srcs = ["main.py"],
    deps = [
        "//libs/python:utils",
        "@pip//fastapi",
    ],
    port = 8000,
    type = "external-api",
)

# NEW: helm_chart rule
helm_chart(
    name = "hello_python_chart",
    app = ":hello_python",
    environment = "dev",
)
```

**Parameters**:
- `name`: Chart target name (convention: `{app}_chart`)
- `app`: Reference to app target (`:app_name`)
- `environment`: Target environment (`dev`, `staging`, `prod`)

### Step 3: Build the Chart

Build the chart with Bazel:

```bash
# Build specific chart
bazel build //demo/hello_python:hello_python_chart

# Find the generated chart
ls -la bazel-bin/demo/hello_python/hello_python_chart/

# Output structure:
# hello_python_chart/
#   Chart.yaml
#   values.yaml
#   templates/
#     deployment.yaml
#     service.yaml
#     ingress.yaml
#     configmap.yaml
#     pdb.yaml
```

### Step 4: Validate the Chart

Run Helm validation:

```bash
# Lint the chart
helm lint bazel-bin/demo/hello_python/hello_python_chart/

# Template to see generated YAML
helm template hello-python bazel-bin/demo/hello_python/hello_python_chart/

# Check for expected resources
helm template hello-python bazel-bin/demo/hello_python/hello_python_chart/ | grep "kind:"
```

**Expected output**:
```
kind: Deployment
kind: Service
kind: Ingress
kind: ConfigMap
kind: PodDisruptionBudget
```

### Step 5: Customize Values (Optional)

Override values at deployment time:

```bash
# Create custom values
cat > custom-values.yaml <<EOF
apps:
  hello_python:
    replicas: 3
    resources:
      requests:
        memory: "512Mi"
        cpu: "500m"
      limits:
        memory: "1Gi"
        cpu: "1000m"
EOF

# Install with custom values
helm install hello-python \
  bazel-bin/demo/hello_python/hello_python_chart/ \
  --values custom-values.yaml
```

### Step 6: Deploy

Install the chart:

```bash
# Development environment
helm install hello-python-dev \
  bazel-bin/demo/hello_python/hello_python_chart/

# Production environment (rebuild with prod environment first)
bazel build //demo/hello_python:hello_python_chart_prod
helm install hello-python-prod \
  bazel-bin/demo/hello_python/hello_python_chart_prod/
```

---

## Multi-App Charts

Combine multiple apps into a single chart.

### Step 1: Create Multi-App Chart Rule

```python
# BUILD.bazel
load("//tools:demo_app.bzl", "demo_app")
load("//tools:helm.bzl", "helm_chart")

# Define apps
demo_app(
    name = "api_server",
    srcs = ["api.py"],
    port = 8080,
    type = "external-api",
)

demo_app(
    name = "background_worker",
    srcs = ["worker.py"],
    type = "worker",
)

demo_app(
    name = "db_migration",
    srcs = ["migrate.py"],
    type = "job",
)

# Multi-app chart
helm_chart(
    name = "full_stack_chart",
    apps = [
        ":api_server",
        ":background_worker",
        ":db_migration",
    ],
    environment = "dev",
)
```

### Step 2: Build and Validate

```bash
# Build multi-app chart
bazel build //demo/full_stack:full_stack_chart

# Validate
helm lint bazel-bin/demo/full_stack/full_stack_chart/

# Check generated resources
helm template full-stack bazel-bin/demo/full_stack/full_stack_chart/ | grep "kind:" | sort | uniq -c
```

**Expected output**:
```
   1 kind: ConfigMap
   2 kind: Deployment      # api_server + background_worker
   1 kind: Ingress         # api_server only
   1 kind: Job             # db_migration only
   1 kind: Service         # api_server only
```

### Step 3: Deploy Multi-App Chart

```bash
helm install full-stack-dev \
  bazel-bin/demo/full_stack/full_stack_chart/
```

**Resource ordering (ArgoCD sync-waves)**:
1. Wave `-1`: Jobs (db_migration) - runs first
2. Wave `0`: Deployments, Services, Ingress (api_server, background_worker) - runs after jobs

---

## Common Migration Scenarios

### Scenario 1: Public API

**Old approach** (manual Kubernetes YAML):
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-server
---
apiVersion: v1
kind: Service
metadata:
  name: api-server
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: api-server
```

**New approach** (Bazel + Helm):
```python
# BUILD.bazel
demo_app(
    name = "api_server",
    srcs = ["main.py"],
    deps = ["@pip//fastapi"],
    port = 8080,
    type = "external-api",
)

helm_chart(
    name = "api_server_chart",
    app = ":api_server",
    environment = "prod",
)
```

```bash
# Build and deploy
bazel build //api:api_server_chart
helm install api-server bazel-bin/api/api_server_chart/
```

### Scenario 2: Background Worker

**Old approach**:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: email-worker
# No Service or Ingress needed
```

**New approach**:
```python
demo_app(
    name = "email_worker",
    srcs = ["worker.py"],
    deps = [
        "@pip//celery",
        "@pip//redis",
    ],
    type = "worker",  # No Service or Ingress generated
)

helm_chart(
    name = "email_worker_chart",
    app = ":email_worker",
    environment = "prod",
)
```

### Scenario 3: Database Migration Job

**Old approach** (Helm hook):
```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: db-migration
  annotations:
    helm.sh/hook: pre-install,pre-upgrade
```

**New approach**:
```python
demo_app(
    name = "db_migration",
    srcs = ["migrate.py"],
    deps = [
        "@pip//alembic",
        "@pip//sqlalchemy",
    ],
    type = "job",  # Automatically includes Helm hooks
)

helm_chart(
    name = "db_migration_chart",
    app = ":db_migration",
    environment = "prod",
)
```

---

## Ingress Migration (1:1 Pattern)

### Old Multi-Mode Ingress (DEPRECATED)

The old system supported "single" vs "per-app" ingress modes. **This is no longer supported**.

**Old values.yaml** (DEPRECATED):
```yaml
ingress:
  enabled: true
  mode: single  # REMOVED
  hosts:        # REMOVED
    - host: api.example.com
      paths:
        - path: /api
          service: api_server
        - path: /admin
          service: admin_api
```

### New 1:1 Ingress Pattern

Each `external-api` app gets its own dedicated Ingress resource.

**New approach**:
```python
# BUILD.bazel - Define two APIs
demo_app(
    name = "api_server",
    port = 8080,
    type = "external-api",
)

demo_app(
    name = "admin_api",
    port = 8081,
    type = "external-api",
)

# Multi-app chart
helm_chart(
    name = "apis_chart",
    apps = [
        ":api_server",
        ":admin_api",
    ],
    environment = "prod",
)
```

**Generated resources**:
```yaml
# api_server-prod-ingress
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: api_server-prod-ingress
spec:
  rules:
  - host: api_server-prod.local
    http:
      paths:
      - path: /
        backend:
          service:
            name: api_server-prod
---
# admin_api-prod-ingress
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: admin_api-prod-ingress
spec:
  rules:
  - host: admin_api-prod.local
    http:
      paths:
      - path: /
        backend:
          service:
            name: admin_api-prod
```

**Benefits**:
- Simpler configuration (no mode selection)
- Easier to reason about (1 app = 1 ingress)
- Independent routing rules per app
- Better separation of concerns

---

## Validation Checklist

After migration, verify:

### 1. Bazel Build
```bash
bazel build //your/app:app_chart
# Expected: Success with no errors
```

### 2. Chart Structure
```bash
tree bazel-bin/your/app/app_chart/
# Expected:
# app_chart/
#   Chart.yaml
#   values.yaml
#   templates/
#     *.yaml
```

### 3. Helm Lint
```bash
helm lint bazel-bin/your/app/app_chart/
# Expected: "0 chart(s) linted, 0 chart(s) failed"
```

### 4. Template Output
```bash
helm template test bazel-bin/your/app/app_chart/ | kubectl apply --dry-run=client -f -
# Expected: All resources valid
```

### 5. Resource Types
```bash
helm template test bazel-bin/your/app/app_chart/ | grep "kind:" | sort | uniq
# Expected: Correct resource types for your app type
```

### 6. Values Schema
```bash
helm template test bazel-bin/your/app/app_chart/ --values custom-values.yaml
# Expected: Custom values applied correctly
```

---

## Troubleshooting

### Issue: "No such attribute 'type'"

**Error**:
```
Error: in demo_app rule //demo/hello_python:hello_python: 
  no such attribute 'type'
```

**Solution**: Update your app definition to use the `demo_app` rule with `type` attribute.

```python
# Change from:
py_binary(name = "app", srcs = ["main.py"])

# To:
demo_app(name = "app", srcs = ["main.py"], type = "external-api")
```

### Issue: "Port required for external-api"

**Error**:
```
Error: external-api type requires 'port' attribute
```

**Solution**: Add `port` to your app definition:

```python
demo_app(
    name = "api",
    srcs = ["main.py"],
    port = 8080,  # Required for API types
    type = "external-api",
)
```

### Issue: "Chart not found"

**Error**:
```
Error: chart bazel-bin/demo/app/app_chart/ not found
```

**Solution**: Build the chart first with Bazel:

```bash
bazel build //demo/app:app_chart
```

### Issue: "Ingress not generated"

**Problem**: Expected Ingress resource not in template output.

**Solution**: Check app type and ingress config:

1. Verify app is `external-api`:
   ```python
   demo_app(
       name = "api",
       type = "external-api",  # Must be external-api
   )
   ```

2. Check ingress is enabled in values:
   ```yaml
   global:
     ingress:
       enabled: true  # Must be true
   ```

3. Re-template and check:
   ```bash
   helm template test ./chart/ | grep -A 20 "kind: Ingress"
   ```

### Issue: "Job doesn't run before Deployment"

**Problem**: Migration Job runs after application Deployment.

**Solution**: Verify ArgoCD sync-wave annotations:

```bash
# Check Job annotation
helm template test ./chart/ | grep -A 5 "kind: Job"
# Expected: argocd.argoproj.io/sync-wave: "-1"

# Check Deployment annotation
helm template test ./chart/ | grep -A 5 "kind: Deployment"
# Expected: argocd.argoproj.io/sync-wave: "0"
```

Jobs (wave `-1`) run before Deployments (wave `0`).

---

## Migration Testing Strategy

### 1. Build Time Testing

```bash
# Build all charts
bazel build //...

# Check for build errors
echo $?  # Should be 0
```

### 2. Helm Validation

```bash
# Lint all generated charts
find bazel-bin -name "Chart.yaml" -exec dirname {} \; | \
  xargs -I {} helm lint {}

# Expected: All charts pass
```

### 3. Template Testing

```bash
# Generate all YAML
find bazel-bin -name "Chart.yaml" -exec dirname {} \; | \
  xargs -I {} helm template test {} > /tmp/all-templates.yaml

# Validate with kubectl
kubectl apply --dry-run=client -f /tmp/all-templates.yaml
```

### 4. Development Environment Testing

```bash
# Deploy to dev cluster
helm install test-app bazel-bin/demo/app/app_chart/ \
  --namespace dev \
  --create-namespace

# Verify deployment
kubectl get all -n dev

# Test app functionality
kubectl port-forward -n dev svc/app-dev 8080:8080
curl http://localhost:8080/health
```

### 5. Cleanup

```bash
helm uninstall test-app -n dev
kubectl delete namespace dev
```

---

## Next Steps

After successful migration:

1. **Update CI/CD**: Integrate Bazel chart builds into your pipelines
2. **Document custom values**: Create values documentation for your team
3. **Setup ArgoCD**: Configure ArgoCD to deploy from generated charts
4. **Monitor deployments**: Setup monitoring and alerting for new charts

---

## See Also

- [README.md](README.md) - Quick start guide and overview
- [APP_TYPES.md](APP_TYPES.md) - Complete app type reference
- [TEMPLATES.md](TEMPLATES.md) - Template development guide
- [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) - Full implementation details
