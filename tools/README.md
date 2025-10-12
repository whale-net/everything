# Tools# Tools



This directory contains Bazel tools and utilities for the monorepo.This directory contains Bazel tools and utilities for the monorepo.



## Directory Structure# Tools



- **`bazel/`** - Bazel rules and macros (`.bzl` files)This directory contains Bazel tools and utilities for the monorepo.

  - `release.bzl` - Release system and container image generation

  - `container_image.bzl` - Multiplatform OCI image creation## Release System

  - `platforms.bzl` - Platform definitions for cross-compilation

  - `pytest.bzl` - Python testing rulesThe release system uses standard Bazel binaries (`py_binary`, `go_binary`) with the `release_app` macro to create container images and deployment metadata.

- **`openapi/`** - OpenAPI specification and client generation

  - `openapi.bzl` - OpenAPI spec generation from FastAPI apps### Example Usage

  - `openapi_client.bzl` - Client generation rule

  - `openapi_gen_wrapper.sh` - OpenAPI Generator wrapper script```starlark

- **`scripts/`** - Utility scriptsload("@rules_python//python:defs.bzl", "py_binary")

  - `version_resolver.py` - Helm chart version resolutionload("//tools:release.bzl", "release_app")

  - `test_cross_compilation.sh` - Multiplatform image verification

  - `python_runner.py` - Python execution wrapper# Standard py_binary - no wrapper needed!

- **`helm/`** - Helm chart generation and Kubernetes manifest managementpy_binary(

- **`release_helper/`** - Release management tools and utilities    name = "my_app",

- **`client_codegen/`** - OpenAPI client code generation    srcs = ["main.py"],

- **`cacerts/`** - CA certificates for container images    deps = ["@pypi//:fastapi"],

)

## Release System

# Add release metadata and container image generation

The release system uses standard Bazel binaries (`py_binary`, `go_binary`) with the `release_app` macro to create container images and deployment metadata.release_app(

    name = "my_app",

### Example Usage    language = "python",

    domain = "demo",

```starlark    app_type = "external-api",  # external-api, internal-api, worker, or job

load("//tools/bazel:release.bzl", "release_app")    port = 8000,                # Port app listens on

load("@rules_python//python:defs.bzl", "py_binary"))

```

# Standard py_binary - no wrapper needed!

py_binary(**Cross-compilation**: Build for different platforms using `--platforms` flag:

    name = "my_app",```bash

    srcs = ["main.py"],bazel build //app:my_app --platforms=//tools:linux_x86_64

    deps = ["@pypi//:fastapi"],bazel build //app:my_app --platforms=//tools:linux_arm64

)```



# Add release metadata and container image generation## Container Image Tools

release_app(

    name = "my_app",### multiplatform_image (`container_image.bzl`)

    language = "python",Creates OCI container images with multiplatform support (AMD64 and ARM64).

    domain = "demo",

    app_type = "external-api",  # external-api, internal-api, worker, or jobAutomatically used by `release_app` macro - no need to call directly in most cases.

    port = 8000,                # Port app listens on

)## Subdirectories

```

- **`helm/`** - Helm chart generation and Kubernetes manifest management

**Cross-compilation**: Build for different platforms using `--platforms` flag:- **`release_helper/`** - Release management tools and utilities

```bash

bazel build //app:my_app --platforms=//tools:linux_x86_64## Release Helper

bazel build //app:my_app --platforms=//tools:linux_arm64

```The release helper (`release_helper.py`) is a comprehensive tool for managing app releases and container images.



## Container Image Tools### Key Commands

```bash

### multiplatform_image (`bazel/container_image.bzl`)# List all apps with release metadata

Creates OCI container images with multiplatform support (AMD64 and ARM64).bazel run //tools:release -- list



Automatically used by `release_app` macro - no need to call directly in most cases.# Detect apps that have changed since last tag

bazel run //tools:release -- changes

## Release Helper

# Build and load a container image for an app

The release helper (`release_helper.py`) is a comprehensive tool for managing app releases and container images.bazel run //tools:release -- build <app_name>



### Key Commands# Release an app with version and optional commit tag

```bashbazel run //tools:release -- release <app_name> --version <version> --commit <sha>

# List all apps with release metadata

bazel run //tools:release -- list# Plan a release (used by CI)

bazel run //tools:release -- plan --event-type tag_push --version <version>

# Detect apps that have changed since last tag```

bazel run //tools:release -- changes

The release helper ensures consistent handling of container images, version validation, and integration with CI/CD workflows.

# Build and load a container image for an app
bazel run //tools:release -- build <app_name>

# Release an app with version and optional commit tag
bazel run //tools:release -- release <app_name> --version <version> --commit <sha>

# Plan a release (used by CI)
bazel run //tools:release -- plan --event-type tag_push --version <version>
```

The release helper ensures consistent handling of container images, version validation, and integration with CI/CD workflows.

## Migration Notes

For backward compatibility, aliases are provided at the top level:
- `//tools:release` → `//tools/release_helper:release_helper`
- `//tools:version_resolver` → `//tools/scripts:version_resolver`
- `//tools:test_cross_compilation` → `//tools/scripts:test_cross_compilation`
- `//tools:openapi_gen_wrapper` → `//tools/openapi:openapi_gen_wrapper`

Load statements should now use the new paths:
- `load("//tools/bazel:release.bzl", ...)` (instead of `//tools:release.bzl`)
- `load("//tools/openapi:openapi_client.bzl", ...)` (instead of `//tools:openapi_client.bzl`)
- `load("//tools/bazel:pytest.bzl", ...)` (instead of `//tools:pytest.bzl`)
