# Agent Instructions for Everything Monorepo

This document provides comprehensive guidelines for AI agents working on the Everything monorepo. It establishes a framework for understanding, maintaining, and extending the codebase while preserving its architectural principles.

## ⚠️ CRITICAL: Cross-Compilation

**MUST READ**: [`docs/CROSS_COMPILATION.md`](docs/CROSS_COMPILATION.md)

This repository implements true cross-compilation for Python apps using Bazel platform transitions. **This is critical for ARM64 container deployments**. If cross-compilation breaks, ARM64 containers will crash at runtime with compiled dependencies (pydantic, numpy, pandas, etc.).

**Key Points**:
- Python apps with compiled dependencies MUST use `multiplatform_py_binary`
- Platform transitions ensure correct wheel selection (x86_64 vs aarch64)
- Test cross-compilation with:
  ```bash
  # Load images first (required)
  bazel run //demo/hello_fastapi:hello_fastapi_image_amd64_load
  bazel run //demo/hello_fastapi:hello_fastapi_image_arm64_load
  # Run the test
  bazel test //tools:test_cross_compilation --test_output=streamed
  ```
- CI automatically verifies cross-compilation on every PR
- If `//tools:test_cross_compilation` fails, **DO NOT MERGE**

## 📋 Framework Overview

### Core Principles
- **Bazel-First Architecture**: All build, test, and release operations use Bazel
- **True Cross-Compilation**: Platform transitions for correct ARM64 wheel selection
- **Monorepo Structure**: Multiple apps and shared libraries in a single repository
- **Language Support**: Primary support for Python and Go with extensible patterns
- **Container-Native**: All apps are containerized using OCI standards
- **Release Automation**: Comprehensive CI/CD with intelligent change detection

### Repository Structure
```
├── demo/                    # Example applications
├── libs/                    # Shared libraries (python/, go/)
├── tools/                   # Build and release tooling
├── docs/                    # Documentation (including CROSS_COMPILATION.md)
├── .github/                 # CI/CD workflows
├── docker/                  # Base container configurations
├── test_cross_compilation.py # CRITICAL: Cross-compilation verification
└── BUILD.bazel, MODULE.bazel # Bazel configuration
```

## 🚀 Release System Architecture

### Release Apps (`release_app` macro)

The `release_app` macro is the cornerstone of the release system. It automatically creates both release metadata and multi-platform OCI images for applications.

#### Basic Usage
```starlark
# In your app's BUILD.bazel
load("//tools:release.bzl", "release_app")

release_app(
    name = "my_app",
    binary_target = ":my_app",
    language = "python",  # or "go"
    domain = "demo",      # Required: categorizes your app
    description = "Description of what this app does",
)
```

#### Parameters
- **`name`**: App name (must match directory name)
- **`binary_target`**: Bazel target for the executable
- **`language`**: Programming language ("python" or "go")
- **`domain`**: App categorization (e.g., "api", "web", "demo")
- **`description`**: Human-readable app description
- **`version`**: Default version (optional, defaults to "latest")
- **`registry`**: Container registry (optional, defaults to "ghcr.io")
- **`custom_repo_name`**: Override default naming (optional)

#### Generated Artifacts
The `release_app` macro automatically creates:
1. **Release metadata** (`<app_name>_metadata`) - JSON metadata for discovery
2. **Multi-platform images** - Separate targets for amd64 and arm64
3. **OCI push targets** - For publishing to container registries

### Multi-Platform Image System

#### How It Works
The repository uses an automated multi-platform build system that creates container images supporting both AMD64 and ARM64 architectures:

**For Python apps:**
- `multiplatform_py_binary` creates platform-specific binaries automatically
- `pycross` handles platform-specific Python dependencies (wheels) transparently
- No manual platform configuration needed - everything is automatic

**For Go apps:**
- Go toolchain handles cross-compilation automatically
- Binaries are statically linked, so no platform-specific dependencies

