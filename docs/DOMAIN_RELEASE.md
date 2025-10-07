# Domain/Namespace Release Guide

This document demonstrates how to release all apps in a domain/namespace, similar to how helm charts can be released by domain.

## Overview

The release system supports multiple app reference formats:
- **Full format**: `domain-appname` (e.g., `demo-hello_python`)
- **Path format**: `domain/appname` (e.g., `demo/hello_python`)
- **Short format**: `appname` (e.g., `hello_python`) - if unambiguous
- **Domain format**: `domain` (e.g., `demo`) - **releases ALL apps in that domain**

## Available Domains

To see all available domains in the repository:

```bash
bazel run //tools:release -- list
```

Current domains in this repository:
- **demo**: Example/demo applications
- **manman**: Production manman services (experience_api, status_api, worker_dal_api, status_processor, worker, migration)

## Examples

### Example 1: Release All Demo Apps

Release all apps in the `demo` domain with version v1.0.0:

```bash
# Via GitHub CLI
gh workflow run release.yml \
  -f apps=demo \
  -f version=v1.0.0

# Via local release tool
bazel run //tools:release -- plan \
  --event-type workflow_dispatch \
  --apps demo \
  --version v1.0.0
```

This will release:
- demo-hello_python
- demo-hello_go
- demo-hello_fastapi
- demo-hello_internal_api
- demo-hello_worker
- demo-hello_job
- demo-hello_world_test

### Example 2: Release All Manman Services

Release all production manman services:

```bash
# Via GitHub CLI
gh workflow run release.yml \
  -f apps=manman \
  -f version=v2.1.0

# This releases all manman domain apps:
# - experience_api
# - status_api
# - worker_dal_api
# - status_processor
# - worker
# - migration
```

### Example 3: Mixed Format - Specific App + Domain

Release a specific app from one domain plus all apps from another domain:

```bash
gh workflow run release.yml \
  -f apps=hello_python,manman \
  -f version=v1.5.0

# This releases:
# - demo-hello_python (specific app)
# - All manman domain apps (domain)
```

### Example 4: Multiple Domains

Release apps from multiple domains:

```bash
gh workflow run release.yml \
  -f apps=demo,manman \
  -f version=v2.0.0

# This releases all apps in both demo and manman domains
```

### Example 5: Domain Exclusion with "all"

When using `all`, the demo domain is excluded by default:

```bash
# Releases all apps EXCEPT demo domain apps
gh workflow run release.yml \
  -f apps=all \
  -f version=v1.0.0

# To include demo domain when using "all":
gh workflow run release.yml \
  -f apps=all \
  -f version=v1.0.0 \
  -f include_demo=true
```

## Use Cases

### Use Case 1: Release by Service Tier
If you organize apps by tier (e.g., `frontend`, `backend`, `data`), you can release entire tiers:

```bash
gh workflow run release.yml -f apps=backend -f version=v1.0.0
```

### Use Case 2: Release by Team
If domains represent team ownership (e.g., `platform`, `analytics`), teams can release their apps:

```bash
gh workflow run release.yml -f apps=platform -f version=v2.0.0
```

### Use Case 3: Staged Rollout
Release different domains at different times:

```bash
# Stage 1: Internal services
gh workflow run release.yml -f apps=internal -f version=v3.0.0

# Stage 2: Public APIs (after validation)
gh workflow run release.yml -f apps=api -f version=v3.0.0

# Stage 3: Frontend apps (after API validation)
gh workflow run release.yml -f apps=web -f version=v3.0.0
```

## Implementation Details

The domain release feature is implemented in:
- **Validation**: `tools/release_helper/validation.py` - `validate_apps()` function
- **Release Planning**: `tools/release_helper/release.py` - `plan_release()` function
- **Tests**: `tools/release_helper/test_validation.py` - `test_validate_apps_domain_format()`
- **Integration Test**: `tools/release_helper/test_release.py` - `test_plan_release_workflow_dispatch_domain_format()`

### How It Works

1. When you specify a domain name (e.g., `demo`), the system:
   - Checks if the name matches an existing domain via `is_domain_name()`
   - Retrieves all apps in that domain via `validate_domain()`
   - Returns the complete list of apps for release planning

2. The validation system supports mixing formats:
   - You can specify `hello_python,manman` to release one specific app and all apps in a domain
   - Apps are deduplicated automatically if specified multiple times

3. Domain detection is automatic:
   - The system queries all apps and extracts unique domain names
   - If your input matches a domain name exactly, it's treated as a domain
   - Otherwise, it's treated as an app name

## Error Handling

If you specify an invalid domain name, you'll get a helpful error:

```
Invalid apps: nonexistent.
Available apps: demo-hello_python, demo-hello_go, ...
Available domains: demo, manman
You can use: full format (domain-appname, e.g. demo-hello_python), 
path format (domain/appname, e.g. demo/hello_python), 
short format (appname, e.g. hello_python, if unambiguous), 
or domain format (domain, e.g. demo)
```

## Comparison with Helm Charts

The app release system uses the same domain-based approach as helm charts:

**Helm Charts**:
```bash
gh workflow run release.yml -f helm_charts=demo
```

**Apps**:
```bash
gh workflow run release.yml -f apps=demo
```

Both systems support:
- ✅ Domain/namespace filtering
- ✅ `all` with demo exclusion by default
- ✅ `--include-demo` flag to include demo domain
- ✅ Mixed format inputs (specific items + domains)

## Best Practices

1. **Use domains to organize apps by deployment tier or team**
   - Makes bulk releases easier
   - Reduces release coordination overhead

2. **Use `all` with `--include-demo=false` for production releases**
   - Prevents accidental demo app releases to production

3. **Use domain format for coordinated service updates**
   - When updating a microservice domain, release all services together

4. **Use mixed format for phased rollouts**
   - Release critical apps individually, then use domain for the rest

## Related Documentation

- **AGENTS.md**: Complete release system documentation
- **HELM_RELEASE.md**: Helm chart release documentation
- **README.md**: Quick start guide with examples
