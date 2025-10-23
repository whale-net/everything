# Logging Environment Variables

## Overview

The consolidated logging library (`//libs/python/logging`) automatically detects application metadata from environment variables. This eliminates the need to hardcode service names, versions, and other metadata in application code.

## Environment Variables

### Core Application Metadata

These should be set by the `release_app` macro and Helm charts:

| Variable | Description | Example | Set By |
|----------|-------------|---------|--------|
| `APP_NAME` | Application name | `hello-fastapi` | `release_app` → Helm chart |
| `APP_VERSION` | Application version | `v1.2.3` | `release_app` → Helm chart |
| `APP_DOMAIN` | Application domain | `demo`, `api` | `release_app` → Helm chart |
| `APP_TYPE` | Application type | `external-api`, `worker` | `release_app` → Helm chart |
| `APP_ENV` / `ENVIRONMENT` | Deployment environment | `dev`, `staging`, `prod` | Helm values |
| `GIT_COMMIT` / `COMMIT_SHA` | Git commit SHA | `abc123def...` | Build system → Helm |

### Kubernetes Context (Downward API)

These are auto-populated by Kubernetes downward API in pod specs:

| Variable | Description | Downward API Field |
|----------|-------------|--------------------|
| `POD_NAME` | Kubernetes pod name | `metadata.name` |
| `POD_NAMESPACE` / `NAMESPACE` | Kubernetes namespace | `metadata.namespace` |
| `NODE_NAME` | Kubernetes node name | `spec.nodeName` |
| `CONTAINER_NAME` | Container name | Set in chart |

### Helm Context

These can be set by Helm charts:

| Variable | Description | Helm Template |
|----------|-------------|---------------|
| `HELM_CHART_NAME` | Helm chart name | `{{ .Chart.Name }}` |
| `HELM_RELEASE_NAME` | Helm release name | `{{ .Release.Name }}` |

### OTLP Configuration

| Variable | Description | Default |
|----------|-------------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP collector endpoint | `http://localhost:4317` |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | OTLP logs endpoint | Falls back to general endpoint |

## Implementation Plan

### 1. Update `release_app` Macro

The `release_app` macro should export metadata as part of the app definition:

```python
# tools/release.bzl
def release_app(name, binary_target, language, domain, **kwargs):
    # ... existing code ...
    
    # Export metadata as JSON for Helm charts to consume
    native.genrule(
        name = name + "_metadata_env",
        srcs = [":" + name + "_metadata"],
        outs = [name + "_metadata.env"],
        cmd = """
        cat $(location :{}_metadata) | jq -r '
            "APP_NAME=" + .name,
            "APP_DOMAIN=" + .domain,
            "APP_TYPE=" + (.type // "external-api"),
            "APP_VERSION=" + (.version // "latest")
        ' > $@
        """.format(name),
    )
```

### 2. Update Helm Templates

Helm deployment templates should inject environment variables:

```yaml
# deployment.yaml.tmpl
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: {{ .Values.appName }}
        image: {{ .Values.image }}
        env:
        # Application metadata
        - name: APP_NAME
          value: "{{ .Values.appName }}"
        - name: APP_DOMAIN
          value: "{{ .Values.domain }}"
        - name: APP_TYPE
          value: "{{ .Values.appType }}"
        - name: APP_VERSION
          value: "{{ .Values.version }}"
        - name: APP_ENV
          value: "{{ .Values.global.environment }}"
        - name: GIT_COMMIT
          value: "{{ .Values.commitSha }}"
        
        # Kubernetes downward API
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        - name: CONTAINER_NAME
          value: "{{ .Values.appName }}"
        
        # Helm context
        - name: HELM_CHART_NAME
          value: "{{ .Chart.Name }}"
        - name: HELM_RELEASE_NAME
          value: "{{ .Release.Name }}"
        
        # OTLP configuration
        - name: OTEL_EXPORTER_OTLP_ENDPOINT
          value: "{{ .Values.otlp.endpoint | default "http://otel-collector:4317" }}"
```

### 3. Update OCI Image Build ✅ COMPLETE

Bake default metadata into container images for runtime auto-detection:

**Implementation:**
- Modified `tools/bazel/release.bzl` to pass `env` dict to `multiplatform_image`
- Updated `tools/bazel/container_image.bzl` to accept `env` parameter
- Default environment variables flow from `release_app` → image build

**Example from `release.bzl`:**
```python
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

**Result:**
- ✅ All images now have default APP_NAME, APP_DOMAIN, APP_TYPE
- ✅ Kubernetes can override with more specific values (version, commit, pod info)
- ✅ Works even outside Kubernetes for local testing
- ✅ All 46 tests passing

## Application Code Usage

With environment variables set, applications can use minimal configuration:

### Before (Manual Configuration)
```python
from libs.python.logging import configure_logging

configure_logging(
    service_name="hello-fastapi",
    service_version="v1.2.3",
    deployment_environment="production",
    domain="demo",
    app_type="external-api",
    log_level="INFO",
    enable_otlp=True,
)
```

### After (Auto-Detection)
```python
from libs.python.logging import configure_logging

# Everything auto-detected from environment!
configure_logging()

# Or override only what you need:
configure_logging(
    log_level="DEBUG",
    enable_otlp=False,  # For external workers
)
```

## Benefits

1. **No Hardcoding**: Application code doesn't need to know its own name/version
2. **Single Source of Truth**: `release_app` metadata flows to all systems
3. **Consistent Metadata**: Same values in logs, metrics, traces
4. **Environment-Specific**: Different values for dev/staging/prod automatically
5. **Kubernetes Native**: Leverages downward API for pod context
6. **Build-Time Safety**: Bazel validates metadata at build time

## Migration Path

1. ✅ **Phase 1**: Update logging library to support auto-detection (DONE)
2. **Phase 2**: Update `release_app` macro to export metadata
3. **Phase 3**: Update Helm charts to inject environment variables
4. **Phase 4**: Update OCI image build to bake default values
5. **Phase 5**: Simplify application code to use auto-detection
6. **Phase 6**: Deprecate manual parameter passing

## Testing

### Local Development
Set environment variables manually:
```bash
export APP_NAME=hello-fastapi
export APP_DOMAIN=demo
export APP_TYPE=external-api
export APP_ENV=dev
export APP_VERSION=local

python -m my_app
```

### Kubernetes
Environment variables automatically set by downward API and Helm chart.

### Verification
Check logs for the auto-detection indicator:
```json
{
  "message": "Logging configured for hello-fastapi",
  "auto_detected": true,
  "environment": "dev",
  "domain": "demo"
}
```
