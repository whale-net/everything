# Tools

This directory contains Bazel tools and utilities for the monorepo.

## Release Helper

The release helper (`release_helper.py`) is a comprehensive tool for managing app releases and container images.

### Key Commands
```bash
# List all apps with release metadata
bazel run //tools:release -- list

# Detect apps that have changed since last tag
bazel run //tools:release -- changes

# Build and load a container image for an app
bazel run //tools:release -- build <app_name>

# Release an app with version and optional commit tag
bazel run //tools:release -- release <app_name> --version <version> --commit <sha>

# Plan a release (used by CI)
bazel run //tools:release -- plan --event-type tag_push --version <version>
```

The release helper ensures consistent handling of container images, version validation, and integration with CI/CD workflows.

## Containerized Distribution

The release helper is deployed as a containerized application using the `release_app` macro:

### Docker Image
- **Image**: `ghcr.io/whale-net/tools-release_helper_app`
- **Distribution**: Built and pushed automatically in CI workflows
- **Usage**: Executed via `docker run` in CI jobs for consistent environment

### CI Integration
The tool is built once per workflow and distributed as a Docker image:
```bash
# In CI: Build and push the release tool image
bazel run //tools/release_helper:release_helper_app_image_push

# In CI: Use the containerized tool
docker run --rm \
  -v "$(pwd):/workspace" \
  -w /workspace \
  ghcr.io/whale-net/tools-release_helper_app:ci-$COMMIT_SHA \
  plan --event-type pull_request --format github
```

### Local Development
For local development, you can still use the tool directly:
```bash
# Standard build and run
bazel run //tools:release -- list

# Or build the containerized version locally
bazel run //tools/release_helper:release_helper_app_image_load
docker run --rm tools-release_helper_app:latest --help
```
