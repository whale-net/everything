# Helm Chart Release Module

This module provides template-based Helm chart generation for multi-app deployments.

## Overview

The `//tools/helm` module replaces the previous string-building approach with a clean template-based system for generating Helm charts.

## Components

### Core Files

- `helm_chart_release.bzl` - Bazel rule for generating Helm charts using templates
- `template_renderer.py` - Python utility for template processing
- `BUILD.bazel` - Module definition and targets

### Templates

- `templates/Chart.yaml.template` - Template for Helm Chart.yaml metadata
- `templates/values.yaml.template` - Template for values.yaml configuration  
- `templates/*.yaml` - Kubernetes manifest templates (deployment, service)
- `templates/_helpers.tpl` - Helm template helpers

## Usage

```starlark
load("//tools/helm:helm_chart_release.bzl", "helm_chart_release_macro")

helm_chart_release_macro(
    domain = "myapp",
    charts = {
        "api_chart": {
            "description": "API services",
            "apps": ["my_api", "my_worker"]
        }
    }
)
```

## Migration from String Building

This module replaces the previous implementation that used extensive string concatenation in functions like:
- `_create_chart_yaml_content()` - Now uses `Chart.yaml.template`
- `_create_values_yaml_content()` - Now uses `values.yaml.template`

The template-based approach provides:
- Better maintainability
- Cleaner separation of concerns
- Easier customization
- Consistent formatting

## Template Variables

### Chart.yaml Template Variables
- `${chart_name}` - Generated chart name (domain-chartname)
- `${description}` - Chart description
- `${domain}` - Domain name
- `${chart_version}` - Chart version

### Values.yaml Template Variables
- `${domain}` - Domain name
- `${image_configs}` - Generated image configurations for all apps
- `${app_configs}` - Generated app-specific configurations  
- `${overrides_section}` - User-provided overrides

## Example Output

For a chart with domain "manman" and apps ["api", "worker"], the system generates:

**Chart.yaml:**
```yaml
apiVersion: v2
name: manman-host_chart
description: ManMan host services (APIs)
type: application
version: 1.0.0
# ... additional metadata
```

**values.yaml:**
```yaml
# Image configurations for each app
images:
  api:
    name: "ghcr.io/whale-net/manman-api"
    tag: "latest"
    repository: "ghcr.io/whale-net/manman-api"
  
# Domain-specific application configuration  
manman:
  apps:
    api:
      enabled: true
      version: "latest"
      replicas: 1
      # ... additional config
```