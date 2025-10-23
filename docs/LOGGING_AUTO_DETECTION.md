# Logging Auto-Detection Implementation

## Overview

The consolidated logging library (`//libs/python/logging`) now supports complete auto-detection of application metadata from environment variables. This eliminates the need to hardcode service names, versions, and other metadata in application code.

## Implementation Status

### ✅ Phase 1: Logging Library with Auto-Detection (COMPLETE)

**Files Modified:**
- `libs/python/logging/config.py` - Added `LogContext.from_environment()` auto-detection
- `libs/python/logging/context.py` - Maps 40+ environment variables to context fields
- `libs/python/logging/otel_handler.py` - OTLP handler with semantic conventions

**Key Features:**
- `configure_logging()` with all optional parameters
- Auto-detects from: APP_NAME, APP_DOMAIN, APP_TYPE, APP_VERSION, APP_ENV, GIT_COMMIT, POD_*, etc.
- Backward compatible with legacy hardcoded parameters
- OTLP-first architecture with semantic conventions

### ✅ Phase 2: Release Metadata Export (COMPLETE)

**Files Modified:**
- `tools/helm/composer.go` - Added Domain and CommitSha fields to AppConfig
- `tools/helm/templates/base/values.yaml.tmpl` - Exports domain and commitSha to YAML

**Implementation:**
```go
// AppConfig struct
type AppConfig struct {
    Name        string
    Domain      string      // NEW: Domain from metadata
    CommitSha   string      // NEW: Git commit (future)
    // ... other fields
}

// buildAppConfig populates from metadata
config.Domain = app.Domain

// writeValuesYAML conditionally writes fields
if config.Domain != "" {
    yamlWriter.WriteStringField("domain", config.Domain, 1)
}
```

**Verification:**
```bash
$ bazel build //manman:manman_chart
$ cat bazel-bin/manman/manman_chart/values.yaml
# Shows: domain: manman for each app
```

### ✅ Phase 3: Helm Templates Inject Environment Variables (COMPLETE)

**Files Modified:**
- `tools/helm/templates/deployment.yaml.tmpl` - Injects 50+ environment variables
- `tools/helm/templates/job.yaml.tmpl` - Same env var injection for jobs
- `tools/helm/templates/base/values.yaml.tmpl` - Added otlp configuration

**Environment Variables Injected:**

| Category | Variables | Source |
|----------|-----------|--------|
| App Metadata | APP_NAME, APP_DOMAIN, APP_TYPE | Values from metadata |
| Versioning | APP_VERSION, GIT_COMMIT | Values from Helm |
| Environment | APP_ENV, ENVIRONMENT | Global environment setting |
| Kubernetes | POD_NAME, POD_NAMESPACE, NODE_NAME | Downward API |
| Container | CONTAINER_NAME | Deployment template |
| Helm | HELM_CHART_NAME, HELM_RELEASE_NAME | Chart metadata |
| OTLP | OTEL_EXPORTER_OTLP_ENDPOINT | Values.otlp.endpoint |

**Example from deployment.yaml.tmpl:**
```yaml
env:
  # Application metadata
  - name: APP_NAME
    value: "{{ .Values.appName }}"
  - name: APP_DOMAIN
    value: "{{ .Values.domain }}"
  - name: APP_TYPE
    value: "{{ .Values.appType }}"
  
  # Kubernetes downward API
  - name: POD_NAME
    valueFrom:
      fieldRef:
        fieldPath: metadata.name
  
  # OTLP configuration
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "{{ .Values.otlp.endpoint | default \"http://localhost:4317\" }}"
```

### ✅ Phase 4: OCI Image Build Bakes Default Values (COMPLETE)

**Files Modified:**
- `tools/bazel/release.bzl` - Passes env dict to multiplatform_image
- `tools/bazel/container_image.bzl` - Accepts env parameter

**Implementation:**
```python
# tools/bazel/release.bzl
def release_app(name, domain, app_type, ...):
    # Create default env vars for image
    default_env = {
        "APP_NAME": name,
        "APP_DOMAIN": domain,
        "APP_TYPE": app_type,
    }
    
    multiplatform_image(
        name = image_target,
        binary = base_label,
        env = default_env,  # Bake defaults into image
        ...
    )
```

**Verification:**
```bash
$ bazel build //demo/hello_fastapi:hello-fastapi_image_base --platforms=//tools:linux_arm64
$ cat bazel-bin/demo/hello_fastapi/hello-fastapi_image_base.env.txt
APP_NAME=hello-fastapi
APP_DOMAIN=demo
APP_TYPE=external-api
```

