# GitHub Copilot Instructions for Everything Monorepo

- Provide short, straightforward, responses. Elaborate only when necessary.
- Do not apologize for being wrong.
- Do not praise the developer, you are just a tool not a conversation
- Do not attempt to commit on the developers behalf
- If provided with a github link for debugging, try and use github mcp tools

**ALWAYS reference these instructions first and fallback to search or bash commands only when you encounter unexpected information that does not match the info here.**

## Desired Behaviors
- Avoid creating markdown READMEs
- Do not patch production environment - rely on release actions and human inputs

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
bazel run //demo/hello_worker:hello_worker
bazel run //demo/hello_job:hello_job
bazel run //demo/hello_internal_api:hello_internal_api
bazel run //demo/hello_world_test:hello_world_test
```

### Build Container Images (After Successful Build)
```bash
# Build and load images efficiently using oci_load targets
bazel run //demo/hello_python:hello_python_image_load
bazel run //demo/hello_go:hello_go_image_load

# Test the containers (validation scenario)
# Note: Image names are simple (e.g., hello_python_linux_amd64:latest, hello_go:latest)
docker run --rm hello_python_linux_amd64:latest
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

4. **Worker App Validation**:
   ```bash
   # Build and run the worker app (no service exposure)
   bazel run //demo/hello_worker:hello_worker
   # Expected output: Worker-specific output
   ```

5. **Job App Validation**:
   ```bash
   # Build and run the job app (one-time execution)
   bazel run //demo/hello_job:hello_job
   # Expected output: Job-specific output
   ```

6. **Container Image Validation**:
   ```bash
   # Build container and verify it runs
   bazel run //demo/hello_python:hello_python_image_load
   docker run --rm hello_python_linux_amd64:latest
   # Should output the same as the direct bazel run
   ```

7. **Helm Chart Validation**:
   ```bash
   # Build and validate helm chart
   bazel build //demo:demo_chart  # Or any chart target
   helm lint bazel-bin/demo/demo_chart/
   helm template test bazel-bin/demo/demo_chart/ | kubectl apply --dry-run=client -f -
   # Should validate all Kubernetes resources
   ```

### CI/CD Pipeline Integration
**Always run these commands before committing:**
```bash
# Run all tests with CI configuration (timeout: 30+ minutes, NEVER CANCEL)
bazel test //... --config=ci

# Verify release system discovery
bazel query "kind('app_metadata', //...)"

# Verify helm chart discovery
bazel query "kind('helm_chart_metadata', //...)"

# Test release tool functionality
bazel run //tools:release -- list
bazel run //tools:release -- changes
bazel run //tools:release -- list-helm-charts

# Plan a test release (dry run)
bazel run //tools:release -- plan --event-type workflow_dispatch --apps all --version v1.0.0

# Plan helm chart release
bazel run //tools:release -- plan-helm-release --charts all --version v1.0.0

# Test building specific app image
bazel run //tools:release -- build hello_python

# Test building specific helm chart
bazel run //tools:release -- build-helm-chart demo-workers
```

### CI Pipeline Commands (Reference)
The CI system (`.github/workflows/ci.yml` and `release.yml`) uses these exact commands:

**CI Build/Test:**
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

**Helm Chart Release:**
```bash
# Plan helm chart release
bazel run //tools:release -- plan-helm-release --charts all --format json

# Build and package helm chart
bazel run //tools:release -- package-helm-chart $CHART_NAME --version $VERSION --use-released

# Publish to GitHub Pages helm repository
bazel run //tools:release -- publish-helm-repo --charts-dir ./packaged-charts
```

## Repository Structure and Key Locations

