# Simplified Multiplatform Build System

## Overview

This repository now uses a **greatly simplified** approach to building multiplatform container images. The old complex system with platform transitions, multiple binary variants, and wrapper rules has been replaced with the idiomatic Bazel approach.

## The Simplified Approach

### Key Principles

1. **Single Binary Target** - No platform suffixes or variants needed
2. **Platform Flag** - Use `--platforms` to build for different architectures
3. **OCI Image Index** - Combine platform images with `oci_image_index`
4. **Release App Wrapper** - Keep the convenient `release_app` macro

### How It Works

```python
# In your BUILD.bazel - Simple, clean, standard Bazel
multiplatform_py_binary(
    name = "my_app",
    srcs = ["main.py"],
    deps = ["@pypi//:fastapi"],
)

release_app(
    name = "my_app",
    language = "python",
    domain = "demo",
)
```

This creates:
- `my_app` - Standard py_binary (works on any platform)
- `my_app_info` - Metadata for release system
- `my_app_image` - Multiplatform OCI image index
- `my_app_image_amd64` - AMD64-specific image
- `my_app_image_arm64` - ARM64-specific image
- `my_app_image_load` - Load AMD64 image to Docker
- `my_app_image_push` - Push multiplatform image to registry

## Building Images

### For Specific Platforms

```bash
# Build AMD64 image
bazel build //app:my_app_image_amd64 --platforms=//tools:linux_x86_64

# Build ARM64 image
bazel build //app:my_app_image_arm64 --platforms=//tools:linux_arm64
```

### For Local Development

```bash
# Load AMD64 image to Docker (most common dev environment)
bazel run //app:my_app_image_load

# Load specific platform
bazel run //app:my_app_image_amd64_load
bazel run //app:my_app_image_arm64_load
```

### For Release

The release system handles building both platforms automatically:

```bash
# Build multiplatform image (builds both AMD64 and ARM64)
bazel run //tools:release -- build my_app

# Push to registry with tags
bazel run //tools:release -- release my_app --version v1.0.0
```

## What Was Removed

### ❌ Old Complex System

- **Platform transitions** - Custom Starlark rules that changed `--platforms` flag
- **Multiple binary variants** - `_base_amd64`, `_base_arm64`, `_linux_amd64`, `_linux_arm64`
- **Wrapper rules** - `_multiplatform_py_binary_rule` with transition implementation
- **Symlink indirection** - Extra layer of indirection via symlinks
- **Platform-specific parameters** - `binary_amd64` and `binary_arm64` in `multiplatform_image`

### ✅ New Simple System

- **Standard binaries** - Just `py_binary` or `go_binary`
- **Platform flag** - Use Bazel's native `--platforms` flag
- **Single binary parameter** - `multiplatform_image(binary = ":my_app")`
- **OCI image index** - Standard `oci_image_index` with platform map

## Cross-Compilation

### Python

Python cross-compilation works through:
1. **uv.lock** - Contains wheels for all platforms (AMD64, ARM64)
2. **rules_pycross** - Selects correct wheels based on `--platforms` flag
3. **Platform flag** - `--platforms=//tools:linux_arm64` tells Bazel the target

When you build with `--platforms=//tools:linux_arm64`, rules_pycross automatically selects the aarch64 wheels from uv.lock.

### Go

Go cross-compilation works through:
1. **rules_go** - Built-in cross-compilation support
2. **Platform constraints** - Automatically sets GOOS/GOARCH
3. **Platform flag** - `--platforms=//tools:linux_arm64` sets target

Go's compiler natively supports cross-compilation, so this "just works".

## Migration from Old System

If you have existing apps using the old system:

1. **Remove platform suffixes** from binary references in `release_app`
2. **Update BUILD files** to use simplified macros (no changes needed usually)
3. **Keep using `release_app`** - The API is the same!

Example migration:

```python
# OLD - Don't do this anymore
release_app(
    name = "my_app",
    binary_name = "my_app",  # Would create my_app_linux_amd64, my_app_linux_arm64
    language = "python",
    domain = "demo",
)

# NEW - Simplified
release_app(
    name = "my_app",
    binary_name = "my_app",  # Just references :my_app
    language = "python",
    domain = "demo",
)
```

The `release_app` macro handles everything automatically.

## Benefits

1. **Simpler** - No custom transitions or wrapper rules
2. **Idiomatic** - Uses standard Bazel patterns
3. **Maintainable** - Less custom code to maintain
4. **Understandable** - Easier for new developers to understand
5. **Standard** - Follows Bazel best practices
6. **Compatible** - Works with existing release system

## Technical Details

### Container Image Building

The `multiplatform_image` macro:

1. Creates platform-specific image targets with `target_compatible_with`
2. Builds each image with appropriate `--platforms` flag
3. Combines them using `oci_image_index`
4. Provides convenient load/push targets

### Platform Constraints

Platform constraints ensure targets are only built for compatible platforms:

```python
container_image(
    name = "my_app_image_amd64",
    binary = ":my_app",
    target_compatible_with = [
        "@platforms//os:linux",
        "@platforms//cpu:x86_64",
    ],
)
```

This tells Bazel: "Build this target for Linux x86_64 only".

### OCI Image Index

The `oci_image_index` creates a manifest list:

```python
oci_image_index(
    name = "my_app_image",
    images = {
        "linux/amd64": ":my_app_image_amd64",
        "linux/arm64": ":my_app_image_arm64",
    },
)
```

When you `docker pull`, it automatically selects the right platform.

## Summary

The new system is **dramatically simpler** while providing the same functionality:

- ✅ Single binary targets (no platform suffixes)
- ✅ Standard Bazel patterns (no custom transitions)
- ✅ Clean API (single `binary` parameter)
- ✅ Full multiplatform support (AMD64 + ARM64)
- ✅ Compatible with release system
- ✅ Easy to understand and maintain
