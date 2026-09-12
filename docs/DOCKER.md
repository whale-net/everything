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

## Runtime Cross-Compilation Validation (not just a build check)

A successful `bazel build` of an arm64 image proves nothing about whether
the binary inside it actually runs — that's exactly what makes ARM64
cross-compilation breakage silent at build time (see the warning at the
top of this doc). Actually running the arm64 artifact, not just building
it, is what an `image-integration` test must do.

**Go binaries built with `CGO_ENABLED=0` (the default for every `go_binary`
release image in this repo) are statically linked**, so once you have the
compiled binary in hand, running it cross-arch needs no Docker container
and no base-image libc at all — only `binfmt_misc` needs `qemu-aarch64`
registered on the host:

```bash
# One-time per host/CI runner (idempotent):
docker run --rm --privileged multiarch/qemu-user-static --reset -p yes
# In GitHub Actions, use docker/setup-qemu-action instead -- wired into
# this repo's .github/actions/setup-build-env as the setup-qemu input.

# Now an arm64 ELF just... runs, transparently emulated:
bazel build //some/domain:some_binary --platforms=//tools:linux_arm64
bazel-bin/some/domain/some_binary_/some_binary --help
```

For a binary that ships as a release image rather than a plain
`go_binary`, extract it straight out of the built `oci_image_index`
(`google/go-containerregistry`'s `pkg/v1/layout` package can open the OCI
layout directory and walk to a specific platform's manifest/layers) instead
of `docker load`-ing it — this sidesteps the `_load` target's `--platforms`
flag entirely, since `oci_image_index`'s own `platforms` attribute already
builds every platform in a single invocation. See
`krill/imageintegration/image_integration_test.go` (issue #2499) for a
worked example: it extracts `migrate`/`api`/`mcp` for both platforms this
way and runs each one for real (a live Postgres migration, an HTTP
`/healthz`, an MCP handshake) rather than just inspecting the ELF header's
architecture field.

A domain whose binary talks to Postgres or another dockerized dependency
still needs a live Docker daemon for *that* — Docker itself is not needed
to exec the cross-compiled binary, only for whatever it talks to. Tag such
a test the same way `//libs/go/dbtest:postgres_constraints_test` does
(`manual`, `no-sandbox`, `no-cache`, `external`, `requires-network`) plus
`image-integration`, and see `krill/ARCHITECTURE.md` "Cross-compilation and
image-integration validation" for how it gets discovered and run in CI
without being hardcoded into a target list.

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
- **Main branch pushes**: Build images but do not push to registry (validation only)
- **Release workflow**: Creates and pushes version-specific (`:v1.2.3`) and commit-specific (`:abc123def`) tags, plus updates `:latest`

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
