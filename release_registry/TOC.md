# Release Registry — TOC

Application release registry: gRPC service that tracks app metadata, git commits, container artifacts, and environment promotions.

## Overview

- [ARCHITECTURE.md](ARCHITECTURE.md) — Service role in the release pipeline, data model, Resolve flow for ArgoCD
- [ENV.md](ENV.md) — Runtime configuration (database URL, gRPC auth, etc.)

## Proto Schema

- [protos/registry.proto](protos/registry.proto) — Messages: AppMetadata, RegisterApp/RegisterCommit/RegisterArtifact/Promote/Resolve RPCs
- `//release_registry/protos` — Go proto library target (`go_proto_library` + `proto_library`)

## Migrations

- *TBD* — PostgreSQL schema for apps, commits, artifacts, promotions (SCD2 promotion history)

## gRPC Service

- *TBD* — Server implementation, interceptors, DB layer
- *TBD* — Client credentials and server JWT interceptor wiring (`//libs/go/grpcauth`)

## Auth Interceptors

- *TBD* — gRPC auth policy for Resolve (open) vs Register/Promote (service account only)

## CLI Wrapper

- *Tbd* — CLI commands that call the registry service (register, resolve, promote)