**Platform selection:**
- `oci_image_index` creates a multi-platform manifest list
- Docker/Kubernetes automatically pulls the correct platform image
- No need to specify platforms explicitly in most cases

#### Custom Platform Definitions (Optional)
Platform definitions are available in `//tools:platforms.bzl` for advanced use cases:
- `//tools:linux_x86_64` - Linux AMD64
- `//tools:linux_arm64` - Linux ARM64
- `//tools:macos_x86_64` - macOS Intel (local dev)
- `//tools:macos_arm64` - macOS Apple Silicon (local dev)

These are rarely needed - the build system handles platform selection automatically.

### Image Build System

#### Container Image Naming Convention
All container images follow the `<domain>-<app>:<version>` format:

```bash
# Registry format
ghcr.io/OWNER/demo-hello_python:v1.2.3    # Version-specific
ghcr.io/OWNER/demo-hello_python:latest    # Latest release
ghcr.io/OWNER/demo-hello_python:abc123def # Commit-specific

# Local development format
demo-hello_python:latest
```

#### Image Targets Generated
For each app with `release_app`, the following targets are created:
- `<app>_image` - Multi-platform manifest list (AMD64 + ARM64)
- `<app>_image_amd64` - AMD64-specific image
- `<app>_image_arm64` - ARM64-specific image
- `<app>_image_load` - Load into Docker (uses AMD64 for local testing)
- `<app>_image_amd64_load` - Load AMD64 image specifically
- `<app>_image_arm64_load` - Load ARM64 image specifically
- `<app>_image_push` - Push multi-platform manifest
- `<app>_image_amd64_push` - Push AMD64 image
- `<app>_image_arm64_push` - Push ARM64 image

#### Building Images
```bash
# Build all platform variants
bazel build //path/to/app:app_image

# Build specific platform
bazel build //path/to/app:app_image_amd64

# Build and load into Docker (recommended for development)
bazel run //path/to/app:app_image_load

# Using release tool (production workflow)
bazel run //tools:release -- build app_name
```

## 🛠️ Development Workflow

### Adding New Applications

#### 1. Create Application Structure
```bash
# Create app directory
mkdir -p new_app

# Add source files and tests
touch new_app/main.py  # or main.go
touch new_app/test_main.py  # or main_test.go
```

#### 2. Create BUILD.bazel File
For Python apps:
```starlark
load("@rules_python//python:defs.bzl", "py_binary", "py_test")
load("@everything_pip_deps//:requirements.bzl", "requirement")
load("//tools:release.bzl", "release_app")

py_binary(
    name = "new_app",
    srcs = ["main.py"],
    deps = ["//libs/python"],
    visibility = ["//visibility:public"],
)

py_test(
    name = "test_main",
    srcs = ["test_main.py"],
    deps = [
        ":new_app_lib",
        requirement("pytest"),
    ],
    size = "small",
)

release_app(
    name = "new_app",
    binary_target = ":new_app",
    language = "python",
    domain = "demo",  # Choose appropriate domain
    description = "Description of what this app does",
)
```

For Go apps:
```starlark
load("@rules_go//go:def.bzl", "go_binary", "go_test")
load("//tools:release.bzl", "release_app")

go_binary(
    name = "new_app",
    srcs = ["main.go"],
    deps = ["//libs/go"],
    visibility = ["//visibility:public"],
)

go_test(
    name = "main_test",
    srcs = ["main_test.go"],
    deps = ["//libs/go"],
    size = "small",
)

release_app(
    name = "new_app",
    binary_target = ":new_app",
    language = "go",
    domain = "demo",
    description = "Description of what this app does",
)
```

#### 3. Verify App Discovery
```bash
# Check that your app is discoverable
bazel query "kind('app_metadata', //...)"

# Verify targets exist
bazel query "//new_app:new_app"
bazel query "//new_app:new_app_metadata"
```

### Working with Shared Libraries

#### Python Libraries
Reference shared Python code from `//libs/python`:
```starlark
deps = [
    "//libs/python",
    requirement("package_name"),
]
```

