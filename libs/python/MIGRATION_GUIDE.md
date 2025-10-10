# Migration Guide: Using Unified Logging in ManMan

This guide shows how to gradually migrate ManMan's logging from `manman.src.logging_config` to the unified `libs.python.log_setup` module.

## Phase 1: Start Using Unified Logging in New Code

For new services or when refactoring existing services, use the unified logging:

### Example: Migrating Experience API

**Before (manman/src/host/main.py):**
```python
from manman.src.logging_config import setup_logging

setup_logging(
    level=logging.INFO,
    microservice_name="experience-api",
    app_env=os.getenv("APP_ENV", "dev"),
    enable_otel=enable_otel_logging,
)
```

**After:**
```python
from libs.python.log_setup import setup_logging

setup_logging(
    level=logging.INFO,
    app_name="experience-api",
    app_type="external-api",
    domain="manman",
    # app_env is read from APP_ENV environment variable automatically
    enable_otel=enable_otel_logging,
)
```

### Benefits of Migration

1. **Consistent with Release Metadata**: Uses the same app_name, app_type, and domain from `release_app`
2. **Better OTEL Integration**: Service name becomes `manman-experience-api` with structured attributes
3. **Environment Awareness**: Automatically reads APP_ENV from environment
4. **Unified Format**: All logs follow the `[domain/app-name/app-type/env]` pattern

## Phase 2: Update Environment Variables

Update your deployment configs to pass release metadata as environment variables:

### Helm Chart Values (example):
```yaml
apps:
  experience-api:
    env:
      APP_NAME: experience-api
      APP_TYPE: external-api
      APP_DOMAIN: manman
      APP_ENV: dev  # or staging, prod
```

### In Application Code:
```python
import os
from libs.python.log_setup import setup_logging

# Read from environment (set by Helm/K8s)
app_name = os.getenv("APP_NAME", "unknown")
app_type = os.getenv("APP_TYPE", "")
domain = os.getenv("APP_DOMAIN", "")

setup_logging(
    level=logging.INFO,
    app_name=app_name,
    app_type=app_type,
    domain=domain,
    enable_otel=True,
)
```

## Phase 3: Automatic Metadata Injection (Future)

In the future, we can inject metadata automatically via container environment:

### In Container Build (tools/container_image.bzl):
```python
# Extract metadata from release_app
metadata = get_app_metadata(app_target)

# Inject as environment variables
env = {
    "APP_NAME": metadata["name"],
    "APP_TYPE": metadata["app_type"],
    "APP_DOMAIN": metadata["domain"],
    # APP_ENV is set by deployment (dev/staging/prod)
}
```

### In Application Code:
```python
from libs.python.log_setup import setup_logging

# All metadata is automatically available from environment
setup_logging(
    level=logging.INFO,
    enable_otel=True,
)
```

The setup_logging function will automatically read:
- APP_NAME → app_name
- APP_TYPE → app_type  
- APP_DOMAIN → domain
- APP_ENV → app_env

## Phase 4: Deprecate Old Logging Config

Once all services are migrated:

1. Mark `manman.src.logging_config` as deprecated
2. Add deprecation warnings
3. Eventually remove the old module

## Coexistence Strategy

During migration, both logging systems can coexist:

```python
# manman/src/logging_config.py - Keep for backward compatibility
from libs.python.log_setup import setup_logging as unified_setup_logging

def setup_logging(
    level: int = logging.INFO,
    microservice_name: Optional[str] = None,
    app_env: Optional[str] = None,
    force_setup: bool = False,
    enable_otel: bool = False,
    enable_console: bool = True,
    otel_endpoint: Optional[str] = None,
) -> None:
    """
    Wrapper for backward compatibility.
    Delegates to unified logging with parameter mapping.
    """
    unified_setup_logging(
        level=level,
        app_name=microservice_name,  # Map microservice_name to app_name
        app_type=None,  # Not available in old API
        domain="manman",  # Hardcoded for manman services
        app_env=app_env,
        force_setup=force_setup,
        enable_otel=enable_otel,
        enable_console=enable_console,
        otel_endpoint=otel_endpoint,
    )
```

This allows existing code to continue working while new code uses the unified API.

## Testing During Migration

Verify logging works correctly after migration:

1. **Check Log Format**: Logs should include `[domain/app-name/app-type/env]` prefix
2. **Verify OTEL**: Check that traces/logs appear in OTEL collector with correct service name
3. **Test Environment Variables**: Ensure APP_ENV is correctly read from environment
4. **Validate Metadata**: Confirm app metadata appears in structured logs

## Rollback Plan

If issues arise during migration:

1. Keep old `manman.src.logging_config` unchanged
2. Use feature flags to toggle between old and new logging
3. Gradually migrate service by service
4. Monitor for any issues with log collection or OTEL

## Questions?

- What metadata should be exposed as environment variables?
- Should we auto-inject metadata in container builds?
- When should we deprecate the old logging config?
