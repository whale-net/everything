# Tools

This directory contains Bazel tools and utilities for the monorepo.

## Go Tools

### Initialize Go Cache
```bash
bazel run //:init-go-cache
```
Initializes Go module and build cache directories to prevent warnings in CI/CD environments. This is automatically run during CI builds.

### Go Environment Info
```bash
bazel run //:go-env-info
```
Displays detailed Go environment information including:
- Go version and installation paths
- Cache directory locations and sizes
- Go proxy and checksum database settings

## Usage in CI/CD

The Go cache initialization tool is automatically run in GitHub Actions to prevent cache-related warnings during post-job cleanup. The CI workflow includes proper Go module caching using GitHub Actions cache.

## Available Rules

- `go_cache_init`: Creates a Bazel executable target that initializes Go cache directories
- `go_env_info`: Creates a Bazel executable target that displays Go environment information

These rules are defined in `tools/go.bzl` and can be used in any BUILD.bazel file in the monorepo.
