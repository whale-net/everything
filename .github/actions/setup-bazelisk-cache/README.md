# Setup Bazelisk with Caching

This composite action sets up Bazelisk with optimized Bazel caching for CI/CD workflows.

## Purpose

This action was created to solve CI cache failures and eliminate code duplication across workflows. It addresses the DRY principle by providing a single, reusable action for Bazelisk setup and caching.

## Problem Solved

Before this action, Bazelisk caching was duplicated across multiple workflows and suffered from cache key generation issues similar to the Go caching problems. The cache configuration used:

```yaml
key: ${{ runner.os }}-bazel-${{ hashFiles(env.CACHE_KEY_FILES) }}
```

This approach failed when `hashFiles()` with multiple file patterns encountered missing files or when patterns didn't match properly, causing "Cache service responded with 400" errors.

## Solution

This action uses separate `hashFiles()` calls for each file pattern:
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

The action creates a hierarchical cache key based on:
1. `MODULE.bazel` - Core Bazel module configuration
2. `MODULE.bazel.lock` - Locked dependency versions
3. `.bazelrc` - Bazel configuration
4. `.bazelversion` - Bazel version specification
5. `**/BUILD.bazel` - All BUILD files across the repository
6. `**/*.bzl` - All Starlark extension files
7. `go.mod` - Go module dependencies (if applicable)
8. `requirements.lock.txt` - Python dependencies (if applicable)

The restore-keys provide fallback options, ensuring good cache hit rates even when some files change.

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