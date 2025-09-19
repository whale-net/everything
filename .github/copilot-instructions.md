# GitHub Copilot Instructions for Everything Monorepo

**ALWAYS reference these instructions first and fallback to search or bash commands only when you encounter unexpected information that does not match the info here.**

## Critical Dependency and Network Requirements

### Internet Access Required
**CRITICAL**: This repository requires internet access to `bcr.bazel.build` (Bazel Central Registry) for dependency resolution. **DO NOT attempt to build if internet access is limited or network connectivity to bcr.bazel.build fails**.

**If Bazel fails with "Unknown host: bcr.bazel.build" or similar network errors:**
- Document this as a known limitation
- Do not attempt workarounds - the build will fail
- Refer to the manual installation commands below that work in connected environments

## Working Effectively 

### Bootstrap and Build (Requires Internet Access)
**CRITICAL TIMING**: Set timeouts of 60+ minutes for builds. **NEVER CANCEL** build operations.

```bash
# Prerequisites - verify network connectivity first
curl -s --fail https://bcr.bazel.build/ || echo "ERROR: BCR unreachable - builds will fail"

# Method 1: Use GitHub Actions setup-bazel action (recommended in CI)
# - Uses: bazel-contrib/setup-bazel@0.15.0
# - Automatically handles caching and multiple Bazel versions

# Method 2: Manual Installation (for local development)
wget https://github.com/bazelbuild/bazel/releases/download/8.0.0/bazel-8.0.0-linux-x86_64
chmod +x bazel-8.0.0-linux-x86_64
sudo mv bazel-8.0.0-linux-x86_64 /usr/local/bin/bazel

# Verify Bazel version matches .bazelversion (8.3.1)
bazel version

# Initial setup - download and compile dependencies
# NEVER CANCEL: First build takes 20-40 minutes (downloads deps, compiles everything)
bazel build //...  # Set timeout to 3600+ seconds (60+ minutes)

# Run all tests - NEVER CANCEL: Takes 5-15 minutes
bazel test //...  # Set timeout to 1800+ seconds (30+ minutes)

# Subsequent builds are much faster due to caching (2-5 minutes typically)
```

### Run Applications (After Successful Build)
```bash
# Run demo applications
bazel run //demo/hello_python:hello_python
bazel run //demo/hello_go:hello_go  
bazel run //demo/hello_fastapi:hello_fastapi
bazel run //demo/hello_world_test:hello_world_test
```

### Build Container Images (After Successful Build)
```bash
# Build and load images efficiently using oci_load targets
bazel run //demo/hello_python:hello_python_image_load
bazel run //demo/hello_go:hello_go_image_load

# Test the containers (validation scenario)
docker run --rm hello_python:latest
docker run --rm hello_go:latest

# Use release tool for production workflows
bazel run //tools:release -- build hello_python
```

## Validation and Testing

### Manual Validation Scenarios
**ALWAYS run these validation steps after making changes to apps:**

1. **Python App Validation**:
   ```bash
   # Build and run the Python app
   bazel run //demo/hello_python:hello_python
   # Expected output: "Hello, world from uv and Bazel BASIL test from Python!"
   # Expected output: "Version: 1.0.0"
   ```

2. **Go App Validation**:
   ```bash
   # Build and run the Go app
   bazel run //demo/hello_go:hello_go
   # Expected output: "Hello, world from Bazel from Go!"  
   # Expected output: "Version: 1.0.0"
   ```

3. **FastAPI App Validation**:
   ```bash
   # Start the FastAPI server (runs on port 8000)
   bazel run //demo/hello_fastapi:hello_fastapi &
   SERVER_PID=$!
   sleep 3
   # Test the API endpoint
   curl http://localhost:8000/
   # Expected output: {"message":"hello world"}
   kill $SERVER_PID
   ```

4. **Container Image Validation**:
   ```bash
   # Build container and verify it runs
   bazel run //demo/hello_python:hello_python_image_load
   docker run --rm hello_python:latest
   # Should output the same as the direct bazel run
   ```

### CI/CD Pipeline Integration
**Always run these commands before committing:**
```bash
# Run all tests with CI configuration (timeout: 30+ minutes, NEVER CANCEL)
bazel test //... --config=ci

# Verify release system discovery
bazel query "kind('app_metadata', //...)"

# Test release tool functionality
bazel run //tools:release -- list
bazel run //tools:release -- changes

# Plan a test release (dry run)
bazel run //tools:release -- plan --event-type workflow_dispatch --apps all --version v1.0.0

# Test building specific app image
bazel run //tools:release -- build hello_python
```

### CI Pipeline Commands (Reference)
The CI system (`.github/workflows/ci.yml`) uses these exact commands:
```bash
# Test phase
bazel test //...

# Docker build planning
bazel run //tools:release -- plan --event-type pull_request --base-commit=$BASE_COMMIT --format github

# Docker building per app
bazel run //tools:release -- build $APP_NAME

# Release (main branch only)
bazel run //tools:release -- release $APP_NAME --version latest --commit $GITHUB_SHA
```

