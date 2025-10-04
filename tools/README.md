# Tools

This directory contains Bazel tools and utilities for the monorepo.

## Binary Wrappers

### multiplatform_py_binary (`python_binary.bzl`)
Wrapper for Python binaries that creates platform-specific targets for cross-compilation.

```starlark
load("//tools:python_binary.bzl", "multiplatform_py_binary")

multiplatform_py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = [":app_lib", "@pypi//:fastapi"],
)
```

Creates three targets:
- `my_app_linux_amd64` - Linux x86_64 binary with correct wheel selection
- `my_app_linux_arm64` - Linux ARM64 binary with correct wheel selection
- Platform-specific binaries ensure compiled dependencies (pydantic, numpy, etc.) get the right wheels

### multiplatform_go_binary (`go_binary.bzl`)
Wrapper for Go binaries that creates platform-specific targets for cross-compilation.

```starlark
load("//tools:go_binary.bzl", "multiplatform_go_binary")

multiplatform_go_binary(
    name = "my_app",
    srcs = ["main.go"],
    deps = ["//libs/go"],
)
```

Creates three targets:
- `my_app` - Host platform binary (for local development)
- `my_app_linux_amd64` - Cross-compiled Linux x86_64 binary
- `my_app_linux_arm64` - Cross-compiled Linux ARM64 binary

## Container Image Tools

### multiplatform_image (`container_image.bzl`)
Creates OCI container images with multiplatform support (AMD64 and ARM64).

Automatically used by `release_app` macro - no need to call directly in most cases.

## Subdirectories

- **`helm/`** - Helm chart generation and Kubernetes manifest management
- **`release_helper/`** - Release management tools and utilities

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
