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
| `cache-hit` | A boolean value indicating if there was a cache hit for shared Bazel cache |
| `go-cache-hit` | A boolean value indicating if there was a cache hit for Go domain cache |
| `python-cache-hit` | A boolean value indicating if there was a cache hit for Python domain cache |

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

The action implements **domain-partitioned caching** to optimize cache hit rates and enable incremental testing:

### Cache Partitioning

The cache is partitioned into three domains:
- **Shared Cache**: Core Bazel configuration and shared dependencies
- **Go Domain Cache**: Go-specific targets and dependencies  
- **Python Domain Cache**: Python-specific targets and dependencies

### Cache Key Generation

Domain-specific cache keys are generated using:
```bash
# Shared configuration (always included)
CONFIG_HASH = hashFiles('MODULE.bazel', 'MODULE.bazel.lock', '.bazelrc', '.bazelversion')

# Domain-specific hashes
GO_HASH = hashFiles('go.mod', 'go.sum', 'hello_go/**', 'libs/go/**')
PYTHON_HASH = hashFiles('requirements.in', 'requirements.lock.txt', 'hello_python/**', 'libs/python/**')

# Final cache keys
GO_CACHE_KEY = "${RUNNER_OS}-bazel-go-${CONFIG_HASH}-${GO_HASH}-${CACHE_SUFFIX}"
PYTHON_CACHE_KEY = "${RUNNER_OS}-bazel-python-${CONFIG_HASH}-${PYTHON_HASH}-${CACHE_SUFFIX}"
SHARED_CACHE_KEY = "${RUNNER_OS}-bazel-shared-${CONFIG_HASH}-${CACHE_SUFFIX}"
```

### Incremental Testing

The domain-partitioned caching enables **incremental testing**:
- Only tests Go targets when Go domain cache misses (indicating Go code changes)
- Only tests Python targets when Python domain cache misses (indicating Python code changes)
- Always tests shared infrastructure regardless of cache state

This approach significantly reduces CI time for partial changes while maintaining full test coverage.

## Caches Created

The action manages multiple cache directories for domain-partitioned caching:

### Shared Caches (always created)
- `/tmp/bazel-cache` - Shared Bazel build cache
- `/tmp/bazel-repo-cache` - Shared Bazel repository cache  
- `~/.cache/bazelisk` - Bazelisk binary cache

### Domain-Specific Caches (created on demand)
- `/tmp/bazel-cache/go` - Go domain build cache
- `/tmp/bazel-cache/python` - Python domain build cache
- `/tmp/bazel-repo-cache/go` - Go domain repository cache
- `/tmp/bazel-repo-cache/python` - Python domain repository cache

### Cache Hit Outputs

The action provides cache hit status for each domain:
- `cache-hit`: Shared cache hit status
- `go-cache-hit`: Go domain cache hit status  
- `python-cache-hit`: Python domain cache hit status

These outputs enable incremental testing workflows that only test changed domains.

## Integration

This action is used in:
- `ci.yml` - All three CI jobs (build, test, docker)
- `release.yml` - All release workflow jobs

By centralizing the Bazelisk setup and caching logic, it ensures consistent behavior across all workflows and reduces maintenance overhead.