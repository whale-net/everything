# Tools

This directory contains Bazel tools and utilities for the monorepo.

# Tools

This directory contains Bazel tools and utilities for the monorepo.

## Release System

The release system uses standard Bazel binaries (`py_binary`, `go_binary`) with the `release_app` macro to create container images and deployment metadata.

### Example Usage

```starlark
load("@rules_python//python:defs.bzl", "py_binary")
load("//tools:release.bzl", "release_app")

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