#### Go Libraries
Reference shared Go code from `//libs/go`:
```starlark
deps = ["//libs/go"]
```

## 🔄 Release Management

### Release Methods

#### 1. GitHub UI (Recommended)
Use the GitHub Actions "Release" workflow:
1. Go to Actions tab
2. Select "Release" workflow
3. Click "Run workflow"
4. Configure:
   - **apps**: Comma-separated list or "all"
   - **version**: Semantic version (e.g., v1.2.3)
   - **dry_run**: Test without publishing

#### 2. GitHub CLI
```bash
# Release specific apps
gh workflow run release.yml \
  -f apps=hello_python,hello_go \
  -f version=v1.2.3 \
  -f dry_run=false

# Release all apps
gh workflow run release.yml \
  -f apps=all \
  -f version=v1.2.3

# Dry run (test without publishing)
gh workflow run release.yml \
  -f apps=hello_python \
  -f version=v1.2.3 \
  -f dry_run=true
```

#### 3. Local Release Tool
```bash
# List all discoverable apps
bazel run //tools:release -- list

# Detect apps with changes
bazel run //tools:release -- changes

# Build and test locally
bazel run //tools:release -- build app_name

# Release with version
bazel run //tools:release -- release app_name --version v1.2.3

# Dry run
bazel run //tools:release -- release app_name --version v1.2.3 --dry-run
```

### Release Process Details

#### 1. App Discovery
The system uses Bazel queries to find all apps with release metadata:
```bash
bazel query "kind('app_metadata', //...)"
```

#### 2. Change Detection
Intelligent change detection ensures only modified apps are released:
- Git diff analysis since last release tag
- Dependency awareness (shared library changes trigger dependent apps)
- Manual app selection override

#### 3. Release Matrix
GitHub Actions automatically generates build matrices:
```yaml
matrix:
  include:
    - app: hello_python
      binary: hello_python
      image: hello_python_image
    - app: hello_go
      binary: hello_go
      image: hello_go_image
```

#### 4. Container Publishing
Multi-platform images are built and pushed with multiple tags:
- Version-specific: `ghcr.io/OWNER/domain-app:v1.2.3`
- Latest: `ghcr.io/OWNER/domain-app:latest`
- Commit-specific: `ghcr.io/OWNER/domain-app:abc123def`

## 🔍 Agent Guidelines

### Code Analysis
When analyzing code:
1. **Understand the Bazel structure** - BUILD.bazel files define targets and dependencies
2. **Check release metadata** - Look for `release_app` usage to understand releasable apps
3. **Follow dependency chains** - Use `bazel query` to understand relationships
4. **Respect language conventions** - Python and Go have different patterns

### Making Changes
When modifying code:
1. **Test locally first** - Use `bazel test //...` before changes
2. **Update BUILD.bazel files** - Add new dependencies and targets as needed
3. **Maintain release compatibility** - Don't break existing `release_app` configurations
4. **Follow naming conventions** - Keep directory names and target names consistent

### Release Management
When working with releases:
1. **Use semantic versioning** - Follow semver for all releases
2. **Test with dry runs** - Always test release process before publishing
3. **Understand change detection** - Know which changes trigger which app releases
4. **Monitor image builds** - Ensure multi-platform images build successfully

### Troubleshooting
Common debugging approaches:
1. **Check app discovery**: `bazel query "kind('app_metadata', //...)"`
2. **Verify targets exist**: `bazel query "//path/to/app:target"`
3. **Test builds locally**: `bazel run //tools:release -- build app_name`
4. **Check image functionality**: `docker run --rm domain-app:latest`

## 🧪 Testing and Validation

### Build Testing
```bash
# Build all targets
bazel build //...

# Build specific app
bazel build //path/to/app:app_name

# Build with different configurations
bazel build --config=ci //...
```

### Test Execution
```bash
# Run all tests
bazel test //...

# Run specific test
bazel test //path/to/app:test_target

# Run with verbose output
bazel test //... --test_output=all
```

