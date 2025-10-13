# Argo CD Integration

This Helm chart includes optional Argo CD specific configurations to ensure proper deployment ordering and compatibility.

## Configuration Options

### Basic Argo CD Support

To enable basic Argo CD compatibility:

```yaml
argocd:
  enabled: true
```

This adds Argo CD specific annotations for better resource management.

### Sync Waves (Recommended)

For proper migration ordering in Argo CD, use sync waves instead of Helm hooks:

```yaml
argocd:
  enabled: true
  useSyncWaves: true
  migrationSyncWave: -10
```

When `useSyncWaves` is enabled, Helm hooks are automatically skipped to prevent conflicts.

## Deployment Order

When `useSyncWaves: true`:

1. **Sync Wave -10**: Migration job runs first
2. **Sync Wave 0**: Application deployments (Slack bot and worker) start

## Argo CD Application Example

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: friendly-computing-machine
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://your-repo.git
    targetRevision: HEAD
    path: charts/friendly-computing-machine
    helm:
      valueFiles:
        - values-argocd.yaml
  destination:
    server: https://kubernetes.default.svc
    namespace: fcm
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true
```

## Usage Patterns

### Local Development (Helm)
```yaml
# Uses Helm hooks for migration ordering
argocd:
  enabled: false
```

### Argo CD Production (Sync Waves)
```yaml
# Uses Argo CD sync waves (automatically skips Helm hooks)
argocd:
  enabled: true
  useSyncWaves: true
```

### Argo CD with Legacy Helm Hooks
```yaml
# Basic Argo CD compatibility with Helm hooks
argocd:
  enabled: true
  useSyncWaves: false
```

## Benefits

- **Mutually exclusive modes**: Either Helm hooks (local) or sync waves (Argo CD)
- **Clean separation**: Migration job only needs database access, not Slack tokens
- **Proper ordering**: Migrations always run before applications
- **Clean rollbacks**: Argo CD can properly track and rollback migrations
- **No hook conflicts**: Sync waves automatically skip Helm hooks
- **Better observability**: Clear sync phases in Argo CD UI
