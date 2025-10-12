# Distroless Base Image Migration

## Overview

As of October 2025, Python container images have been migrated from Ubuntu 24.04 to Google's distroless/base-debian12 image, resulting in a **78% reduction in base image size**.

## Changes

### Base Image
- **Before**: `ubuntu:24.04` (117MB)
- **After**: `gcr.io/distroless/base-debian12` (~25MB)
- **Reduction**: 92MB (78.6% smaller)

### What's Included in Distroless Base
- ✅ glibc and essential runtime libraries
- ✅ SSL/TLS certificates (ca-certificates)
- ✅ Busybox shell (/bin/sh)
- ✅ libssl, libcrypto
- ❌ No package manager (apt/dpkg)
- ❌ No shell utilities (minimal attack surface)

## Impact

### For Python Applications
- **No code changes required**: All existing Python applications work unchanged
- **Smaller images**: Total image size reduced by the base image difference (92MB)
- **Faster deployments**: Reduced image pull time
- **Better security**: Minimal attack surface, no package manager

### For Go Applications
- **Same benefits**: Go apps also use distroless base
- **Already minimal**: Go binaries are statically linked, so impact is primarily base image size

## Compatibility

### Maintained Features
- ✅ Bazel's hermetic Python toolchain
- ✅ Cross-compilation (ARM64/AMD64)
- ✅ SSL/TLS connections
- ✅ Shell entrypoint scripts
- ✅ Multi-platform support

### Platform Changes
Reduced supported platforms to focus on actively used architectures:
- ✅ linux/amd64 (AMD64/x86_64)
- ✅ linux/arm64 (ARM64/aarch64)
- ❌ linux/arm/v7 (no longer needed)
- ❌ linux/ppc64le (no longer needed)
- ❌ linux/riscv64 (no longer needed)
- ❌ linux/s390x (no longer needed)

## Migration Guide

### For Existing Applications
No action required! Your applications will automatically use the new base image on the next build.

### Building Images
```bash
# Same commands as before
bazel run //demo/hello_python:hello-python_image_load --platforms=//tools:linux_x86_64
docker run --rm demo-hello-python:latest

# Release tool works unchanged
bazel run //tools:release -- build hello-python
```

### Troubleshooting

#### Image Pull Errors
If you see errors pulling `gcr.io/distroless/base-debian12`:
```bash
# Ensure you can access Google Container Registry
docker pull gcr.io/distroless/base-debian12:latest
```

#### Missing Utilities
Distroless images intentionally exclude package managers and shell utilities. If your app requires additional tools:
1. Add them as build dependencies in your BUILD.bazel
2. Or, override the base image for specific apps:
   ```starlark
   # Use custom base for this app only
   release_app(
       name = "my_app",
       language = "python",
       domain = "demo",
       base = "@custom_base",  # Override base image
   )
   ```

## Technical Details

### Files Modified
1. `MODULE.bazel`: Changed base image pull configuration
2. `tools/bazel/container_image.bzl`: Updated default base parameter
3. `README.md`: Updated documentation

### Verification
Images have been tested to ensure:
- ✅ Python apps start correctly
- ✅ Dependencies load properly
- ✅ SSL/TLS connections work
- ✅ Cross-compilation produces correct artifacts
- ✅ Multi-platform manifests work correctly

## Benefits Summary

### Size Reduction
| Component | Before | After | Reduction |
|-----------|--------|-------|-----------|
| Base Image | 117MB | 25MB | 92MB (78.6%) |
| Total Python App* | ~200MB | ~108MB | ~92MB (46%) |

*Example with typical dependencies (FastAPI, Pydantic, etc.)

### Additional Benefits
- **Security**: Smaller attack surface, fewer vulnerabilities
- **Performance**: Faster image pulls, quicker deployments
- **Cost**: Reduced bandwidth and storage costs
- **Maintenance**: Google maintains distroless images with security updates

## References

- [Distroless Container Images](https://github.com/GoogleContainerTools/distroless)
- [Original Issue](https://github.com/whale-net/everything/issues/XXX)
- [Base Image Comparison](https://github.com/GoogleContainerTools/distroless#why-should-i-use-distroless-images)
