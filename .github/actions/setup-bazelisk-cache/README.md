# Setup Bazelisk w## Solution

This action uses a robust cache key generation approach:
- ✅ Uses a bash step to generate cache keys with proper conditional logic
- ✅ Handles optional cache suffixes correctly using shell scripting
- ✅ Focuses on core configuration files that affect build dependencies  
- ✅ Generates valid cache keys that won't cause 400 errors
- ✅ Provides hierarchical restore-keys for better cache hit rates
- ✅ Creates cache directories proactively to avoid path errors
- ✅ Includes cache status information for debuggingg

This composite action sets up Bazelisk with optimized Bazel caching for CI/CD workflows.

## Purpose

This action was created to solve CI cache failures and eliminate code duplication across workflows. It addresses the DRY principle by providing a single, reusable action for Bazelisk setup and caching.

## Problem Solved

Before this action, Bazelisk caching was duplicated across multiple workflows and suffered from cache key generation issues. The original implementation used invalid GitHub Actions expression syntax that caused "Cache service responded with 400" errors:

```yaml
key: ${{ runner.os }}-bazel-${{ hashFiles(...) }}${{ inputs.cache-suffix && format('-{0}', inputs.cache-suffix) || '' }}
```

The issue was the invalid conditional expression syntax (`&&` and `||` operators are not supported in GitHub Actions expressions).

## Solution

This action uses an optimized cache key generation approach:
- ✅ Uses single `hashFiles()` call with multiple files to stay under 512-character limit
- ✅ Focuses on core configuration files that affect build dependencies  
- ✅ Handles missing files gracefully (returns empty string for missing files)
- ✅ Generates valid cache keys based on existing files
- ✅ Provides hierarchical restore-keys for better cache hit rates
- ✅ Creates cache directories proactively to avoid path errors
- ✅ Includes cache status information for debugging

## Inputs

| Input | Description | Required | Default |
|-------|-------------|----------|---------|
| `bazelisk-version` | Bazelisk version to use (e.g., "1.x" for latest 1.x, or specific like "1.27.0") | No | `1.x` |
| `cache-suffix` | Additional suffix for cache key to avoid conflicts between workflows | No | `''` |

## Outputs

| Output | Description |
|--------|-------------|
| `cache-hit` | A boolean value indicating if there was a cache hit for Bazel |

## Usage

### Basic Usage

```yaml
- name: Setup Bazelisk with caching
  uses: ./.github/actions/setup-bazelisk-cache
```

### With Custom Version

```yaml
- name: Setup Bazelisk with caching
  uses: ./.github/actions/setup-bazelisk-cache
  with:
    bazelisk-version: '1.27.0'
```

### With Cache Suffix

```yaml
- name: Setup Bazelisk with caching
  uses: ./.github/actions/setup-bazelisk-cache
  with:
    cache-suffix: 'release'
```

## Cache Strategy

The action creates an optimized cache key using a bash script that properly handles conditional logic:

```bash
BASE_KEY="${{ runner.os }}-bazel-${{ hashFiles('MODULE.bazel', 'MODULE.bazel.lock', '.bazelrc', '.bazelversion', 'go.mod', 'requirements.lock.txt') }}-cache"
if [[ -n "${{ inputs.cache-suffix }}" ]]; then
  CACHE_KEY="${BASE_KEY}-${{ inputs.cache-suffix }}"
else
  CACHE_KEY="${BASE_KEY}-default"
fi
```

This approach:
- Uses `hashFiles()` to generate a stable hash of core configuration files
- Handles optional cache suffixes with proper bash conditional logic
- Ensures cache keys are always valid and won't cause 400 errors
- Provides consistent cache key generation across different workflows

The restore-keys provide hierarchical fallback options, ensuring good cache hit rates even when some files change.

## Caches Created

The action manages three cache directories:
- `/tmp/bazel-cache` - Bazel build cache
- `/tmp/bazel-repo-cache` - Bazel repository cache
- `~/.cache/bazelisk` - Bazelisk binary cache

## Integration

This action is used in:
- `ci.yml` - All three CI jobs (build, test, docker)
- `release.yml` - All release workflow jobs

By centralizing the Bazelisk setup and caching logic, it ensures consistent behavior across all workflows and reduces maintenance overhead.