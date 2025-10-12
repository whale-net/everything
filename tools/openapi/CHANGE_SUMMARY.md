# OpenAPI Spec Domain Naming - Change Summary

## Problem Statement
The intermediate OpenAPI JSON artifact did not include the domain prefix, which could lead to conflicts if app names are shared between domains.

## Example Scenario (Before Fix)
```
demo/api-service     → api-service_openapi_spec.json
manman/api-service   → api-service_openapi_spec.json  # CONFLICT!
```

Both apps would generate files with the same name in the Bazel build output, causing potential overwrites and build failures.

## Solution Implemented
Modified the OpenAPI spec generation system to include the domain prefix in the output filename.

### Changes Made

#### 1. `tools/openapi/openapi.bzl`
- Added `domain` parameter to `openapi_spec()` function
- Modified output filename logic:
  - With domain: `{domain}-{app}_openapi_spec.json`
  - Without domain: `{app}_openapi_spec.json` (backward compatibility)

#### 2. `tools/bazel/release.bzl`
- Updated `release_app` to pass `domain` parameter when calling `openapi_spec`
- This ensures all OpenAPI specs generated via `release_app` include the domain

#### 3. `.github/workflows/release.yml`
- Updated the "Build OpenAPI spec" job to look for the new filename format
- Added fallback logic to support both formats during transition
- First tries: `{domain}-{app}_openapi_spec.json`
- Falls back to: `{app}_openapi_spec.json`

## Example Scenario (After Fix)
```
demo/api-service     → demo-api-service_openapi_spec.json
manman/api-service   → manman-api-service_openapi_spec.json  # No conflict!
```

## Backward Compatibility
- The workflow includes fallback logic to support the old naming format
- Existing builds continue to work during the transition
- Client generation is unaffected (uses Bazel labels, not filenames)

## Testing
The change was verified by:
1. Reviewing all references to OpenAPI spec files in the codebase
2. Confirming that `openapi_client` uses labels, not filenames
3. Documenting the expected behavior in `DOMAIN_NAMING.md`
4. Verifying syntax of modified Bazel files

## Impact
- ✅ Prevents filename conflicts between apps with same name in different domains
- ✅ Makes spec files more identifiable in build outputs
- ✅ Maintains backward compatibility with existing workflows
- ✅ No changes required to client generation or consumption

## Files Modified
1. `tools/openapi/openapi.bzl` - Added domain parameter and filename logic
2. `tools/bazel/release.bzl` - Pass domain to openapi_spec
3. `.github/workflows/release.yml` - Updated to handle new filename format
4. `tools/openapi/DOMAIN_NAMING.md` - Documentation (new file)
5. `tools/openapi/CHANGE_SUMMARY.md` - This file (new file)
