# Unified Logging Architecture

## Overview

The unified logging system integrates **release app properties** (defined in `release_app` macro) with **deployment properties** (set at runtime via environment variables).

## Data Flow

```
release_app(BUILD.bazel)          Environment Variables          Logging Output
====================              ===================            ==============

domain: "manman"          ─┐                                 ┌─> [manman/experience-api/
app_name: "experience-api" ├──> Container Env Vars ──────────┤     external-api/dev]
app_type: "external-api"  ─┘     APP_NAME=...               └─> OTEL Resource Attributes:
                                  APP_TYPE=...                   - service.name: manman-experience-api
                                  APP_DOMAIN=...                 - service.type: external-api
                                  APP_ENV=dev (K8s)              - service.domain: manman
                                                                 - deployment.environment: dev
```

## Components

### 1. Release Metadata (Build Time)
Defined in `BUILD.bazel` via `release_app` macro:

```starlark
release_app(
    name = "experience-api",
    binary_name = "//manman/src/host:experience_api",
    language = "python",
    domain = "manman",                    # ← Logging domain
    app_type = "external-api",            # ← Logging app_type
    description = "Experience API service",
    port = 8000,
    args = ["start-experience-api"],
)
```

### 2. Deployment Properties (Runtime)
Set by Kubernetes/Helm at deployment time:

```yaml
env:
  - name: APP_ENV
    value: "dev"  # or staging, prod
  # Future: auto-injected from release metadata
  - name: APP_NAME
    value: "experience-api"
  - name: APP_TYPE
    value: "external-api"
  - name: APP_DOMAIN
    value: "manman"
```

### 3. Unified Logging (Application)
Used in application code:

```python
from libs.python.log_setup import setup_logging

setup_logging(
    level=logging.INFO,
    app_name=os.getenv("APP_NAME", "experience-api"),
    app_type=os.getenv("APP_TYPE", "external-api"),
    domain=os.getenv("APP_DOMAIN", "manman"),
    # app_env read from APP_ENV automatically
    enable_otel=True,
)
```

## Log Format

### Console Logs
```
2025-10-10 05:00:00,000 - [manman/experience-api/external-api/dev] manman.src.api - INFO - Processing request
                          ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
                          domain/app_name/app_type/app_env
```

### OTEL Structured Logs
```json
{
  "resource": {
    "service.name": "manman-experience-api",
    "service.type": "external-api",
    "service.domain": "manman",
    "deployment.environment": "dev",
    "service.instance.id": "pod-abc123"
  },
  "timestamp": "2025-10-10T05:00:00Z",
  "severity": "INFO",
  "body": "Processing request"
}
```

## Benefits

1. **Single Source of Truth**: Release metadata defined once in BUILD.bazel
2. **Consistent Naming**: Same app_name, app_type, domain everywhere
3. **Environment Aware**: Logs show which environment (dev/staging/prod)
4. **OTEL Ready**: Structured attributes for distributed tracing
5. **Backward Compatible**: Works with existing manman patterns

## Future Enhancements

### Auto-Inject Metadata in Containers
Modify `tools/container_image.bzl` to automatically inject release metadata as environment variables:

```python
def multiplatform_image(name, binary, **kwargs):
    # Extract metadata from release_app
    metadata = get_app_metadata(binary)
    
    # Auto-inject as environment
    env = kwargs.get("env", {})
    env.update({
        "APP_NAME": metadata["name"],
        "APP_TYPE": metadata["app_type"],
        "APP_DOMAIN": metadata["domain"],
    })
    
    # Build image with injected metadata
    oci_image(name=name, env=env, ...)
```

Then application code becomes even simpler:

```python
from libs.python.log_setup import setup_logging

# All metadata automatically available from environment
setup_logging(
    level=logging.INFO,
    enable_otel=True,
)
```

The `setup_logging` function reads APP_NAME, APP_TYPE, APP_DOMAIN, and APP_ENV automatically.

## Migration Path

1. **Phase 1**: Use unified logging in new services (manual env vars)
2. **Phase 2**: Update existing services one by one
3. **Phase 3**: Implement auto-injection in container builds
4. **Phase 4**: Deprecate manman.src.logging_config

See `MIGRATION_GUIDE.md` for detailed migration steps.
