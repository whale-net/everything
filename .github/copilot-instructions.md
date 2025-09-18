# GitHub Copilot Instructions for Everything Monorepo

## Primary Reference Document
**ALWAYS consult `AGENT.md` first** for comprehensive guidelines when working on this monorepo. The AGENT.md file contains detailed instructions for AI agents and provides the authoritative framework for understanding and working with this codebase.

## Core Architecture Patterns

### Bazel-First Development
- This is a Bazel-based monorepo with Python and Go applications
- All build, test, and release operations use Bazel
- Always check `BUILD.bazel` files for target definitions and dependencies
- Use `bazel query` commands to understand target relationships

### Release System (`release_app` macro)
The cornerstone of this repository is the `release_app` macro. When creating new applications:

```starlark
load("//tools:release.bzl", "release_app")

release_app(
    name = "my_app",
    binary_target = ":my_app",
    language = "python",  # or "go"
    domain = "demo",      # Required: categorizes your app
    description = "Description of what this app does",
)
```

### Container Image Naming Convention
All container images follow `<domain>-<app>:<version>` format:
- `ghcr.io/OWNER/demo-hello_python:v1.2.3`
- `ghcr.io/OWNER/demo-hello_python:latest`

## Development Guidelines

### Repository Structure
```
├── demo/          # Example applications
├── libs/          # Shared libraries (python/, go/)
├── tools/         # Build and release tooling
├── .github/       # CI/CD workflows
├── docker/        # Base container configurations
└── BUILD.bazel, MODULE.bazel # Bazel configuration
```

### When Adding New Applications
1. Create app directory with appropriate `BUILD.bazel`
2. Use `release_app` macro for containerized applications
3. Follow language-specific patterns (Python vs Go)
4. Verify app discovery: `bazel query "kind('app_metadata', //...)"`

### When Modifying Code
1. Test locally first: `bazel test //...`
2. Update `BUILD.bazel` files for new dependencies
3. Maintain release compatibility
4. Follow existing naming conventions

### Release Management
- Use semantic versioning (v1.2.3)
- Test with dry runs before publishing
- Understand change detection triggers
- Use GitHub Actions "Release" workflow for production releases

## Key Commands to Remember
```bash
# List all discoverable apps
bazel run //tools:release -- list

# Detect apps with changes
bazel run //tools:release -- changes

# Build and test locally
bazel run //tools:release -- build app_name

# Release with version
bazel run //tools:release -- release app_name --version v1.2.3

# Verify app discovery
bazel query "kind('app_metadata', //...)"
```

## Language-Specific Patterns

### Python Applications
- Use `@rules_python//python:defs.bzl` for py_binary and py_test
- Reference shared libs with `//libs/python`
- Use `@everything_pip_deps//:requirements.bzl` for external dependencies

### Go Applications
- Use `@rules_go//go:def.bzl` for go_binary and go_test
- Reference shared libs with `//libs/go`

## Security and Performance
- Never commit secrets to the repository
- Use `oci_load` targets for local development (faster)
- Leverage Bazel's caching for faster builds
- Keep dependencies updated in MODULE.bazel

## For Detailed Information
Refer to `AGENT.md` sections:
- 🚀 Release System Architecture
- 🛠️ Development Workflow  
- 🔄 Release Management
- 🔍 Agent Guidelines
- 🧪 Testing and Validation
- 📚 Extension Points
- 🚨 Important Considerations

The `AGENT.md` file is your comprehensive guide - always check it for detailed procedures, troubleshooting, and architectural decisions.

The `AGENT.md` file is your comprehensive guide - always check it for detailed procedures, troubleshooting, and architectural decisions.