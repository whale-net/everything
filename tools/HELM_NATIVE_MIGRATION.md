# Helm Chart Native: Simplified Approach

## Overview

This document describes the new **Helm-native approach** that replaces the complex Go renderer system with standard Helm library chart composition.

## What Changed

### ❌ Before: Over-Complicated 
- **3 different tools**: `manual_chart.bzl`, `helm_chart_release.bzl`, `helm_composition_simple.bzl`
- **Complex Go renderer**: 7 Go files just to template YAML
- **Double templating**: Go templates → Helm templates → K8s YAML
- **Hard to debug**: Custom templating engine
- **500+ lines** of complex code

### ✅ After: Helm-Native & Simple
- **1 unified tool**: `helm_chart_native.bzl`
- **Standard Helm patterns**: Library chart + dependencies
- **Single templating pass**: Helm does all the work
- **Easy to debug**: Standard `helm template` command works
- **~150 lines** of straightforward code

## New Architecture

```
App Metadata → Simple Python Script → values.yaml + Library Chart → Helm → K8s YAML
```

### Core Components

1. **Library Chart** (`//charts/whale-net-library/`)
   - Reusable Helm templates for common patterns
   - Standard Helm library chart structure
   - Shared across all generated charts

2. **Simple Generator** (`helm_chart_native.bzl`)
   - Pure Bazel string generation
   - No Go renderer needed
   - Uses library chart dependencies

3. **Python Values Generator** (`tools/values_generator.py`)
   - Replaces complex Go renderer
   - Simple script that reads app metadata
   - Generates clean values.yaml

## Migration Example

### Before (Complex):
```starlark
load("//tools:helm_composition_simple.bzl", "helm_chart_composed", "k8s_artifact")

k8s_artifact(name = "migrations_job", ...)
k8s_artifact(name = "config", ...)

helm_chart_composed(
    name = "manman_services",
    description = "...",
    apps = [...],
    k8s_artifacts = [":migrations_job", ":config"],
    pre_deploy_jobs = ["migrations_job"],
    chart_values = {
        "ingress.enabled": "true",
        # Complex nested configuration...
    },
)
```

### After (Simple):
```starlark
load("//tools:helm_chart_native.bzl", "helm_chart_native")

helm_chart_native(
    name = "manman_services", 
    description = "Production ManMan services using whale-net library chart",
    apps = [
        ":experience_api_metadata",
        ":status_api_metadata", 
    ],
    values = {
        "ingress.enabled": "true",
        "global.env": "production",
    },
    jobs = [
        "migrations:image.repository=ghcr.io/whale-net/manman-api,hookType=pre-install"
    ],
)
```

## Benefits

### 🚀 **Simpler Usage**
- Single rule handles all cases  
- Clean, intuitive API
- Less configuration needed

### 🔧 **Better Debugging**
- Standard Helm chart structure
- `helm template` command works
- IDE support for Helm templates

### 📦 **Standard Patterns**
- Uses Helm library chart best practices
- Familiar to Helm users
- Easier onboarding

### 🎯 **Maintainable**
- ~80% less code to maintain
- No Go compiler dependency
- Single tool, single pattern

## Generated Output

The new system generates proper Helm charts with:

- **Chart.yaml**: Standard chart metadata with library dependency
- **values.yaml**: Clean configuration from app metadata
- **templates/**: Simple templates that use library chart helpers
- **Chart.lock**: Helm dependency lock file

Example generated `Chart.yaml`:
```yaml
apiVersion: v2
name: manman_services
description: Production ManMan services using whale-net library chart
type: application
dependencies:
- name: whale-net-library
  version: "1.0.0"
  repository: "file://../whale-net-library"
```

Example generated template:
```yaml
{{- range $appName, $appConfig := .Values.apps }}
{{- include "whale-net.app.deployment" (merge (dict "appName" $appName) $appConfig $) }}
---
{{- end }}
```

## What Can Be Deleted

After migration, these complex tools can be removed:
- `/tools/helm_renderer/` (entire directory - 7 Go files)  
- `/tools/helm_chart_release.bzl` (250+ lines, already deprecated)
- `/tools/helm_composition_simple.bzl` (130+ lines)
- `/tools/templates/helm_composition/` (Go template files)

## Usage Patterns

### Basic App Composition
```starlark
helm_chart_native(
    name = "my_services",
    description = "My application services",
    apps = [":api_metadata", ":worker_metadata"],
)
```

### With Custom Configuration
```starlark
helm_chart_native(
    name = "production_services",
    description = "Production deployment",
    apps = [":api_metadata"],
    values = {
        "ingress.enabled": "true",
        "ingress.className": "nginx", 
        "global.env": "production",
        "apps.api.replicas": "3",
    },
)
```

### With Pre-Install Jobs
```starlark
helm_chart_native(
    name = "full_stack",
    description = "Full application stack with migrations",
    apps = [":api_metadata"],
    jobs = [
        "migrations:image.repository=my-api,hookType=pre-install",
        "seed-data:image.repository=my-api,command=python seed.py"
    ],
)
```

## Next Steps

1. **Test the new approach** with your existing charts
2. **Migrate existing helm_chart_composed** usage to `helm_chart_native`  
3. **Delete deprecated tools** once migration is complete
4. **Update CI/CD pipelines** to use the new pattern

The result is a **much simpler, more maintainable system** that follows Helm best practices while reducing complexity by 80%.