# Docker Images & OCI System

This guide covers container image building and management in the monorepo.

## Overview

Each application is automatically containerized using the consolidated `release_app` macro, which creates both release metadata and OCI images with multiplatform support.

## Consolidated Release System

The `release_app` macro in `//tools:release.bzl` automatically creates both release metadata and multiplatform OCI images:

```starlark
load("//tools:release.bzl", "release_app")

# This single declaration creates:
# - Release metadata (JSON file with app info)
# - Multi-platform OCI images (amd64, arm64)
# - Proper container registry configuration
release_app(
    name = "hello-python",
    language = "python",
    domain = "demo",
    description = "Python hello world application with pytest",
    app_type = "external-api",
    port = 8000,
)
```

### Generated Targets

- `hello-python_image` - Multi-platform OCI image index (AMD64 + ARM64)
- `hello-python_image_base` - Base image (used by platform transitions)
- `hello-python_image_load` - Load Linux image into Docker (requires --platforms flag)
- `hello-python_image_push` - Push multi-platform index to registry

## Cache Optimization

The new OCI build system uses `oci_load` targets instead of traditional tarball generation, providing:

- **Better cache hit rates** - No giant single-layer tarballs
- **Faster CI builds** - Only rebuilds changed layers
- **Efficient development workflow** - Direct integration with Docker/Podman
- **No unused artifacts** - Eliminates the never-used tarball targets from CI

## Building Images with Bazel

```bash
# Build multi-platform image index (contains both AMD64 and ARM64)
bazel build //demo/hello_python:hello-python_image

# Load into Docker - CRITICAL: Must specify --platforms for Linux binaries
# On M1/M2 Macs (ARM64):
bazel run //demo/hello_python:hello-python_image_load --platforms=//tools:linux_arm64

# On Intel Macs/PCs (AMD64):
bazel run //demo/hello_python:hello-python_image_load --platforms=//tools:linux_x86_64

# Test the containers (after loading)
docker run --rm demo-hello-python:latest
docker run --rm demo-hello-go:latest

# Use release tool for production workflows (handles platforms automatically)
bazel run //tools:release -- build hello-python

# WARNING: Without --platforms flag, you may get macOS binaries instead of Linux,
# resulting in "Exec format error" when running containers.
```

## Base Images & Architecture

- **Python**: Uses `python:3.13-slim` (Python 3.13.13 on Debian 12)
- **Go**: Uses `alpine:3.20` (Alpine 3.20.3 for minimal size)
- **Platforms**: Full support for both `linux/amd64` and `linux/arm64`
- **Cross-compilation**: Automatically handles platform-specific builds

## Container Image Naming Convention

All container images follow the `<domain>-<app>:<version>` format:

```bash
# Registry format
ghcr.io/OWNER/demo-hello-python:v1.2.3    # Version-specific (release workflow)
ghcr.io/OWNER/demo-hello-python:latest    # Latest from main branch
ghcr.io/OWNER/demo-hello-python:abc123def # Commit-specific (release workflow)

# Local development format
demo-hello-python:latest
```

**Tagging Strategy:**
- **Main branch pushes**: Only update the `:latest` tag
- **Release workflow**: Creates version-specific (`:v1.2.3`) and commit-specific (`:abc123def`) tags in addition to `:latest`

## Advanced: Manual OCI Rules

> **Note:** The `release_app` macro handles all standard use cases. Manual OCI rules are only needed for highly specialized scenarios.

For edge cases requiring custom OCI configuration, individual rules are available in `//tools:oci.bzl`:

```starlark
load("//tools:oci.bzl", "python_oci_image", "go_oci_image", "oci_image_with_binary")

# Single platform image with custom configuration
oci_image_with_binary(
    name = "custom_image",
    binary = ":my_binary",
    base_image = "@python_slim",
    platform = "linux/amd64",
    repo_tag = "custom:latest",
    # ... custom OCI parameters
)
```

Available functions include:
- `oci_image_with_binary`: Generic OCI image builder with cache optimization
- `multiplatform_image`: Multi-platform image builder (used by release_app)
