# Tools

This directory contains Bazel tools and utilities for the monorepo.

## Binary Wrappers

Binary wrappers create standard binaries with metadata support through the `AppInfo` provider. This metadata (args, port, app_type) is automatically extracted by the release system, eliminating duplication between binary and deployment configuration.

Cross-compilation happens automatically when building with different `--platforms` flags.

### multiplatform_py_binary (`python_binary.bzl`)
Simplified wrapper for Python binaries that creates a standard `py_binary` with metadata.

```starlark
load("//tools:python_binary.bzl", "multiplatform_py_binary")

multiplatform_py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = [":app_lib", "@pypi//:fastapi"],
    args = ["start-server"],  # Command to run (baked into binary)
    port = 8000,              # Port app listens on (for APIs)
    app_type = "external-api", # Type: external-api, internal-api, worker, or job
)
```

Creates these targets:
- `my_app` - Standard py_binary (works on any platform)
- `my_app_info` - AppInfo provider with metadata (used by release_app)

**Cross-compilation**: Build for different platforms using `--platforms` flag:
```bash
bazel build //app:my_app --platforms=//tools:linux_x86_64
bazel build //app:my_app --platforms=//tools:linux_arm64
```

**AppInfo Metadata**: The `args`, `port`, and `app_type` are automatically extracted by `release_app`, so you don't need to specify them twice. They're intrinsic to the application code:
- `args` - How to run this binary
- `port` - What port the application listens on (from your code like `uvicorn.run(..., port=8000)`)
- `app_type` - What kind of service this is (API, worker, job)

### multiplatform_go_binary (`go_binary.bzl`)
Simplified wrapper for Go binaries that creates a standard `go_binary` with metadata.

```starlark
load("//tools:go_binary.bzl", "multiplatform_go_binary")

multiplatform_go_binary(
    name = "my_app",
    srcs = ["main.go"],
    deps = ["//libs/go"],
    port = 8080,               # Port app listens on (for APIs)
    app_type = "external-api", # Type: external-api, internal-api, worker, or job
)
```

Creates these targets:
- `my_app` - Standard go_binary (works on any platform)
- `my_app_info` - AppInfo provider with metadata (used by release_app)

**Cross-compilation**: Build for different platforms using `--platforms` flag:
```bash
bazel build //app:my_app --platforms=//tools:linux_x86_64
bazel build //app:my_app --platforms=//tools:linux_arm64
```

**Note**: Go binaries typically don't use `args` since they're compiled executables. Command-line flags are handled via the Go `flag` package.

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