### Release Testing
```bash
# Test release planning
bazel run //tools:release -- plan --event-type workflow_dispatch --apps all --version v1.0.0

# Test image building
bazel run //tools:release -- build app_name

# Test image functionality
docker run --rm domain-app:latest
```

## 📚 Extension Points

### Adding New Languages
To add support for a new language:
1. Create language-specific OCI image builders in `tools/oci.bzl`
2. Update `release_app` macro to handle the new language
3. Add examples in the `demo/` directory
4. Update CI workflows as needed

### Custom Release Workflows
To customize release behavior:
1. Modify `tools/release_helper/` Python modules
2. Update workflow files in `.github/workflows/`
3. Extend the `release_app` macro parameters
4. Add new CLI commands to `tools/release_helper/cli.py`

### Advanced OCI Configuration
For specialized container requirements:
```starlark
load("//tools:oci.bzl", "oci_image_with_binary")

oci_image_with_binary(
    name = "custom_image",
    binary = ":my_binary",
    base_image = "@custom_base",
    platform = "linux/amd64",
    repo_tag = "custom:latest",
)
```

## 🚨 Important Considerations

### Security
- Never commit secrets to the repository
- Use GitHub secrets for registry credentials
- Validate all external inputs in release tools

### Performance
- Use `oci_load` targets for local development (faster than direct building)
- Leverage Bazel's caching for faster builds
- Consider build matrix optimization for large releases

### Maintenance
- Keep dependencies up to date in MODULE.bazel
- Regularly review and update base container images
- Monitor release pipeline performance and reliability

---

## � Helm Chart Composition System

### Overview

The helm chart system automatically generates Kubernetes manifests from app definitions. It supports 4 app types, each generating appropriate Kubernetes resources.

### App Types and Generated Resources

| App Type | Deployment | Service | Ingress | PDB | Job | Use Case |
|----------|-----------|---------|---------|-----|-----|----------|
| `external-api` | ✅ | ✅ | ✅ | ✅ | ❌ | Public HTTP APIs |
| `internal-api` | ✅ | ✅ | ❌ | ✅ | ❌ | Internal services |
| `worker` | ✅ | ❌ | ❌ | ✅ | ❌ | Background processors |
| `job` | ❌ | ❌ | ❌ | ❌ | ✅ | Migrations, setup tasks |

### Defining Apps with Types

Add `type` attribute to app definitions:

```python
# demo/hello_fastapi/BUILD.bazel
load("//tools:demo_app.bzl", "demo_app")

demo_app(
    name = "hello_fastapi",
    srcs = ["main.py"],
    deps = [
        "@pip//fastapi",
        "@pip//uvicorn",
    ],
    port = 8000,           # Required for API types
    type = "external-api",  # Defines what resources to generate
)
```

**Type selection**:
- `external-api`: Public HTTP API needing external ingress
- `internal-api`: Internal HTTP service (no ingress)
- `worker`: Background processor (no service/ingress)
- `job`: Pre-install/pre-upgrade task (migrations)

### Generating Helm Charts

Create charts with the `helm_chart` rule:

```python
load("//tools:helm.bzl", "helm_chart")

# Single app chart
helm_chart(
    name = "hello_fastapi_chart",
    app = ":hello_fastapi",
    environment = "dev",
)

# Multi-app chart
helm_chart(
    name = "full_stack_chart",
    apps = [
        ":api_server",
        ":background_worker",
        ":db_migration",
    ],
    environment = "prod",
)
```

### Building and Validating Charts

```bash
# Build chart
bazel build //demo/hello_fastapi:hello_fastapi_chart

# Chart location
ls -la bazel-bin/demo/hello_fastapi/hello_fastapi_chart/

# Validate
helm lint bazel-bin/demo/hello_fastapi/hello_fastapi_chart/

# Preview generated YAML
helm template test bazel-bin/demo/hello_fastapi/hello_fastapi_chart/
```

### Ingress Pattern (1:1 Mapping)

Each `external-api` app gets its own dedicated Ingress resource:

