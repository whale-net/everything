# Tools

This directory contains Bazel tools and utilities for the monorepo.

## Release Helper

The release helper (`release_helper.py`) is a comprehensive tool for managing app releases and container images.

### Key Commands
```bash
# List all apps with release metadata
bazel run //tools:release -- list

# Detect apps that have changed since last tag
bazel run //tools:release -- changes

# Build and load a container image for an app
bazel run //tools:release -- build <app_name>

# Release an app with version and optional commit tag
bazel run //tools:release -- release <app_name> --version <version> --commit <sha>

# Plan a release (used by CI)
bazel run //tools:release -- plan --event-type tag_push --version <version>
```

The release helper ensures consistent handling of container images, version validation, and integration with CI/CD workflows.

## Performance Optimization

The release helper is optimized for fast builds and effective caching:

### Caching Strategy
- Pre-built in CI workflows to ensure it's always cached
- Uses unified cache keys across all jobs for maximum cache hits
- Optimized Bazel configuration with `--config=tools` for faster builds

### Build Configuration
To build the release tool with optimizations:
```bash
# Standard build
bazel build //tools:release

# Optimized build (recommended)
bazel build --config=tools //tools:release

# Test caching performance
./cache-optimization-test.sh
```

For detailed information about the caching optimizations, see [Release Tool Caching Documentation](../docs/release-tool-caching.md).
