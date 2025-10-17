# Setup Guide

This guide covers the prerequisites and installation steps for the Everything monorepo.

## Prerequisites

- **Bazel 8.3+** with bzlmod support (specified in `.bazelversion`)
  - Install via [Bazelisk](https://github.com/bazelbuild/bazelisk) for automatic version management
  - Bazelisk will automatically download the correct Bazel version
- **Docker** (for building and running container images)
- **Git** (for version control and change detection)
- **Python Virtual Environment** (recommended for development)
  ```bash
  # Create a virtual environment using uv
  uv venv
  # Activate the virtual environment
  source .venv/bin/activate  # On Linux/macOS
  # On Windows: .venv\Scripts\activate
  ```

## Installation

```bash
# Install Bazelisk (manages Bazel versions automatically)
# On macOS and Linux
brew install bazelisk

# Verify installation (will auto-download Bazel 8.3.1)
bazel version
```

## Verify Setup

After installation, verify everything works:
```bash
# Check Bazel version
bazel version

# Test build system
bazel build //demo/hello_python:hello-python

# Run a quick test
bazel test //demo/hello_python:test_main

# Verify release system discovery
bazel query "kind('app_metadata', //...)"
```

## Building and Testing

```bash
# Run applications
bazel run //demo/hello_python:hello-python
bazel run //demo/hello_go:hello-go
bazel run //demo/hello_fastapi:hello-fastapi
bazel run //demo/hello_world_test:hello_world_test

# Build all targets
bazel build //...

# Run tests with detailed output
bazel test //... 
# Run specific tests
bazel test //demo/hello_python:test_main 
bazel test //demo/hello_go:main_test
bazel test //demo/hello_fastapi:test_main
bazel test //demo/hello_world_test:test_main
```
