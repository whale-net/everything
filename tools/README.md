# Tools

This directory contains Bazel tools and utilities for the monorepo.

## Directory Structure

- **`bazel/`** - Bazel rules and macros (`.bzl` files)
  - `release.bzl` - Release system and container image generation
  - `container_image.bzl` - Multiplatform OCI image creation
  - `platforms.bzl` - Platform definitions for cross-compilation
  - `pytest.bzl` - Python testing rules
- **`openapi/`** - OpenAPI specification and client generation
  - `openapi.bzl` - OpenAPI spec generation from FastAPI apps
  - `openapi_client.bzl` - Client generation rule
  - `openapi_gen_wrapper.sh` - OpenAPI Generator wrapper script
- **`scripts/`** - Utility scripts
  - `version_resolver.py` - Helm chart version resolution
  - `test_cross_compilation.sh` - Multiplatform image verification
  - `python_runner.py` - Python execution wrapper
- **`helm/`** - Helm chart generation and Kubernetes manifest management
- **`release_helper_go/`** - Release management tools and utilities
- **`client_codegen/`** - OpenAPI client code generation
- **`cacerts/`** - CA certificates for container images

## Release System

The release system uses standard Bazel binaries (`py_binary`, `go_binary`) with the `release_app` macro to create container images and deployment metadata.

### Example Usage

```starlark
load("//tools/bazel:release.bzl", "release_app")
load("@rules_python//python:defs.bzl", "py_binary")

# Standard py_binary - no wrapper needed!
py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = ["@pypi//:fastapi"],
)

# Add release metadata and container image generation
release_app(
    name = "my_app",
    language = "python",
    domain = "demo",
    app_type = "external-api",  # external-api, internal-api, worker, or job
    port = 8000,                # Port app listens on
)
```

**Cross-compilation**: Build for different platforms using `--platforms` flag:
```bash
bazel build //app:my_app --platforms=//tools:linux_x86_64
bazel build //app:my_app --platforms=//tools:linux_arm64
```

## Container Image Tools

### multiplatform_image (`bazel/container_image.bzl`)
Creates OCI container images with multiplatform support (AMD64 and ARM64).

Automatically used by `release_app` macro - no need to call directly in most cases.

## Release Helper

The release helper (`tools/release_helper_go`) is a Go CLI for managing app releases and container images. See [`docs/RELEASE.md`](../docs/RELEASE.md) for full usage.

### Key Commands
```bash
# List all apps with release metadata
bazel run //tools:release -- list

# Detect apps that have changed since last tag
bazel run //tools:release -- changes

# Build and load a container image for an app
bazel run //tools:release -- build <app_name>

# Plan a release (used by CI)
bazel run //tools:release -- plan --event-type tag_push --version <version>
```

The release helper ensures consistent handling of container images, version validation, and integration with CI/CD workflows.

## Migration Notes

For backward compatibility, aliases are provided at the top level:
- `//tools:release` → `//tools/release_helper_go:release_helper_go`
- `//tools:version_resolver` → `//tools/scripts:version_resolver`
- `//tools:test_cross_compilation` → `//tools/scripts:test_cross_compilation`
- `//tools:openapi_gen_wrapper` → `//tools/openapi:openapi_gen_wrapper`

Load statements should now use the new paths:
- `load("//tools/bazel:release.bzl", ...)` (instead of `//tools:release.bzl`)
- `load("//tools/openapi:openapi_client.bzl", ...)` (instead of `//tools:openapi_client.bzl`)
- `load("//tools/bazel:pytest.bzl", ...)` (instead of `//tools:pytest.bzl`)