### Main Directories
```
├── .github/               # CI/CD workflows and actions
│   ├── workflows/         # ci.yml (build/test) and release.yml
│   └── actions/           # setup-build-env reusable action
├── .github/               # CI/CD workflows and actions
│   ├── workflows/         # ci.yml (build/test), release.yml (apps and helm), pages.yml
│   └── actions/           # setup-build-env reusable action
├── demo/                  # Example applications (hello_python, hello_go, hello_fastapi, hello_worker, hello_job, hello_internal_api)  
├── manman/                # Production domain with multiple services (experience_api, status_api, worker_dal_api, status_processor, worker, migration)
├── libs/                  # Shared libraries (//libs/python, //libs/go)
├── tools/                 # Release system and build utilities
│   ├── helm/              # Helm chart generation system
│   └── release_helper/    # Python CLI for release operations
├── docs/                  # Documentation (CROSS_COMPILATION.md, HELM_*.md)
├── docker/                # Dockerfile templates
├── BUILD.bazel           # Root build configuration
├── MODULE.bazel          # Bazel dependencies (requires BCR access)
├── AGENT.md              # Comprehensive agent guidelines
└── README.md             # Project documentation
```

### Critical Files for Development
- **`MODULE.bazel`**: External dependencies (requires internet to BCR)
- **`tools/release.bzl`**: Release system with `release_app` and `release_helm_chart` macros
- **`.bazelrc`**: Build configuration and CI optimizations
- **`uv.lock`**: Python dependencies with cross-platform wheels (regenerate with `uv lock`)
- **`pyproject.toml`**: Python project configuration and dependency specifications
- **`tools/helm/helm.bzl`**: Helm chart generation system

### Application Types
Each app can specify an `app_type` attribute in `release_app` to define what Kubernetes resources to generate:
- **`external-api`**: Public HTTP API with Deployment, Service, Ingress, and PDB
- **`internal-api`**: Internal HTTP service with Deployment, Service, and PDB (no Ingress)
- **`worker`**: Background processor with Deployment and PDB (no Service or Ingress)
- **`job`**: Pre-install/pre-upgrade task with Kubernetes Job (no Deployment, Service, or Ingress)

### Application Structure Pattern
Each app follows this structure:
```
demo/app_name/
├── BUILD.bazel           # Contains py_binary/go_binary, tests, and release_app
├── main.py/.go           # Application entry point
├── test_main.py/_test.go # Unit tests  
└── __init__.py           # Python package marker
```

Example `release_app` with app type:
```python
release_app(
    name = "hello_worker",
    language = "python",
    app_type = "worker",  # Defines Kubernetes resources to generate
    domain = "demo",
    registry = "ghcr.io",
    version = "1.0.0",
    description = "Background worker example",
)
```

## Development Workflow

### Adding New Applications
1. Create directory under appropriate domain (e.g., `demo/`, `manman/`)
2. Add `BUILD.bazel` with binary, test, and `release_app` targets
3. Always include `release_app` macro for release system integration
4. Specify `app_type` to define Kubernetes resources (external-api, internal-api, worker, job)
5. Follow naming convention: binary name matches directory name

### Working with Helm Charts
The repository includes a comprehensive Helm chart generation system:

**Creating Helm Charts:**
```python
# In BUILD.bazel
load("//tools:release.bzl", "release_helm_chart")

# Single app chart
release_helm_chart(
    name = "my_app_chart",
    apps = [":my_app_metadata"],
    chart_name = "my-app",
    namespace = "default",
    environment = "dev",
    domain = "demo",
)

# Multi-app chart
release_helm_chart(
    name = "full_stack_chart",
    apps = [
        ":api_metadata",
        ":worker_metadata",
        ":migration_metadata",
    ],
    chart_name = "full-stack",
    namespace = "myapp",
    environment = "prod",
    domain = "myapp",
)
```

**Building and validating charts:**
```bash
# Build chart
bazel build //demo:my_app_chart

# Validate with helm
helm lint bazel-bin/demo/my_app_chart/

# Preview generated YAML
helm template test bazel-bin/demo/my_app_chart/
```

**For detailed Helm documentation, see:**
- `tools/helm/README.md` - Quick start and patterns
- `tools/helm/APP_TYPES.md` - Complete app type reference
- `tools/helm/MIGRATION.md` - Migration guide
- `docs/HELM_RELEASE.md` - Release integration
- `docs/HELM_REPOSITORY.md` - Helm repository management

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

# List all helm charts
bazel run //tools:release -- list-helm-charts

