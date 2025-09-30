# Ingress Simplification - September 29, 2025

## Overview
Simplified the Helm chart composer by removing confusing multi-app ingress configuration. The new approach follows the principle: **one app per chart, ingress configuration is consumer-defined**.

## Changes Made

### 1. Removed Build-Time Ingress Configuration ✅

**Removed from ChartConfig:**
- `IngressHost` field (hostname should be set by chart consumers)
- `IngressMode` field (single vs per-service was confusing)

**Before:**
```go
type ChartConfig struct {
    ChartName   string
    Version     string
    Environment string
    Namespace   string
    IngressHost string  // ❌ Removed
    IngressMode string  // ❌ Removed
    OutputDir   string
}
```

**After:**
```go
type ChartConfig struct {
    ChartName   string
    Version     string
    Environment string
    Namespace   string
    OutputDir   string
}
```

### 2. Simplified IngressConfig Structure ✅

**Before:**
```go
type IngressConfig struct {
    Enabled     bool
    Mode        string  // "single" or "per-app" - ❌ Removed
    Host        string  // ❌ Removed
    ClassName   string
    Annotations map[string]string
    TLS         []IngressTLSConfig
}
```

**After:**
```go
type IngressConfig struct {
    Enabled     bool               // Auto-set based on external-api presence
    ClassName   string             // Optional, set by consumer
    Annotations map[string]string  // Optional, set by consumer
    TLS         []IngressTLSConfig // Optional, set by consumer
}
```

### 3. Updated CLI ✅

**Removed flags:**
- `--ingress-host`
- `--ingress-mode`

**Remaining flags:**
```bash
--metadata       # Comma-separated metadata files (required)
--chart-name     # Chart name
--version        # Chart version
--environment    # Target environment
--namespace      # Kubernetes namespace
--output         # Output directory
--template-dir   # Template directory
```

### 4. Updated Bazel Rule ✅

**Removed attributes:**
- `ingress_host`
- `ingress_mode`

**Before:**
```starlark
helm_chart(
    name = "demo_chart",
    apps = [...],
    chart_name = "demo-apps",
    namespace = "demo",
    ingress_host = "demo.example.com",  # ❌ Removed
    ingress_mode = "single",             # ❌ Removed
)
```

**After:**
```starlark
helm_chart(
    name = "fastapi_chart",
    apps = ["//demo/hello_fastapi:hello_fastapi_metadata"],
    chart_name = "hello-fastapi",
    namespace = "demo",
)
```

### 5. Simplified values.yaml Output ✅

**Before:**
```yaml
ingress:
  enabled: true
  mode: single           # ❌ Removed
  host: api.example.com  # ❌ Removed
  className: nginx
```

**After:**
```yaml
ingress:
  enabled: true  # Auto-set based on app types
  # Host, paths, and other config set by chart consumer
```

## Philosophy

### Old Approach (Removed)
- ❌ Build-time ingress configuration
- ❌ Multi-app charts with complex routing
- ❌ Mode selection (single vs per-service)
- ❌ Hostname baked into chart at build time

### New Approach (Current)
- ✅ One app per chart (simpler mental model)
- ✅ Ingress enabled automatically for external-api types
- ✅ All ingress details configured by consumer via values.yaml
- ✅ Chart is environment-agnostic

## Consumer Configuration

Chart consumers now configure ingress details in their values.yaml overrides:

```yaml
# values-production.yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: api.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: api-tls
      hosts:
        - api.example.com
```

## Benefits

### Simplicity
- **Removed 2 configuration fields** from ChartConfig
- **Removed 2 CLI flags**
- **Removed 2 Bazel attributes**
- **Simpler mental model**: one app → one chart → ingress enabled if external-api

### Flexibility
- Chart consumers have full control over ingress configuration
- No need to rebuild charts for different environments
- Same chart works in dev, staging, production with different values

### Clarity
- No confusion about "single" vs "per-service" ingress modes
- No questions about what ingress_host does
- Clear separation: build-time (chart structure) vs deploy-time (ingress config)

### Best Practices
- Follows Helm best practices: charts are templates, values are environment-specific
- Enables proper GitOps workflows with values files per environment
- Charts can be published to registries and reused

## Updated Examples

### Example 1: Single External API
```starlark
helm_chart(
    name = "api_chart",
    apps = ["//api/users:users_metadata"],
    chart_name = "users-api",
    namespace = "production",
    chart_version = "1.0.0",
)
```

