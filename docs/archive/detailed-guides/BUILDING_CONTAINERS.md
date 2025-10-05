# Building Multi-Platform Container Images

This document explains how to build multi-platform (AMD64 and ARM64) container images in this repository.

## Quick Start

### Building Images

```bash
# Build for specific platform
bazel run //demo/hello_python:hello_python_image_amd64_load --platforms=//tools:linux_x86_64

# Build for ARM64
bazel run //demo/hello_python:hello_python_image_arm64_load --platforms=//tools:linux_arm64

# Run the container
docker run --rm demo-hello_python_amd64:latest
```

### Using the Release Tool

The release tool handles platform building automatically:

```bash
# Build all platforms for an app
bazel run //tools:release -- build hello_python

# List all apps
bazel run //tools:release -- list
```

## Architecture

### Standard Bazel Rules

We use standard Bazel rules without custom wrappers:

```starlark
load("@rules_python//python:defs.bzl", "py_binary")
load("//tools:release.bzl", "release_app")

# Standard binary - works with any platform
py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = ["@pypi//:fastapi"],
)

# Add container images and release metadata
release_app(
    name = "my_app",
    language = "python",
    domain = "demo",
    app_type = "external-api",
    port = 8000,
)
```

### Platform Selection

Platform is specified at **build time** using Bazel's `--platforms` flag:

```bash
# Build for AMD64
bazel build //app:my_app --platforms=//tools:linux_x86_64

# Build for ARM64  
bazel build //app:my_app --platforms=//tools:linux_arm64
```

Available platforms:
- `//tools:linux_x86_64` - Linux AMD64
- `//tools:linux_arm64` - Linux ARM64

### Cross-Compilation

Cross-compilation works automatically:

- **Python**: `rules_pycross` selects correct wheels based on target platform from `uv.lock`
- **Go**: `rules_go` has native cross-compilation support

### Container Images

The `release_app` macro creates multi-platform images:

**Generated targets:**
- `{name}_image` - Multi-platform image index (oci_image_index)
- `{name}_image_amd64` - AMD64 image (oci_image)
- `{name}_image_arm64` - ARM64 image (oci_image)
- `{name}_image_amd64_load` - Load AMD64 image to Docker (oci_load)
- `{name}_image_arm64_load` - Load ARM64 image to Docker (oci_load)

## Key Concepts

### What Changed (October 2025)

We simplified the build system by removing:
- ❌ Custom wrapper macros (`multiplatform_py_binary`, `multiplatform_go_binary`)
- ❌ Platform transitions (custom Starlark rules)
- ❌ AppInfo provider system
- ❌ Multiple binary variants per app

Replaced with idiomatic Bazel:
- ✅ Standard `py_binary` and `go_binary` rules
- ✅ Direct use of `--platforms` flag
- ✅ Metadata in `release_app` macro
- ✅ Single binary per app

See [docs/archive/migration-2025-10/](docs/archive/migration-2025-10/) for migration history.

### What's Still Valid

These concepts remain valid:
- ✅ `multiplatform_image()` function in `container_image.bzl`
- ✅ `release_app()` macro for release metadata
- ✅ `oci_image_index` for multi-platform manifests
- ✅ Platform-specific toolchains in MODULE.bazel

## CI/CD Integration

The CI pipeline builds and tests multi-platform images:

```yaml
# Build for specific platform
- run: bazel run //demo/hello_python:hello_python_image_amd64_load --platforms=//tools:linux_x86_64
- run: docker run --rm demo-hello_python_amd64:latest
```

The release workflow uses the release tool which handles platforms automatically:

```yaml
- run: bazel run //tools:release -- build ${{ matrix.app }}
- run: bazel run //tools:release -- release ${{ matrix.app }} --version ${{ inputs.version }}
```

## Troubleshooting

### Wrong Architecture Wheels

**Problem**: ARM64 container has x86_64 Python wheels  
**Solution**: Ensure you're using `--platforms` flag:

```bash
# Correct
bazel run //app:app_image_arm64_load --platforms=//tools:linux_arm64

# Wrong (uses host platform)
bazel run //app:app_image_arm64_load
```

### Container Won't Run

**Problem**: "exec format error" when running container  
**Cause**: Built for wrong architecture  
**Solution**: Match container architecture to host or use QEMU:

```bash
# On AMD64 host, run AMD64 container
docker run --rm demo-app_amd64:latest

# On ARM64 host, run ARM64 container  
docker run --rm demo-app_arm64:latest

# Or enable QEMU for cross-architecture
docker run --platform linux/arm64 demo-app_arm64:latest
```

### Build Fails

**Problem**: "No matching toolchain found"  
**Cause**: Platform constraint not satisfied  
**Solution**: Check that the platform definition exists in `tools/platforms.bzl`

## Examples

See the demo apps for complete examples:
- [demo/hello_python/BUILD.bazel](demo/hello_python/BUILD.bazel) - Python app
- [demo/hello_go/BUILD.bazel](demo/hello_go/BUILD.bazel) - Go app
- [demo/hello_fastapi/BUILD.bazel](demo/hello_fastapi/BUILD.bazel) - FastAPI service

## References

- [rules_oci documentation](https://github.com/bazel-contrib/rules_oci)
- [rules_python documentation](https://github.com/bazelbuild/rules_python)
- [rules_go documentation](https://github.com/bazelbuild/rules_go)
- [Bazel platforms documentation](https://bazel.build/extending/platforms)
