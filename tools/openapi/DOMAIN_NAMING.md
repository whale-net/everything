# OpenAPI Spec Domain-Based Naming

## Overview

OpenAPI specification files are now generated with domain prefixes in their filenames to prevent naming conflicts when multiple apps with the same name exist in different domains.

## Filename Format

**Previous format:**
```
{app_name}_openapi_spec.json
```

**New format (when domain is provided):**
```
{domain}-{app_name}_openapi_spec.json
```

## Examples

### Without Domain (fallback to old format)
- Target: `//demo/hello-fastapi:hello-fastapi_openapi_spec`
- Output: `hello-fastapi_openapi_spec.json`

### With Domain
- Target: `//manman:experience-api_openapi_spec`
- Domain: `manman`
- App: `experience-api`
- Output: `manman-experience-api_openapi_spec.json`

## Why This Change?

This prevents conflicts when:
1. Two apps with the same name exist in different domains
2. Bazel builds specs for multiple domains in the same build
3. CI/CD workflows process multiple domain specs

### Example Conflict (Before)
```
demo/my-app     -> my-app_openapi_spec.json
manman/my-app   -> my-app_openapi_spec.json  # CONFLICT!
```

### Resolution (After)
```
demo/my-app     -> demo-my-app_openapi_spec.json
manman/my-app   -> manman-my-app_openapi_spec.json  # No conflict
```

## Usage

The domain is automatically passed from `release_app` to `openapi_spec`:

```starlark
# In your BUILD.bazel
release_app(
    name = "my-api",
    domain = "demo",  # This domain is used in the OpenAPI spec filename
    fastapi_app = "demo.my_api.main:app",
    # ... other config
)
```

## Backward Compatibility

The GitHub workflow includes fallback logic to support both formats:
1. First tries: `{domain}-{app}_openapi_spec.json`
2. Falls back to: `{app}_openapi_spec.json`

This ensures existing builds continue to work during the transition.

## Related Files

- `tools/openapi/openapi.bzl` - OpenAPI spec generation rule
- `tools/bazel/release.bzl` - Passes domain to openapi_spec
- `.github/workflows/release.yml` - Handles both filename formats
