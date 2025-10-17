# Everything Monorepo

A modern Bazel monorepo supporting both Python and Go development with automated release management, Helm chart generation, and multi-platform container builds.

## 🌟 Key Features

- **Multi-Language Support**: Python and Go with shared libraries
- **Automated Releases**: Intelligent change detection and selective app releases
- **Container Native**: Multi-platform Docker images (AMD64/ARM64) with OCI standards
- **Helm Integration**: Automatic Kubernetes chart generation from app metadata
- **CI/CD Pipeline**: GitHub Actions with parallel builds and comprehensive testing
- **Bazel-First**: Fast, cacheable builds with dependency management

## 🏗️ Architecture

```
everything/
├── demo/                    # Example applications
├── libs/                    # Shared libraries (python/, go/)
├── tools/                   # Build and release tooling
│   ├── helm/               # Helm chart generation
│   └── release_helper/     # Release automation
├── docs/                    # Documentation
├── .github/workflows/      # CI/CD pipelines
├── BUILD.bazel             # Root build configuration
└── MODULE.bazel            # External dependencies
```

**Core Principles:**
- **Bazel-First Architecture**: All operations use Bazel for consistency
- **True Cross-Compilation**: Platform transitions for correct ARM64 wheel selection
- **Monorepo Structure**: Multiple apps and shared libraries in a single repository
- **Release Automation**: Comprehensive CI/CD with intelligent change detection

## 🚀 Quick Start

### Prerequisites
- **Bazel 8.3+** with bzlmod support
- **Docker** (for container images)
- **Git** (for version control)
- **Python Virtual Environment** (recommended)

### Installation
```bash
# Install Bazelisk (manages Bazel versions)
brew install bazelisk

# Verify installation
bazel version

# Create Python virtual environment
uv venv && source .venv/bin/activate
```

### Build and Test
```bash
# Build everything
bazel build //...

# Run tests
bazel test //...

# Run example apps
bazel run //demo/hello_python:hello-python
bazel run //demo/hello_go:hello-go
```

### Verify Setup
```bash
# Check Bazel version
bazel version

# Test build system
bazel build //demo/hello_python:hello-python

# Verify release system
bazel query "kind('app_metadata', //...)"
```

**For complete setup instructions, see:** [docs/SETUP.md](docs/SETUP.md)

## 📚 Documentation

Comprehensive guides for working with the monorepo:

- **[Setup Guide](docs/SETUP.md)** - Installation and prerequisites
- **[Dependencies](docs/DEPENDENCIES.md)** - Managing Python and Go dependencies
- **[Development](docs/DEVELOPMENT.md)** - Adding new apps and shared libraries
- **[Helm Charts](docs/HELM.md)** - Kubernetes chart generation
- **[Testing](docs/TESTING.md)** - Running and writing tests
- **[Configuration](docs/CONFIGURATION.md)** - Bazel settings and remote cache
- **[CI/CD Pipeline](docs/CI_CD.md)** - Continuous integration details
- **[Docker Images](docs/DOCKER.md)** - Container image building
- **[Release Management](docs/RELEASE.md)** - Automated releases and versioning

Additional resources:
- **[Helm Release System](docs/HELM_RELEASE.md)** - Helm chart releases
- **[Release Tool Cleanup](docs/RELEASE_TOOL_CLEANUP.md)** - Tool cleanup summary

## 🎯 Common Tasks

### Adding a New Python App

```bash
mkdir my_python_app
cd my_python_app
# Create main.py, test_main.py, __init__.py, BUILD.bazel
```

See the complete guide: [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)

### Adding Dependencies

```bash
# Add to pyproject.toml
uv lock --python 3.13
# Use @pypi//:package-name in BUILD.bazel
```

See the complete guide: [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md)

### Building and Running Apps

```bash
# Build an app
bazel build //path/to/app:app_name

# Run an app
bazel run //path/to/app:app_name

# Test an app
bazel test //path/to/app:test_target
```

### Releasing Apps

Use GitHub Actions UI (recommended):
1. Go to Actions → Release workflow
2. Specify apps and version
3. Run with or without dry-run

See the complete guide: [docs/RELEASE.md](docs/RELEASE.md)

## 🤝 Contributing & Support

### Getting Help
- **Setup Issues**: See [docs/SETUP.md](docs/SETUP.md) for installation troubleshooting
- **Build Issues**: Check [docs/CONFIGURATION.md](docs/CONFIGURATION.md) for Bazel settings
- **Release Problems**: See [docs/RELEASE.md](docs/RELEASE.md) for release troubleshooting

### Repository Structure
```
├── .github/workflows/     # CI/CD workflows (ci.yml, release.yml)
├── demo/                  # Example applications
├── docs/                  # Documentation (guides and references)
├── libs/                  # Shared libraries (python/, go/)
├── tools/                 # Build and release tooling
├── BUILD.bazel           # Root build configuration
├── MODULE.bazel          # External dependencies
└── README.md             # This file
```

### Future Improvements
Areas that could be enhanced:
- **Enhanced Go Support**: Enable gazelle rules for better Go dependency management
- **Testing Strategy**: Expand test utilities and integration testing capabilities
- **Documentation**: Auto-generation from code for better consistency
