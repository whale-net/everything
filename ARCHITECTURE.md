# Everything Monorepo — Architecture

> How apps are built, packaged, and delivered in this repo.
> Read this before adding a new app, library, or modifying the release pipeline.

## Overview

A Bazel monorepo with Python and Go support. Every deployable app follows the same pipeline: source → `release_app` macro → OCI image → Helm chart → Kubernetes. The tooling is designed so adding a new app requires minimal boilerplate.

## Repository Structure

```
├── manmanv2/                    # Game server orchestration platform (Go, active)
├── manman/                      # Legacy V1 orchestration system (Python, maintenance)
├── friendly_computing_machine/  # Slack bot with Temporal workflows
├── libs/                        # Shared libraries (Python + Go)
├── tools/                       # Build, release, Helm, and dev tooling
├── demo/                        # Example applications
├── generated/                   # Auto-generated OpenAPI clients (do not edit)
├── docs/                        # Cross-cutting infrastructure docs
├── .github/                     # CI/CD workflows
└── MODULE.bazel                 # Bazel module and dependency configuration
```

## App Delivery Building Blocks

### `release_app` — Making an App Releasable

The `release_app` macro in `tools/release.bzl` is the entry point for the delivery pipeline. Wrap any `py_binary` or `go_binary` with it:

```starlark
load("//tools:release.bzl", "release_app")

release_app(
    name = "my-app",
    binary_target = ":my_app",
    language = "python",  # or "go"
    domain = "manman",    # groups apps in image names and release tooling
    description = "What this app does",
)
```

This generates:
- **`my-app_metadata`** — JSON discovery target used by release tooling and Helm
- **`my-app_image`** — Multi-platform OCI image index (amd64 + arm64)
- **`my-app_image_load`** — Loads a single-arch image into local Docker
- **`my-app_image_push`** — Pushes multi-platform index to GHCR

See [`docs/RELEASE.md`](docs/RELEASE.md) for full parameter reference and release workflow.

### OCI Images — Multi-Platform Containers

All images are built for both `linux/amd64` and `linux/arm64`. Images are named `<domain>-<app>:<version>`.

Platform targets are defined in `tools/platforms.bzl`:
- `//tools:linux_x86_64`
- `//tools:linux_arm64`

```bash
# Load into local Docker (must specify platform)
bazel run //myapp:my-app_image_load --platforms=//tools:linux_arm64

# Push multi-platform index to registry
bazel run //myapp:my-app_image_push
```

> ⚠️ Python apps use `rules_pycross` for platform-specific wheel resolution. ARM64 breakage is **silent at build time** — the container crashes at runtime. See [`docs/DOCKER.md`](docs/DOCKER.md).

### `helm_chart` — Kubernetes Manifests

The `helm_chart` rule in `tools/helm.bzl` generates a Helm chart from one or more `release_app` targets. App type controls which K8s resources are generated:

| App Type | Deployment | Service | Ingress | Job |
|----------|-----------|---------|---------|-----|
| `external-api` | ✓ | ✓ | ✓ | — |
| `internal-api` | ✓ | ✓ | — | — |
| `worker` | ✓ | — | — | — |
| `job` | — | — | — | ✓ |

```starlark
load("//tools:helm.bzl", "helm_chart")

helm_chart(
    name = "my-app_chart",
    apps = [":my-app"],
    environment = "prod",
)
```

See [`tools/helm/README.md`](tools/helm/README.md) and [`tools/helm/APP_TYPES.md`](tools/helm/APP_TYPES.md) for full detail.

## Language Conventions

### Python

- Dependencies managed via `uv` and declared in `pyproject.toml` / `uv.lock`
- `uv.lock` contains pre-resolved wheels for all platforms — required for cross-compilation
- Bazel deps use `@pypi//package_name` syntax
- Cross-platform wheel selection handled by `rules_pycross` (transparent, but must not be broken)

### Go

- Dependencies managed via `bzlmod` in `MODULE.bazel` and `go.mod`
- Go binaries are statically linked — no platform-specific runtime deps, cross-compilation is straightforward
- Bazel deps use standard `go_deps` from `gazelle`

## Generated Code

OpenAPI clients are generated from specs via the `openapi_client` Bazel rule and committed to `//generated/`. Do not edit generated files directly.

```bash
# Sync generated clients to local filesystem for IDE support
./tools/scripts/sync_generated_clients.sh
```

See [`tools/client_codegen/README.md`](tools/client_codegen/README.md).

## CI/CD

GitHub Actions (`.github/workflows/`) handles:
- **PR checks**: build + test on every PR, including `image-integration` tests that verify cross-compilation
- **Release**: triggered manually via workflow dispatch — specify `apps` (csv or `all`), `version` (semver), `dry_run`

If `image-integration` tests fail on a PR, **do not merge** — this indicates a cross-compilation regression.

See [`docs/CI_CD.md`](docs/CI_CD.md) and [`docs/RELEASE.md`](docs/RELEASE.md).

## Local Development

Tilt (`tools/tilt/`) orchestrates local Kubernetes for development. Each domain has its own `Tiltfile`. See [`tools/tilt/README.md`](tools/tilt/README.md) and individual domain READMEs.