**Benefits:**
- Default metadata baked into image at build time
- Works even outside Kubernetes (local Docker testing)
- Kubernetes can override with more specific values (version, commit, pod info)
- All 46 tests passing

### ✅ Phase 5: Simplified Application Code (COMPLETE)

**Files Modified:**
- `demo/hello_logging/main.py` - Zero-config example

**Before (Manual Configuration):**
```python
configure_logging(
    app_name="hello-logging",
    domain="demo",
    app_type="worker",
    environment="development",
    version="v1.0.0",
    log_level="DEBUG",
    enable_otlp=True,
    commit_sha="abc123def456",
    platform="linux/arm64",
)
```

**After (Auto-Detection):**
```python
# ZERO-CONFIG: Everything auto-detected from environment!
configure_logging(
    # Only override what you need:
    log_level="DEBUG",
    enable_console=True,  # For local development
)
```

**Demo Output:**
```bash
$ APP_NAME=hello-logging APP_DOMAIN=demo APP_TYPE=worker \
  bazel run //demo/hello_logging:hello_logging

2025-10-23 14:02:53,824 - [hello-logging] __main__ - INFO - Info message
# ... logs with auto-detected context sent to OTLP
```

## Architecture Flow

```
┌─────────────────┐
│  release_app    │  1. Define metadata (name, domain, type)
│   (BUILD.bazel) │
└────────┬────────┘
         │
         ├──────────────────────────────────────┐
         │                                      │
         v                                      v
┌─────────────────┐                   ┌─────────────────┐
│  Container      │  2. Bake defaults │  Helm Chart     │  3. Generate values
│  Image Build    │     into image    │  Composer       │     from metadata
│                 │                   │                 │
│  APP_NAME=...   │                   │  domain: demo   │
│  APP_DOMAIN=... │                   │  appType: api   │
└─────────────────┘                   └────────┬────────┘
                                               │
                                               v
                                      ┌─────────────────┐
                                      │  Deployment     │  4. Inject env vars
                                      │  Template       │     from values
                                      │                 │
                                      │  APP_VERSION    │
                                      │  GIT_COMMIT     │
                                      │  POD_NAME (K8s) │
                                      └────────┬────────┘
                                               │
                                               v
                                      ┌─────────────────┐
                                      │  Container      │  5. Runtime reads
                                      │  Runtime        │     all env vars
                                      │                 │
                                      │ configure_      │
                                      │   logging()     │  6. Auto-detect
                                      │                 │     from env
                                      └─────────────────┘
```

## Environment Variable Layers

The system uses **layered environment variables** for progressive enhancement:

### Layer 1: Container Image (Build-time defaults)
- **APP_NAME**: From release_app name
- **APP_DOMAIN**: From release_app domain
- **APP_TYPE**: From release_app app_type
- **Source**: `tools/bazel/release.bzl` → `container_image.bzl`
- **When**: Image build time (Bazel)
- **Purpose**: Ensure metadata exists even outside Kubernetes

### Layer 2: Helm Chart Values (Deployment-time specifics)
- **APP_VERSION**: From Helm chart version or CLI override
- **APP_ENV**: From global.environment (dev/staging/prod)
- **GIT_COMMIT**: From build metadata (future)
- **OTLP_ENDPOINT**: From values.otlp.endpoint
- **Source**: `tools/helm/composer.go` → `values.yaml.tmpl`
- **When**: Helm chart generation
- **Purpose**: Add version, environment, OTLP config

### Layer 3: Kubernetes Runtime (Pod-specific context)
- **POD_NAME**: From metadata.name (downward API)
- **POD_NAMESPACE**: From metadata.namespace
- **NODE_NAME**: From spec.nodeName
- **HELM_RELEASE_NAME**: From Helm release
- **Source**: Kubernetes downward API in `deployment.yaml.tmpl`
- **When**: Pod creation
- **Purpose**: Add runtime Kubernetes context

### Layer 4: Application Overrides (Optional)
- **log_level**: Override via CLI flag or code
- **enable_otlp**: Override via --log-otlp flag
- **enable_console**: For local development
- **Source**: Application code or CLI parameters
- **When**: Runtime (configure_logging call)
- **Purpose**: Developer-specific overrides

## Usage Patterns