# Detect changed apps
bazel run //tools:release -- changes

# Build container for app
bazel run //tools:release -- build app_name

# Build helm chart
bazel run //tools:release -- build-helm-chart chart_name

# Plan a release  
bazel run //tools:release -- plan --event-type workflow_dispatch --apps all --version v1.0.0

# Plan helm chart release
bazel run //tools:release -- plan-helm-release --charts all --version v1.0.0

# Dry run release
bazel run //tools:release -- release app_name --version v1.2.3 --dry-run

# Package helm chart for release
bazel run //tools:release -- package-helm-chart chart_name --version v1.0.0
```

## Common Issues and Limitations

## Common Issues and Limitations

### Known Limitations
1. **Network Dependency**: Cannot build without access to bcr.bazel.build
2. **Bazel Version**: Requires Bazel 8.0.0+ (configured in `.bazelversion` as 8.3.1)
3. **Docker Required**: Container operations need Docker daemon running
4. **First Build**: Takes significantly longer (20-40 minutes) due to dependency downloads

### Troubleshooting Build Failures

#### DNS/Network Issues
**Error**: `Unknown host: bcr.bazel.build` or `Failed to fetch registry file`
**Solution**: Network connectivity issue - builds will fail. Document as limitation.

#### Bazel Version Issues  
**Error**: `Bazel version X.X.X not found`
**Solution**: Use manual installation:
```bash
wget https://github.com/bazelbuild/bazel/releases/download/8.0.0/bazel-8.0.0-linux-x86_64
chmod +x bazel-8.0.0-linux-x86_64
sudo mv bazel-8.0.0-linux-x86_64 /usr/local/bin/bazel
```

#### Module Resolution Errors
**Error**: `Error computing the main repository mapping`
**Solution**: Check MODULE.bazel dependencies and network access to BCR

#### Cache Issues
**Error**: Stale build results or "Action failed to execute"
**Solution**: `bazel clean` - removes all cached build artifacts

#### Memory/Disk Issues  
**Error**: "No space left on device" or "Out of memory"
**Solution**: Bazel builds can be large (1-2GB cache). Ensure sufficient disk space.

### Performance Notes
- **Build caching**: Bazel caches aggressively - only changed targets rebuild
- **Test caching**: Tests are cached by default (configured in `.bazelrc`)
- **Container builds**: Use `oci_load` targets for faster local development
- **CI Configuration**: Use `--config=ci` for optimized CI builds

### Working Around Limitations
**If builds fail due to network**:
1. Document the limitation in your response
2. Reference these instructions for manual validation steps
3. Use the file structure reference sections for code navigation
4. Rely on static analysis of BUILD.bazel files and source code

## Agent Best Practices

### When Making Code Changes
1. **Always reference AGENT.md first** for architectural guidance
2. **Follow the `release_app` pattern** for any new applications
3. **Specify app_type** to define Kubernetes resources (external-api, internal-api, worker, job)
4. **Use existing BUILD.bazel files as templates** - structure is consistent
5. **Validate dependencies**: Python deps go in requirements.in, Go in go.mod
6. **Update tests**: Every app should have unit tests following existing patterns
7. **Consider Helm charts**: Multi-app domains should use `release_helm_chart` for composition

### When Network/Build Issues Occur
1. **Do not attempt build workarounds** - document the limitation instead
2. **Use static analysis**: Examine source files and BUILD.bazel structures  
3. **Reference the file output sections** in these instructions for navigation
4. **Validate changes conceptually** using the validation scenarios as a guide

### Timeout and Build Guidelines for Agents
- **NEVER CANCEL** build or test operations
- **Set minimum timeouts**: 3600 seconds (60 minutes) for builds, 1800 seconds (30 minutes) for tests
- **Document timing expectations**: First builds take 20-40 minutes, subsequent builds 2-5 minutes
- **Always include timing context** when suggesting build commands

### Code Quality and Consistency
1. **Follow existing import patterns**: `from libs.python.utils import ...`
2. **Maintain binary naming**: Binary target should match directory name
3. **Include release metadata**: Use `release_app` macro for all new applications
4. **Test structure**: Follow `test_main.py` / `main_test.go` naming conventions
5. **App type consistency**: Use appropriate app_type based on the service role
6. **Helm chart composition**: Group related apps into domain-level charts using `release_helm_chart`

## Common Tasks and File Outputs

### Repository Root Files (Reference)
```bash
# ls -la output (for quick reference without searching)
.bazelrc              # Bazel configuration 
.bazelversion         # Required Bazel version (8.3.1)
BUILD.bazel           # Root build file
MODULE.bazel          # Dependencies (requires BCR internet access)
MODULE.bazel.lock     # Dependency lock file
go.mod                # Go module configuration
pyproject.toml        # Python project configuration
uv.lock               # Locked Python dependencies with cross-platform wheels
AGENT.md              # Agent instructions (primary reference)
README.md             # Project documentation
```

### Build Files Structure (Reference)
```bash
# find . -name "BUILD.bazel" output
./BUILD.bazel
./demo/BUILD.bazel
./demo/hello_go/BUILD.bazel
./demo/hello_python/BUILD.bazel
./demo/hello_fastapi/BUILD.bazel  
./demo/hello_worker/BUILD.bazel
./demo/hello_job/BUILD.bazel
./demo/hello_internal_api/BUILD.bazel
./demo/hello_world_test/BUILD.bazel
./manman/BUILD.bazel
./manman/src/host/BUILD.bazel
./manman/src/worker/BUILD.bazel
./manman/src/repository/BUILD.bazel
./manman/src/migrations/BUILD.bazel
./libs/BUILD.bazel
./libs/python/BUILD.bazel
./tools/BUILD.bazel
./tools/release_helper/BUILD.bazel
./tools/helm/BUILD.bazel
```

### Python Dependency Management
```bash
# Update Python dependencies (after modifying pyproject.toml)
uv lock

