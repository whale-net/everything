# Demo Domain Exclusion Feature

## Overview

This feature adds the ability to exclude the `demo` domain from releases when using the `all` option for apps and helm charts. This is useful for production releases where you want to avoid accidentally publishing demo/example applications and charts.

## Changes Made

### 1. Core Logic Changes

#### `tools/release_helper/release.py`
- Added `include_demo` parameter to `plan_release()` function
- When `apps="all"` and `include_demo=False`, filters out apps with `domain='demo'`
- Logs message when demo domain is excluded

#### `tools/release_helper/cli.py`
- Added `--include-demo` flag to `plan` command
- Added `--include-demo` flag to `plan-helm-release` command
- When `charts="all"` and `include_demo=False`, filters out charts with `domain='demo'`

### 2. GitHub Actions Workflow

#### `.github/workflows/release.yml`
- Added `include_demo` checkbox input (default: unchecked)
- Updated `plan-release` job to pass `--include-demo` flag when checkbox is checked
- Updated `release-helm-charts` job to pass `--include-demo` flag when checkbox is checked

### 3. Tests

#### `tools/release_helper/test_exclude_demo.py`
- Comprehensive unit tests for app exclusion logic
- Comprehensive unit tests for chart exclusion logic
- Tests verify that specific apps/charts and domain-specific selections are not affected

#### `tools/release_helper/BUILD.bazel`
- Added test target for `test_exclude_demo`

### 4. Documentation

#### `docs/HELM_RELEASE.md`
- Updated usage examples to show the new checkbox
- Added "Demo Domain Exclusion" section explaining the behavior
- Added CLI examples with and without `--include-demo` flag

#### `docs/HELM_RELEASE_INTEGRATION.md`
- Updated examples to show demo exclusion behavior
- Added new "Demo Domain Exclusion" section

## Behavior

### Default Behavior (Demo Excluded)
- `--apps all` → Releases all apps **except** demo domain
- `--charts all` → Releases all charts **except** demo domain

### With `--include-demo` Flag
- `--apps all --include-demo` → Releases **all** apps including demo
- `--charts all --include-demo` → Releases **all** charts including demo

### Not Affected
The following are **not affected** by the `--include-demo` flag:
- Specific app/chart names (e.g., `hello_python`, `helm-demo-hello-fastapi`)
- Domain-specific selections (e.g., `demo`, `manman`)

## Usage Examples

### GitHub Actions

**Exclude demo (default):**
1. Apps: `all`
2. Helm charts: `all`
3. Include demo domain: ❌ (unchecked)
→ Releases all production apps and charts only

**Include demo:**
1. Apps: `all`
2. Helm charts: `all`
3. Include demo domain: ✅ (checked)
→ Releases everything including demo

### CLI

**Exclude demo (default):**
```bash
bazel run //tools:release -- plan --apps all --version v1.0.0 --event-type workflow_dispatch

bazel run //tools:release -- plan-helm-release --charts all --version v1.0.0
```

**Include demo:**
```bash
bazel run //tools:release -- plan --apps all --version v1.0.0 --event-type workflow_dispatch --include-demo

bazel run //tools:release -- plan-helm-release --charts all --version v1.0.0 --include-demo
```

## Testing

### Manual Validation
A validation script (`/tmp/validate_demo_exclusion.py`) was created to test the logic:
- ✅ Apps exclusion works correctly
- ✅ Charts exclusion works correctly
- ✅ Include flag works correctly
- ✅ All validation tests pass

### Unit Tests
- ✅ Test file created: `tools/release_helper/test_exclude_demo.py`
- ✅ Tests cover all scenarios:
  - Default exclusion of demo for apps
  - Default exclusion of demo for charts
  - Inclusion with flag for apps
  - Inclusion with flag for charts
  - Specific selections not affected
  - Domain selections not affected

## Benefits

1. **Safer production releases**: Demo apps/charts are excluded by default when using `all`
2. **Explicit control**: Must explicitly check box or use flag to include demo
3. **Backward compatible**: Specific app/chart names and domain selections work as before
4. **Clear intent**: Users must consciously decide to include demo in production releases