### Pattern 1: Zero-Config (Recommended)
```python
from libs.python.logging import configure_logging

# Everything auto-detected!
configure_logging()
```

**When to use:**
- Production deployments in Kubernetes
- Standard applications with no special requirements
- Automated CI/CD pipelines

### Pattern 2: Minimal Config (Local Development)
```python
configure_logging(
    log_level="DEBUG",
    enable_console=True,
    json_format=False,
)
```

**When to use:**
- Local development with detailed logs
- Debugging specific issues
- Console-only workers (manman-worker)

### Pattern 3: CLI Flag Override (Decorator Pattern)
```python
from libs.python.logging.cli import logging_params

@click.command()
@logging_params  # Adds --log-otlp, --log-level, etc.
def main(log_config: dict):
    configure_logging(**log_config)
```

**When to use:**
- CLI applications with user control
- External workers that might not have OTLP
- Testing different logging configurations

### Pattern 4: Full Override (Legacy/Special Cases)
```python
configure_logging(
    app_name="custom-name",
    version="v2.0.0",
    environment="test",
    enable_otlp=False,
)
```

**When to use:**
- Legacy code migration
- Testing with mock metadata
- Special cases requiring full control

## Testing

### Unit Tests
All tests passing (46/46):
```bash
$ bazel test //...
//demo/hello_logging:test_main                  PASSED
//libs/python/logging:test_config               PASSED
# ... 44 more tests
```

### Integration Tests
```bash
# Test image build with env vars
$ bazel build //demo/hello_fastapi:hello-fastapi_image_base --platforms=//tools:linux_arm64
$ cat bazel-bin/demo/hello_fastapi/hello-fastapi_image_base.env.txt
APP_NAME=hello-fastapi
APP_DOMAIN=demo
APP_TYPE=external-api

# Test Helm chart generation
$ bazel build //manman:manman_chart
$ grep domain bazel-bin/manman/manman_chart/values.yaml
    domain: manman

# Test runtime with env vars
$ APP_NAME=test APP_DOMAIN=demo bazel run //demo/hello_logging:hello_logging
# Logs show auto-detected metadata
```

## Migration Guide

### For Existing Applications

**Step 1:** Add `logging_params` decorator (if CLI app):
```python
from libs.python.logging.cli import logging_params

@click.command()
@logging_params
def main(log_config: dict):
    configure_logging(**log_config)
```

**Step 2:** Remove hardcoded metadata:
```python
# BEFORE
configure_logging(
    app_name="my-app",
    domain="api",
    version="v1.0.0",
    ...
)

# AFTER
configure_logging()  # Auto-detects everything!
```

**Step 3:** Test locally with env vars:
```bash
APP_NAME=my-app APP_DOMAIN=api bazel run //path/to:my-app
```

**Step 4:** Deploy to Kubernetes (env vars auto-injected by Helm)

### For New Applications

Just use zero-config from the start:
```python
from libs.python.logging import configure_logging, get_logger

configure_logging()
logger = get_logger(__name__)
```

## Benefits

1. **No Hardcoding**: Apps don't need to know their own name/version
2. **Single Source of Truth**: `release_app` metadata flows everywhere
3. **Consistent Metadata**: Same values in logs, metrics, traces
4. **Environment-Specific**: Different values for dev/staging/prod automatically
5. **Kubernetes Native**: Leverages downward API for pod context
6. **Build-Time Safety**: Bazel validates metadata at build time
7. **Progressive Enhancement**: Works even outside Kubernetes with defaults
8. **Backward Compatible**: Legacy hardcoded configs still work

## Future Enhancements

### Add Commit SHA to Metadata
- Update `tools/bazel/release.bzl` to include git commit
- Propagate through Helm to GIT_COMMIT env var
- Auto-detect in LogContext.from_environment()

### Add Resource Labels
- Container resource requests/limits
- Kubernetes resource metadata (labels, annotations)
- Cloud provider metadata (region, zone)

### Add Observability Correlation
- Automatic trace context propagation
- Log-trace-metric correlation
- Distributed tracing integration

## Related Documentation

- [LOGGING_ENV_VARS.md](LOGGING_ENV_VARS.md) - Environment variable reference
- [AGENTS.md](../AGENTS.md) - Agent behavioral guidelines
- [tools/helm/README.md](../tools/helm/README.md) - Helm chart system
- [libs/python/logging/README.md](../libs/python/logging/README.md) - Logging library docs
