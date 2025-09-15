# Bazel Caching Strategy Implementation

This repository implements a sophisticated multi-layered caching strategy for Bazel builds in GitHub Actions, based on the architectural design document for monorepo caching.

## Architecture Overview

The implementation uses `bazel-contrib/setup-bazel` to provide a unified, multi-layered caching approach:

### Layer 1: Global & Stable Caches
- **Bazelisk Cache**: Caches the Bazel binary itself, keyed by `.bazelversion`
- **Repository Cache**: Caches external dependencies from `MODULE.bazel.lock`, shared across all workflows

### Layer 2: Build Artifacts Cache
- **Disk Cache**: Unified cache for all build and test artifacts using "monorepo" namespace
- Uses sophisticated cache keys with restore-keys for feature branch optimization

## Key Components

### 1. Centralized Configuration (`.github/.bazelrc.ci`)
Contains all CI-specific Bazel flags:
- Remote caching protocol configuration
- Asynchronous cache uploads (using updated `--remote_cache_async` flag)
- Performance optimizations
- Test output configurations
- Updated to use non-deprecated flags

### 2. Multi-Layered Caching Setup
Each job in the CI pipeline uses the same caching configuration:
```yaml
- name: Setup Bazel with Multi-Layered Caching
  uses: bazel-contrib/setup-bazel@0.15.0
  with:
    bazelisk-cache: true      # Layer 1: Bazel binary cache
    repository-cache: true    # Layer 1: Dependencies cache
    disk-cache: "monorepo"    # Layer 2: Build artifacts cache
    bazelrc: |               # Import CI configuration
      import %workspace%/.github/.bazelrc.ci
```

**Important**: The CI configuration is imported but not automatically applied. Bazel commands must explicitly use `--config=ci` to apply the CI-specific settings and avoid config duplication warnings.

## Cache Key Strategy

The `bazel-contrib/setup-bazel` action automatically generates sophisticated cache keys:

- **Primary Keys**: Include branch name and file hashes for exact matches
- **Restore Keys**: Allow feature branches to seed from main branch cache
- **File-based Invalidation**: Automatically invalidates when critical files change

## Benefits

1. **Faster Builds**: Avoids rebuilding unchanged artifacts
2. **Efficient Storage**: Uses GitHub Actions' 10GB cache limit effectively
3. **Branch Optimization**: Feature branches inherit cache from main branch
4. **Automatic Management**: No manual cache key management required
5. **Future-Ready**: Easy migration path to REAPI-compliant remote cache

## Migration from Custom Actions

This implementation replaces the custom `.github/actions/setup-bazelisk-cache` with the standardized `bazel-contrib/setup-bazel` action, providing:
- Better cache key generation
- Standardized multi-layer approach
- Improved restore-keys strategy
- Reduced maintenance overhead

## Monitoring

Monitor CI performance to detect cache effectiveness:
- Watch for sudden increases in build times
- Monitor cache hit rates through build logs
- Check for cache thrashing between different workflows

## Future Scaling

When the organization outgrows GitHub Actions cache limitations, this setup provides an easy migration path to:
- Self-hosted REAPI backends (bazel-remote, BuildBuddy, etc.)
- Cloud storage backends (GCS, S3)
- Commercial managed services

The migration only requires updating the `.bazelrc.ci` configuration - no workflow changes needed.