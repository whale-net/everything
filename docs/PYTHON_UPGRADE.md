# Python Version Upgrade Guide

## Overview

Python versions are managed in two places in `MODULE.bazel`:
1. **Standard Python** (`rules_python` toolchain) - Used for Bazel builds and development
2. **Optimized Python** (`python-build-standalone`) - Used for production container images

## Upgrade Steps

### 1. Update Standard Python Version

```starlark
# MODULE.bazel
python = use_extension("@rules_python//python/extensions:python.bzl", "python")
python.toolchain(
    is_default = True,
    python_version = "3.13",  # Change this
)
```

### 2. Update Optimized Python for Containers

Find the latest release at: https://github.com/astral-sh/python-build-standalone/releases

Update both architectures in `MODULE.bazel`:

```starlark
http_archive(
    name = "python_stripped_x86_64",
    url = "https://github.com/astral-sh/python-build-standalone/releases/download/YYYYMMDD/cpython-X.Y.Z%2BYYYYMMDD-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
    build_file_content = """
exports_files(["python"])
""",
)

http_archive(
    name = "python_stripped_arm64",
    url = "https://github.com/astral-sh/python-build-standalone/releases/download/YYYYMMDD/cpython-X.Y.Z%2BYYYYMMDD-aarch64-unknown-linux-gnu-install_only_stripped.tar.gz",
    build_file_content = """
exports_files(["python"])
""",
)
```

**Important**: Use `install_only_stripped` variants for production (not debug builds).

### 3. Update Python Path in Container Image Builder

Update the Python installation path in `tools/bazel/container_image.bzl`:

```starlark
# Find this line in _python_layer genrule
/opt/python3.13/  # Change to new minor version (e.g., /opt/python3.14/)
```

Search for all occurrences and update:
- Python layer extraction path
- Symlink creation commands
- App layer Python symlink target

### 4. Verify

```bash
# Test build
bazel build //demo/hello_python:hello_python

# Test container
bazel run //demo/hello_python:hello_python_image_load --platforms=//tools:linux_x86_64
docker run --rm hello-python:latest
```

## Version Selection

- **Standard Python**: Choose major.minor (e.g., `3.13`)
- **Optimized Python**: Must match specific patch version (e.g., `3.13.9`)

Keep both versions aligned on major.minor to avoid compatibility issues.

## Why Two Versions?

- **Standard Python**: Hermetic Bazel toolchain, consistent builds across platforms
- **Optimized Python**: Stripped production builds (~20MB vs ~108MB), better for containers
