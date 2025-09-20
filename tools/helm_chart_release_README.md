# Helm Chart Release System

> **📦 Migrated to `//tools/helm`**: The helm chart release system has been migrated to a new module at `//tools/helm` with template-based generation replacing string building. See `//tools/helm/README.md` for the new module documentation.

This document provides comprehensive documentation for the Helm chart release system built on top of Bazel and integrated with the existing release_app infrastructure.

## Overview

The Helm chart release system enables automatic generation and deployment of Helm charts for multi-app services in a Bazel monorepo. It integrates with the existing `release_app` metadata system to automatically resolve versions and dependencies.

## Key Features

- **Multi-app support**: Single chart can deploy multiple related applications
- **Automatic version resolution**: Automatically selects latest versions from release metadata
- **Template-based**: Reusable templates for consistent chart structure
- **CI/CD integration**: GitHub Actions workflow for automated publishing
- **GitHub Pages hosting**: Automatic Helm repository hosting
- **Domain-based naming**: Follows `domain-app` naming conventions

## Quick Start

### 1. Add Helm Chart Target

In your domain's `BUILD.bazel` file:

```starlark
load("//tools:helm_chart_release.bzl", "helm_chart_release_macro")

# Existing release_app targets
release_app(
    name = "my_api",
    domain = "myapp",
    # ... other config
)

release_app(
    name = "my_worker", 
    domain = "myapp",
    # ... other config
)

# Add helm chart for the domain
helm_chart_release_macro(
    domain = "myapp",
    charts = {
        "api_chart": {
            "description": "API service chart",
            "apps": ["my_api"]
        },
        "full_chart": {
            "description": "Complete application stack",
            "apps": ["my_api", "my_worker"]
        }
    }
)
```

### 2. Build the Chart

```bash
# Build specific chart
bazel build //myapp:myapp-api_chart

# Build all charts in domain
bazel build //myapp:all
```

### 3. Deploy via GitHub Actions

The chart will be automatically built and published when:
- Code is pushed to main branch
- Pull request affects chart-related files
- Manual workflow dispatch is triggered

## Architecture

### Core Components

1. **`helm_chart_release` rule** - Core Bazel rule for chart generation
2. **`helm_chart_release_macro` macro** - Convenience wrapper for multi-chart definitions
3. **Template library** - Reusable Helm templates in `//tools/templates/`
4. **Version resolver** - Python utility for automatic version resolution
5. **CI/CD workflow** - GitHub Actions for automated publishing

### Integration with release_app

The system automatically discovers and integrates with existing `release_app` targets:

```
release_app("my_api") → app_metadata → helm_chart_release → Chart.yaml + values.yaml
```

### File Generation

Each chart generates:
- `Chart.yaml` - Helm chart metadata
- `values.yaml` - Application configuration values
- `templates/` - Kubernetes manifests (from template library)

## Configuration

### Chart Configuration

Charts are configured via the `charts` dictionary in `helm_chart_release_macro`:

```starlark
charts = {
    "chart_name": {
        "description": "Human readable description",
        "apps": ["app1", "app2"],  # List of release_app targets
        "version": "1.0.0",        # Optional: override chart version
        "custom_values": {         # Optional: additional values
            "service.type": "LoadBalancer",
            "ingress.enabled": True
        }
    }
}
```

### Version Resolution

The system uses multiple strategies for version resolution:

1. **Explicit version**: Use provided `version` or `app_version`
2. **Metadata resolution**: Query `app_metadata` targets for latest versions
3. **Git tag fallback**: Use Git tags when metadata unavailable
4. **Default fallback**: Use "latest" or "1.0.0" as last resort

### Template Customization

Templates are located in `//tools/templates/` and can be customized:

```
//tools/templates/
├── BUILD.bazel          # Template file groups
├── _helpers.tpl         # Helm helper functions
├── deployment.yaml      # Kubernetes Deployment
└── service.yaml         # Kubernetes Service
```

To add custom templates:

1. Create template files in `//tools/templates/`
2. Add them to the `helm_templates` filegroup in `//tools/templates/BUILD.bazel`
3. Reference them in your chart configuration

## CI/CD Integration

### GitHub Actions Workflow

The `.github/workflows/helm-charts.yml` workflow provides:

1. **Automatic discovery** - Finds all helm chart targets
2. **Multi-matrix builds** - Builds charts in parallel
3. **Chart validation** - Validates generated chart structure
4. **GitHub Pages publishing** - Hosts charts at `https://username.github.io/repo/`

### Workflow Triggers

- **Push to main**: Builds and validates charts
- **Pull requests**: Validates chart changes
- **Manual dispatch**: Allows selective publishing with parameters

### Manual Release

To manually release charts:

1. Go to Actions → Helm Chart Release
2. Click "Run workflow"
3. Set parameters:
   - **Domain**: Target domain or "all"
   - **Chart version**: Version to release
   - **Publish to Pages**: Enable GitHub Pages publishing

## Usage Patterns

### Single-App Charts

For deploying individual applications:

```starlark
helm_chart_release_macro(
    domain = "api",
    charts = {
        "web_chart": {
            "description": "Web API service",
            "apps": ["web_api"]
        }
    }
)
```

### Multi-App Charts

For deploying related services together:

```starlark
helm_chart_release_macro(
    domain = "platform",
    charts = {
        "backend_chart": {
            "description": "Backend services",
            "apps": ["api", "worker", "scheduler"]
        }
    }
)
```

### Environment-Specific Charts

For different deployment environments:

```starlark
helm_chart_release_macro(
    domain = "myapp",
    charts = {
        "dev_chart": {
            "description": "Development environment",
            "apps": ["api"],
            "custom_values": {
                "replicaCount": 1,
                "resources.requests.memory": "256Mi"
            }
        },
        "prod_chart": {
            "description": "Production environment", 
            "apps": ["api", "worker"],
            "custom_values": {
                "replicaCount": 3,
                "resources.requests.memory": "512Mi"
            }
        }
    }
)
```

## Generated Files

The rule generates standard Helm chart files:

1. **Chart.yaml**: Metadata with version info and whale-net maintainer
2. **values.yaml**: Multi-app configuration with image repositories and tags
3. **templates/**: Kubernetes manifests (_helpers.tpl, deployment.yaml, service.yaml)

Example Chart.yaml:
```yaml
apiVersion: v2
name: manman-host
description: Host services for manman domain
version: 1.0.0
maintainers:
  - name: whale-net
    url: https://github.com/whale-net
```

Example values.yaml:
```yaml
images:
  experience_api:
    repository: "experience_api"
    tag: "latest"
  status_api:
    repository: "status_api" 
    tag: "latest"
```

## Best Practices

### Naming Conventions

- **Chart names**: Use `domain-chart_name` format (e.g., `myapp-api_chart`)
- **App names**: Match `release_app` target names
- **Domain names**: Keep consistent with existing domains

### Version Management

- Use semantic versioning for chart versions
- Let the system auto-resolve app versions when possible
- Use explicit versions for production releases

### Template Organization

- Keep templates generic and reusable
- Use Helm helpers for common patterns
- Document template variables and requirements

### CI/CD Strategy

- Use pull requests for chart validation
- Use manual dispatch for production releases
- Enable GitHub Pages for public chart hosting

## Troubleshooting

### Common Issues

**Chart not found in query**
```bash
# Verify chart target exists
bazel query "kind('helm_chart_release', //...)"
```

**Missing metadata**
```bash
# Check app metadata targets
bazel query "kind('app_metadata', //domain:*)"
```

**Build failures**
```bash
# Build with verbose output
bazel build //domain:chart_name --verbose_failures
```

**Template errors**
```bash
# Check generated files
find bazel-bin/domain -name "Chart.yaml" -exec cat {} \;
find bazel-bin/domain -name "values.yaml" -exec head -10 {} \;
```

### Debug Commands

```bash
# List all helm chart targets
bazel query "kind('helm_chart_release', //...)"

# Check app metadata
bazel query "kind('app_metadata', //...)"

# Build with verbose output
bazel build //domain:domain-chart_name --verbose_failures

# Check generated files
find bazel-bin/domain -name "Chart.yaml" -exec cat {} \;
find bazel-bin/domain -name "values.yaml" -exec head -20 {} \;
```

## Migration Guide

### From Existing Charts

If you have existing Helm charts:

1. **Preserve chart structure**: Copy `Chart.yaml` and `values.yaml` as templates
2. **Extract common patterns**: Move reusable parts to template library
3. **Update build targets**: Replace manual chart management with `helm_chart_release_macro`
4. **Migrate CI/CD**: Update workflows to use new GitHub Actions workflow

### Integration Steps

1. Add `helm_chart_release_macro` to existing `BUILD.bazel`
2. Test chart generation: `bazel build //domain:chart_name`
3. Validate generated files match expectations
4. Update CI/CD workflows to use new automation
5. Enable GitHub Pages for chart hosting

## Advanced Features

### Custom Value Injection

```starlark
helm_chart_release_macro(
    domain = "myapp",
    charts = {
        "advanced_chart": {
            "apps": ["api", "worker"],
            "custom_values": {
                "global.imageRegistry": "my-registry.com",
                "api.ingress.host": "api.example.com",
                "worker.cronJobs.backup.schedule": "0 2 * * *"
            }
        }
    }
)
```

### Template Overrides

Create domain-specific templates by:

1. Creating `//domain/templates/` directory
2. Adding custom template files
3. Referencing in chart configuration

### Version Pinning

```starlark
charts = {
    "stable_chart": {
        "apps": ["api"],
        "version": "1.2.3",        # Pin chart version
        "custom_values": {
            "api.image.tag": "v2.1.0"  # Pin specific app version via values
        }
    }
}
```

## Reference

### Rule Parameters

#### helm_chart_release

| Parameter | Type | Description |
|-----------|------|-------------|
| `name` | `string` | Target name |
| `domain` | `string` | Domain name |
| `description` | `string` | Chart description |
| `apps` | `list[string]` | List of app targets |
| `chart_version` | `string` | Chart version (default: "1.0.0") |
| `values_overrides` | `dict` | Additional values |

#### helm_chart_release_macro

| Parameter | Type | Description |
|-----------|------|-------------|
| `domain` | `string` | Domain name |
| `charts` | `dict` | Chart configurations |

Each chart in the `charts` dict has:
- `description`: Human readable description
- `apps`: List of release_app target names  
- `version`: Optional chart version override (default: "1.0.0")
- `custom_values`: Optional dictionary of additional values

### File Locations

- **Rule implementation**: `//tools/helm:helm_chart_release.bzl` (moved from `//tools`)
- **Templates**: `//tools/helm/templates/` (moved from `//tools/templates/`)
- **Template renderer**: `//tools/helm:template_renderer` (new)
- **CI/CD workflow**: `.github/workflows/helm-charts.yml`
- **Documentation**: `//tools/helm_chart_release_README.md`
- **Module documentation**: `//tools/helm/README.md` (new)
- **Examples**: `//manman/BUILD.bazel` (see working implementation)

## Support

For issues and questions:

1. Check this documentation
2. Review existing examples in `//manman/BUILD.bazel`
3. Examine generated files for debugging
4. Check GitHub Actions workflow logs
5. Open an issue in the repository