```yaml
# api_server-dev-ingress
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: api_server-dev-ingress
spec:
  rules:
  - host: api_server-dev.local
    http:
      paths:
      - path: /
        backend:
          service:
            name: api_server-dev
            port:
              number: 8000
```

**Key points**:
- Simple 1:1 pattern (no complex mode selection)
- Each external-api = dedicated Ingress
- Host pattern: `{appName}-{environment}.local`
- Ingress name: `{appName}-{environment}-ingress`

### ArgoCD Integration

Charts include sync-wave annotations for proper ordering:

- **Wave `-1`**: Jobs (migrations, setup) - run first
- **Wave `0`**: Deployments, Services, Ingress - run after jobs

```yaml
# job.yaml.tmpl
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "-1"
    helm.sh/hook: pre-install,pre-upgrade

# deployment.yaml.tmpl, service.yaml.tmpl, ingress.yaml.tmpl
metadata:
  annotations:
    argocd.argoproj.io/sync-wave: "0"
```

### Customizing Helm Charts

Override values at deployment time:

```yaml
# custom-values.yaml
apps:
  hello_fastapi:
    replicas: 3
    resources:
      requests:
        memory: "512Mi"
        cpu: "500m"
      limits:
        memory: "1Gi"
        cpu: "1000m"
    livenessProbe:
      httpGet:
        path: /health/live
        port: 8000

global:
  ingress:
    enabled: true
    className: nginx
    annotations:
      cert-manager.io/cluster-issuer: letsencrypt-prod
```

```bash
helm install app-name ./chart/ --values custom-values.yaml
```

### Common Tasks

#### Generate chart for single app
```bash
bazel build //demo/hello_python:hello_python_chart
helm lint bazel-bin/demo/hello_python/hello_python_chart/
```

#### Generate multi-app chart
```bash
bazel build //demo:full_stack_chart
helm template test bazel-bin/demo/full_stack_chart/ | grep "kind:" | sort | uniq -c
```

#### Validate before deploy
```bash
helm template test ./chart/ | kubectl apply --dry-run=client -f -
```

### Helm Documentation

For detailed documentation, see:
- **[tools/helm/README.md](tools/helm/README.md)** - Quick start and common patterns
- **[tools/helm/APP_TYPES.md](tools/helm/APP_TYPES.md)** - Complete app type reference
- **[tools/helm/TEMPLATES.md](tools/helm/TEMPLATES.md)** - Template development guide
- **[tools/helm/MIGRATION.md](tools/helm/MIGRATION.md)** - Migration guide
- **[tools/helm/IMPLEMENTATION_PLAN.md](tools/helm/IMPLEMENTATION_PLAN.md)** - Full implementation details

### Troubleshooting Helm Charts

#### Chart not found
```bash
# Build the chart first
bazel build //path/to/app:app_chart
```

#### Ingress not generated
```bash
# Check app type
type = "external-api"  # Must be external-api

# Check ingress enabled
global:
  ingress:
    enabled: true  # Must be true
```

#### Port errors
```bash
# API types require port
demo_app(
    name = "api",
    port = 8080,  # Required for external-api and internal-api
    type = "external-api",
)
```

---

## �📝 Documentation Status

This AGENT.md provides a comprehensive framework for AI agents working with the Everything monorepo. The documentation focuses specifically on:

**Detailed Coverage:**
- `release_app` macro usage and parameters
- Container image build system with `<domain>-<app>:<version>` naming
- Multi-platform image generation (amd64/arm64)
- Release workflow automation and change detection
- Helm chart composition with 4 app types
- 1:1 ingress mapping pattern
- ArgoCD sync-wave integration

**Framework Areas:**
- Development workflows for Python and Go applications
- Bazel build system integration
- GitHub Actions CI/CD processes
- Helm chart generation and customization
- Extension points for future enhancements

This framework provides the foundation for working with the Everything monorepo. Specific implementation details can be expanded as the codebase evolves, but the core principles, `release_app` system, and helm chart patterns should remain stable.