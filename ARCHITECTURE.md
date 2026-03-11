# Everything Monorepo — Architecture

> System design and structure for the Everything monorepo.
> Read this before making cross-cutting or structural changes.

## Overview

A Bazel monorepo with Python and Go support, automated release management, Helm chart generation, and multi-platform container builds. The primary application is ManMan, a game server orchestration platform split across a legacy V1 (Python) and an active V2 (Go).

- **ManMan V1** (`//manman`) — Legacy Python services, maintenance mode
- **ManMan V2** (`//manmanv2`) — Current Go services, actively developed

**Start with V2 for all new ManMan development.**

## Repository Structure

```
├── manmanv2/                    # Active game server orchestration platform (Go)
│   ├── api/                     # Control plane gRPC API
│   ├── processor/               # Event processor service
│   ├── host/                    # Bare metal host manager
│   ├── log-processor/           # Log aggregation service
│   ├── migrate/                 # Database migration runner
│   ├── ui/                      # Web management interface
│   ├── protos/                  # Protocol buffer definitions
│   ├── testdata/                # Integration test fixtures
│   └── examples/                # e.g. external-subscriber pattern
│
├── manman/                      # Legacy V1 orchestration system (Python, maintenance)
│   ├── src/                     # Python services (host, worker, repository)
│   ├── management-ui/           # Go web interface
│   ├── clients/                 # Generated API clients
│   └── docs/                    # V1 feature docs
│
├── friendly_computing_machine/  # Slack bot with Temporal workflows
│
├── libs/                        # Shared libraries
│   ├── python/                  # alembic, cli, gunicorn, logging, openapi_gen, rmq, retry
│   └── go/                      # htmxauth, htmxbase, logging
│
├── tools/                       # Build, release, Helm, and dev tooling
│   ├── helm/                    # Bazel Helm chart generation
│   ├── tilt/                    # Local Kubernetes dev orchestration
│   ├── tilt-mcp/                # Tilt MCP integration
│   ├── client_codegen/          # OpenAPI client generation
│   └── release_helper/          # Release automation CLI
│
├── demo/                        # Example applications (hello_python, hello_go, hello_fastapi, etc.)
├── generated/                   # Auto-generated OpenAPI clients (do not edit)
├── docs/                        # Cross-cutting infrastructure docs
├── .github/                     # CI/CD workflows
└── MODULE.bazel                 # Bazel dependency configuration
```

## Core Principles

- **Bazel-First**: All build, test, and release operations use Bazel. No raw `go build` or `pip install` in CI.
- **True Cross-Compilation**: Python uses rules_pycross for platform-specific wheel resolution. ARM64 breakage is silent at build time. See [`docs/DOCKER.md`](docs/DOCKER.md).
- **Container-Native**: All deployable apps produce OCI images via `release_app`. Images follow `<domain>-<app>:<version>` naming.
- **Release Automation**: `release_app` macro + GitHub Actions handles multi-arch publishing and change detection.

## Build System

The `release_app` Bazel macro (defined in `tools/release.bzl`) is the primary entry point for making an app releasable. It generates:
- Release metadata for app discovery
- Multi-platform OCI image targets (amd64 + arm64)
- Push targets for publishing to GHCR

Helm charts are generated automatically from app metadata. See [`tools/TOC.md`](tools/TOC.md) for build system docs.

## ManMan V2 Architecture

Split-plane design — control plane in K8s, execution plane on bare metal:

```
CONTROL PLANE (K8s)
  API Server (gRPC) ──┐
  Event Processor ────┼── PostgreSQL + RabbitMQ + S3
  Migration Job ──────┘
        │
    RabbitMQ (commands/status)
        │
EXECUTION PLANE (Bare Metal)
  Host Manager ── Docker SDK ── Game Containers
```

See [`manmanv2/ARCHITECTURE.md`](manmanv2/ARCHITECTURE.md) for full detail: data model, communication patterns, gRPC definitions, orphan prevention strategy.

## Finding Things

| Looking for... | Go to |
|----------------|-------|
| ManManV2 components and design | [manmanv2/TOC.md](manmanv2/TOC.md) |
| Shared libraries (Python + Go) | [libs/TOC.md](libs/TOC.md) |
| Build system, release, Helm, CI | [docs/TOC.md](docs/TOC.md) |
| Dev tooling (Tilt, codegen) | [tools/TOC.md](tools/TOC.md) |
| Legacy V1 system | [manman/TOC.md](manman/TOC.md) |
| Slack bot / Temporal | [friendly_computing_machine/TOC.md](friendly_computing_machine/TOC.md) |
