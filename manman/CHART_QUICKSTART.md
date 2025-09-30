# ManMan Helm Chart - Quick Reference

**Chart Name**: `manman-host`  
**Version**: `0.2.0`  
**Type**: Composed Chart (5 services)

---

## Quick Start

### Build the Chart

```bash
bazel build //manman:manman_chart
```

### Deploy to Development

```bash
helm install manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  --namespace manman \
  --create-namespace
```

### Upgrade Existing Deployment

```bash
# Rebuild chart first
bazel build //manman:manman_chart

# Upgrade
helm upgrade manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  --namespace manman
```

### View What Will Be Deployed

```bash
helm template manman bazel-bin/manman/manman-host_chart/manman-host
```

---

## Services Included

| Service | Type | Port | Ingress | Description |
|---------|------|------|---------|-------------|
| **experience_api** | external-api | 8000 | ✅ Yes | User-facing API for game server management |
| **worker_dal_api** | external-api | 8000 | ✅ Yes | Worker data access layer API |
| **status_api** | internal-api | 8000 | ❌ No | Internal status monitoring (cluster-only) |
| **status_processor** | worker | 8000 | ❌ No | Background status event processor |
| **migration** | job | - | ❌ No | Database migration (runs first) |

---

## Deployment Order (ArgoCD Sync Waves)

1. **Wave -1**: Migration job runs (pre-install/pre-upgrade hook)
2. **Wave 0**: All services and ingresses deploy

The migration job **must complete successfully** before services start.

---

## Service Endpoints

### External APIs (via Ingress)

```bash
# Experience API
http://experience_api-dev.local/

# Worker DAL API
http://worker_dal_api-dev.local/
```

### Internal APIs (cluster-only)

```bash
# Status API (from another pod)
curl http://status_api-dev-service.manman.svc.cluster.local:8000/health
```

---

## Common Operations

### Scale a Service

```bash
helm upgrade manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  --set apps.experience_api.replicas=5 \
  --set apps.status_processor.replicas=3
```

### Override Resource Limits

```bash
helm upgrade manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  --set apps.experience_api.resources.limits.memory=1Gi \
  --set apps.experience_api.resources.limits.cpu=1000m
```

### Check Deployment Status

```bash
# All resources
kubectl get all -n manman

# Deployments
kubectl get deployments -n manman

# Services
kubectl get services -n manman

# Ingresses
kubectl get ingress -n manman

# Jobs (migration)
kubectl get jobs -n manman
```

### View Logs

```bash
# Experience API
kubectl logs -n manman deployment/experience_api-dev -f

# Status Processor
kubectl logs -n manman deployment/status_processor-dev -f

# Migration Job
kubectl logs -n manman job/migration-dev
```

### Port Forward for Local Testing

```bash
# Experience API
kubectl port-forward -n manman deployment/experience_api-dev 8000:8000

# Status API (internal)
kubectl port-forward -n manman deployment/status_api-dev 8001:8000

# Then access locally:
curl http://localhost:8000/health  # Experience API
curl http://localhost:8001/health  # Status API
```

---

## Customization

### Using a Custom Values File

Create `custom-values.yaml`:

```yaml
apps:
  experience_api:
    replicas: 5
    resources:
      requests:
        memory: "512Mi"
        cpu: "500m"
      limits:
        memory: "1Gi"
        cpu: "1000m"
    env:
      - name: CUSTOM_ENV_VAR
        value: "custom_value"
  
  status_processor:
    replicas: 3
    resources:
      requests:
        memory: "256Mi"
        cpu: "250m"
```

Deploy with custom values:

```bash
helm install manman-dev \
  bazel-bin/manman/manman-host_chart/manman-host \
  -f custom-values.yaml
```

---

## Troubleshooting

### Migration Job Failed

```bash
# Check migration logs
kubectl logs -n manman job/migration-dev

# Delete failed job to retry
kubectl delete job -n manman migration-dev

# Retry upgrade
helm upgrade manman-dev bazel-bin/manman/manman-host_chart/manman-host
```

### Service Not Starting

