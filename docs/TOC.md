# Build System & Infrastructure — TOC

Cross-cutting documentation for the Bazel build system, release pipeline, and infrastructure tooling.

## Setup & Development

- [SETUP.md](SETUP.md) — Prerequisites, installation, first-time environment setup
- [DEVELOPMENT.md](DEVELOPMENT.md) — Workflow for adding apps, libraries, and BUILD.bazel targets
- [TESTING.md](TESTING.md) — Running tests with Bazel
- [CONFIGURATION.md](CONFIGURATION.md) — Build settings and .bazelrc configuration
- [DEPENDENCIES.md](DEPENDENCIES.md) — Managing Python (uv) and Go (bzlmod) dependencies
- [PYTHON_UPGRADE.md](PYTHON_UPGRADE.md) — Upgrading the Python version across the repo
- [GO_DEPENDENCIES.md](GO_DEPENDENCIES.md) — Adding Go dependencies with bzlmod

## Containers & Images

- [DOCKER.md](DOCKER.md) — **⚠️ Read before touching images.** OCI build system, cross-compilation, platform targets
- [IMAGE_LAYER_CACHING.md](IMAGE_LAYER_CACHING.md) — Container layer caching strategy with Bazel

## Release & CI/CD

- [RELEASE.md](RELEASE.md) — Release system: `release_app` macro, change detection, multi-arch publishing
- [HELM.md](HELM.md) — Automatic Helm chart generation from app metadata
- [HELM_RELEASE.md](HELM_RELEASE.md) — Helm chart release integration with GitHub Actions
- [CI_CD.md](CI_CD.md) — GitHub Actions workflow overview
- [CLEANUP.md](CLEANUP.md) — Tag, release, and GHCR package cleanup tooling

## Libraries & Integrations

- [ALEMBIC_CONSOLIDATION.md](ALEMBIC_CONSOLIDATION.md) — Consolidated Alembic migration library (use `//libs/python/alembic`)
- [LOGGING_AUTO_DETECTION.md](LOGGING_AUTO_DETECTION.md) — How the logging library auto-detects OTLP vs console output
- [LOGGING_ENV_VARS.md](LOGGING_ENV_VARS.md) — Environment variables controlling logging behavior
- [STEAMCMD_INTEGRATION.md](STEAMCMD_INTEGRATION.md) — SteamCMD tool packaging

## Implementation Plans & Feature Docs

- [INSTANCE_DRILLDOWN_IMPLEMENTATION.md](INSTANCE_DRILLDOWN_IMPLEMENTATION.md) — Game server instance drill-down page plan

## Archive

Historical docs in [archive/](archive/) — preserved for context, not actively maintained.