Deploy with:
```bash
helm install users-api ./chart \
  -f values-production.yaml \
  --set ingress.hosts[0].host=users.api.example.com
```

### Example 2: Workers Only
```starlark
helm_chart(
    name = "workers_chart",
    apps = [
        "//workers/email:email_metadata",
        "//workers/reports:reports_metadata",
    ],
    chart_name = "background-workers",
    namespace = "workers",
)
```

No ingress configuration needed - `ingress.enabled` will be `false` automatically.

## Migration Guide

### For Existing Charts

**Old:**
```starlark
helm_chart(
    name = "my_chart",
    apps = ["//app:metadata"],
    chart_name = "my-app",
    namespace = "prod",
    ingress_host = "api.example.com",
    ingress_mode = "single",
)
```

**New:**
```starlark
helm_chart(
    name = "my_chart",
    apps = ["//app:metadata"],
    chart_name = "my-app",
    namespace = "prod",
)
```

**Values file (new):**
```yaml
# values-prod.yaml
ingress:
  hosts:
    - host: api.example.com
      paths:
        - path: /
          pathType: Prefix
```

### For Multi-App Scenarios

**Old approach:** One chart with multiple apps, complex routing
```starlark
# ❌ Don't do this anymore
helm_chart(
    name = "platform",
    apps = ["//api1:meta", "//api2:meta", "//api3:meta"],
    chart_name = "platform",
    ingress_mode = "single",
)
```

**New approach:** Separate charts or use helm_chart_composed
```starlark
# ✅ Option 1: Separate charts per service
helm_chart(name = "api1_chart", apps = ["//api1:meta"], ...)
helm_chart(name = "api2_chart", apps = ["//api2:meta"], ...)

# ✅ Option 2: Use helm_chart_composed for complex scenarios
helm_chart_composed(
    name = "platform",
    apps = [...],
    k8s_artifacts = [...],  # Manual Ingress definitions
)
```

## Test Results

### Unit Tests ✅
```
$ bazel test //tools/helm:composer_test
PASSED - All 12 tests passing
```

### Integration Tests ✅
```
$ bazel test //tools/helm:integration_test
PASSED
- Helm lint: ✓
- Chart structure: ✓
- Template rendering: ✓
```

### Generated Output ✅
```yaml
# bazel-bin/demo/hello-fastapi_chart/hello-fastapi/values.yaml
global:
  namespace: demo
  environment: production

apps:
  hello_fastapi:
    image: ghcr.io/demo-hello_fastapi
    imageTag: latest
    port: 8000
    replicas: 2
    resources: {...}
    healthCheck: {...}

ingress:
  enabled: true  # Clean, simple, consumer-configurable
```

## Code Impact

### Files Modified
1. `tools/helm/composer.go` - Removed IngressMode/IngressHost from ChartConfig and IngressConfig
2. `tools/helm/cmd/helm_composer/main.go` - Removed CLI flags
3. `tools/helm/helm.bzl` - Removed Bazel attributes
4. `demo/BUILD.bazel` - Updated examples to remove ingress config
5. `tools/helm/composer_test.go` - Updated tests
6. `tools/helm/test_integration.sh` - Updated to use single-app chart
7. `tools/helm/BUILD.bazel` - Updated integration test data

### Lines Changed
- **Removed**: ~50 lines of ingress configuration code
- **Simplified**: ~30 lines of YAML generation code
- **Updated**: ~40 lines of tests and examples
- **Net impact**: Cleaner, simpler codebase

## Future Considerations

### When You Need Multi-App Charts
Use `helm_chart_composed` from `helm_composition_simple.bzl`:
- Supports manual Kubernetes artifacts
- Allows custom Ingress definitions
- Designed for complex multi-service deployments

### When You Need Custom Routing
Define Ingress manifests manually and include via `k8s_artifacts`:
```starlark
k8s_artifact(
    name = "custom_ingress",
    manifest = "k8s/ingress.yaml",
)

helm_chart_composed(
    name = "platform",
    apps = [...],
    k8s_artifacts = [":custom_ingress"],
)
```

## Conclusion

This simplification removes a confusing abstraction and aligns with Helm best practices:
- ✅ Charts are templates
- ✅ Values are environment-specific
- ✅ Build-time vs deploy-time concerns properly separated
- ✅ Simpler mental model: one app, one chart
- ✅ Full flexibility for consumers

The helm_chart rule is now focused on what it does best: generating structured Helm charts from app metadata. Everything else is left to the chart consumer, where it belongs.