## Repository Structure and Key Locations

### Main Directories
```
├── .github/               # CI/CD workflows and actions
│   ├── workflows/         # ci.yml (build/test) and release.yml
│   └── actions/           # setup-build-env reusable action
├── demo/                  # Example applications (hello_python, hello_go, etc.)  
├── libs/                  # Shared libraries (//libs/python, //libs/go)
├── tools/                 # Release system and build utilities
├── docker/                # Dockerfile templates
├── BUILD.bazel           # Root build configuration
├── MODULE.bazel          # Bazel dependencies (requires BCR access)
├── AGENT.md              # Comprehensive agent guidelines
└── README.md             # Project documentation
```

### Critical Files for Development
- **`MODULE.bazel`**: External dependencies (requires internet to BCR)
- **`tools/release.bzl`**: Release system with `release_app` macro
- **`.bazelrc`**: Build configuration and CI optimizations
- **`requirements.lock.txt`**: Python dependencies (regenerate with `bazel run //:pip_compile`)

### Application Structure Pattern
Each app follows this structure:
```
demo/app_name/
├── BUILD.bazel           # Contains py_binary/go_binary, tests, and release_app
├── main.py/.go           # Application entry point
├── test_main.py/_test.go # Unit tests  
└── __init__.py           # Python package marker
```

## Development Workflow

### Adding New Applications
1. Create directory under appropriate domain (e.g., `demo/`, `api/`)
2. Add `BUILD.bazel` with binary, test, and `release_app` targets
3. Always include `release_app` macro for release system integration
4. Follow naming convention: binary name matches directory name

### Bazel Target Patterns
```bash
# Build specific app
bazel build //demo/hello_python:hello_python

# Build all apps in demo/
bazel build //demo/...

# Run tests for specific app  
bazel test //demo/hello_python:test_main

# Query release apps
bazel query "kind('app_metadata', //...)"
```

### Release System Commands
```bash
# List all apps with release metadata
bazel run //tools:release -- list

# Detect changed apps
bazel run //tools:release -- changes

# Build container for app
bazel run //tools:release -- build app_name

# Plan a release  
bazel run //tools:release -- plan --event-type workflow_dispatch --apps all --version v1.0.0

# Dry run release
bazel run //tools:release -- release app_name --version v1.2.3 --dry-run
```

## Common Issues and Limitations

### Known Limitations
1. **Network Dependency**: Cannot build without access to bcr.bazel.build
2. **Bazel Version**: Requires Bazel 8.0.0+ (configured in `.bazelversion`)
3. **Docker Required**: Container operations need Docker daemon running

### Build Failures
- **"Unknown host: bcr.bazel.build"**: Network connectivity issue - builds will fail
- **Module resolution errors**: Check MODULE.bazel dependencies and network access
- **Cache issues**: Use `bazel clean` if you encounter stale build results

### Performance Notes
- **Build caching**: Bazel caches aggressively - only changed targets rebuild
- **Test caching**: Tests are cached by default (configured in `.bazelrc`)
- **Container builds**: Use `oci_load` targets for faster local development

## Common Tasks and File Outputs

### Repository Root Files (Reference)
```bash
# ls -la output (for quick reference without searching)
.bazelrc              # Bazel configuration 
.bazelversion         # Required Bazel version (8.3.1)
BUILD.bazel           # Root build file
MODULE.bazel          # Dependencies (requires BCR internet access)
MODULE.bazel.lock     # Dependency lock file
go.mod               # Go module configuration
requirements.in      # Python dependencies source
requirements.lock.txt # Locked Python dependencies (42KB)
AGENT.md             # Agent instructions (primary reference)
README.md            # Project documentation
```

### Build Files Structure (Reference)
```bash
# find . -name "BUILD.bazel" output
./BUILD.bazel
./demo/BUILD.bazel
./demo/hello_go/BUILD.bazel
./demo/hello_python/BUILD.bazel
./demo/hello_fastapi/BUILD.bazel  
./demo/hello_world_test/BUILD.bazel
./libs/BUILD.bazel
./libs/python/BUILD.bazel
./tools/BUILD.bazel
./tools/release_helper/BUILD.bazel
```

### Python Dependency Management
```bash
# Update Python dependencies (after modifying requirements.in)
bazel run //:pip_compile

# Check requirements files
cat requirements.in        # Source dependencies: pytest, pytest-git, fastapi, uvicorn[standard], httpx, typer
wc -l requirements.lock.txt # 582 lines of locked dependencies
```

## Primary Reference Document
**For comprehensive agent guidelines, ALWAYS consult `AGENT.md`** which contains detailed instructions on:
- Repository architecture and release system patterns
- Development workflows and release management  
- Testing strategies and troubleshooting guidance
- Extension points and maintenance procedures

The `AGENT.md` file provides the authoritative framework for understanding and working with this codebase.