```bash
# Check pod status
kubectl get pods -n manman

# View pod events
kubectl describe pod -n manman <pod-name>

# Check logs
kubectl logs -n manman <pod-name>
```

### Health Check Failing

```bash
# Port forward to test health endpoint directly
kubectl port-forward -n manman deployment/experience_api-dev 8000:8000

# Test health endpoint
curl http://localhost:8000/health

# Should return 200 OK
```

### Ingress Not Working

```bash
# Check ingress configuration
kubectl describe ingress -n manman experience_api-dev-ingress

# Verify ingress controller is running
kubectl get pods -n ingress-nginx  # or your ingress namespace

# Test service directly (bypass ingress)
kubectl port-forward -n manman service/experience_api-dev-service 8000:8000
curl http://localhost:8000/health
```

---

## Uninstalling

### Remove Deployment

```bash
helm uninstall manman-dev --namespace manman
```

### Delete Namespace

```bash
kubectl delete namespace manman
```

**⚠️ Warning**: This will delete all data including persistent volumes if any.

---

## Modifying the Chart

### Update Service Configuration

Edit `manman/BUILD.bazel` and rebuild:

```starlark
release_app(
    name = "experience_api",
    # ... update configuration here
)
```

```bash
# Rebuild chart
bazel build //manman:manman_chart

# Upgrade deployment
helm upgrade manman-dev bazel-bin/manman/manman-host_chart/manman-host
```

### Change Environment

The chart is built for `dev` environment by default. To change:

Edit `manman/BUILD.bazel`:

```starlark
helm_chart(
    name = "manman_chart",
    apps = [...],
    environment = "prod",  # Changed from "dev"
    chart_version = "0.2.0",
)
```

Rebuild and deploy.

---

## CI/CD Integration

### GitHub Actions Example

```yaml
- name: Build manman chart
  run: bazel build //manman:manman_chart

- name: Deploy to dev
  run: |
    helm upgrade --install manman-dev \
      bazel-bin/manman/manman-host_chart/manman-host \
      --namespace manman \
      --create-namespace \
      --wait
```

### ArgoCD Application

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: manman-dev
spec:
  project: default
  source:
    repoURL: https://github.com/whale-net/everything
    path: bazel-bin/manman/manman-host_chart/manman-host
    targetRevision: main
  destination:
    server: https://kubernetes.default.svc
    namespace: manman
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
```

---

## Resource Summary

### Default Resources (per service)

```yaml
Experience API, Status API, Worker DAL API:
  requests:
    cpu: 50m
    memory: 256Mi
  limits:
    cpu: 100m
    memory: 512Mi

Status Processor:
  requests:
    cpu: 50m
    memory: 256Mi
  limits:
    cpu: 100m
    memory: 512Mi

Migration Job:
  requests:
    cpu: 100m
    memory: 256Mi
  limits:
    cpu: 200m
    memory: 512Mi
```

### Default Replicas

- **experience_api**: 2
- **status_api**: 2
- **worker_dal_api**: 2
- **status_processor**: 2

---

## Health Checks

All services use standard health check configuration:

```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8000
  initialDelaySeconds: 10
  periodSeconds: 10
  timeoutSeconds: 5
  failureThreshold: 3

readinessProbe:
  httpGet:
    path: /health
    port: 8000
  initialDelaySeconds: 10
  periodSeconds: 10
  timeoutSeconds: 5
  failureThreshold: 3
```

**Note**: Services must implement a `/health` endpoint that returns 200 OK.

---

## References

- **Full Migration Guide**: `manman/CHART_MIGRATION.md`
- **Helm Tool Docs**: `tools/helm/README.md`
- **App Types**: `tools/helm/APP_TYPES.md`
- **Build File**: `manman/BUILD.bazel`
- **Manual Chart Backup**: `manman/__manual_backup_of_old_chart/`

---

## Support

For issues or questions:

1. Check the troubleshooting section above
2. Review full documentation in `manman/CHART_MIGRATION.md`
3. Consult helm tool documentation in `tools/helm/`
4. Review ArgoCD sync status if using ArgoCD