# Check dependency configuration
cat pyproject.toml     # Source dependencies: pytest, fastapi, uvicorn[standard], httpx, typer, pydantic, etc.
ls -lh uv.lock         # Cross-platform locked dependencies with wheel hashes
```

## Primary Reference Document
**For comprehensive agent guidelines, ALWAYS consult `AGENT.md`** which contains detailed instructions on:
- Repository architecture and release system patterns
- Development workflows and release management  
- Testing strategies and troubleshooting guidance
- Extension points and maintenance procedures
- Helm chart composition system with 4 app types
- Cross-compilation system for multi-platform support

## Domain Overview

### Demo Domain
Example applications demonstrating different app types:
- **hello_python**: Simple Python app demonstrating basic patterns
- **hello_go**: Simple Go app demonstrating Go support
- **hello_fastapi**: FastAPI web service (`external-api` type)
- **hello_internal_api**: Internal API service (`internal-api` type)
- **hello_worker**: Background worker (`worker` type)
- **hello_job**: Database migration job (`job` type)
- **hello_world_test**: Test application

### Manman Domain
Production domain with 6 services forming a complete application:
- **experience_api**: Experience API service (`external-api`, port 8080)
- **status_api**: Status monitoring service (`internal-api`, port 8081)
- **worker_dal_api**: Worker data access layer API (`external-api`, port 8082)
- **status_processor**: Status processing service (`internal-api`, port 8083)
- **worker**: Background worker service (`worker`)
- **migration**: Database migration job (`job`)

The manman domain demonstrates:
- Multi-service application architecture
- Helm chart composition with `release_helm_chart`
- Real-world production patterns
- Service-to-service communication
- Background processing and jobs

### Release Workflow Integration
The `release.yml` workflow supports both app and helm chart releases:

**Apps Release:**
- Specify apps via `apps` input (comma-separated or "all")
- Builds multi-platform container images (amd64, arm64)
- Pushes to container registry with version tags

**Helm Charts Release:**
- Specify charts via `helm_charts` input (comma-separated or "all" or domain name)
- Packages helm charts with versioning
- Publishes to GitHub Pages helm repository
- Supports auto-versioning with `--increment-minor` or `--increment-patch`

Both can be released together in a single workflow run.
