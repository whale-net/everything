# Enhanced Helm Chart Framework

A reusable, configurable Helm chart system that integrates with the Everything monorepo's `release_app` pattern to provide consistent, production-ready deployments with optional components.

## Overview

The Enhanced Helm Chart Framework builds upon the existing chart tooling to provide:

- **Generic Service Support**: API services, background processors, and jobs
- **Optional Components**: Pod Disruption Budgets, Horizontal Pod Autoscaling, Ingress
- **Consistent Naming**: Follows repository conventions and integrates with release system
- **Flexible Configuration**: Per-app and global configuration options
- **Production Ready**: Built-in best practices for resource management and observability

## Key Features

### Service Types

1. **API Services** (`type: "api"`)
   - HTTP services with health checks
   - Kubernetes Service creation
   - Ingress integration
   - Default port: 8000

2. **Processor Services** (`type: "processor"`)
   - Background/headless services
   - Optional health endpoints
   - No Kubernetes Service by default
   - Suitable for pub/sub workers, processors

3. **Job Services** (`type: "job"`)
   - Batch/one-time jobs
   - Kubernetes Job resources
   - Configurable completions and parallelism

### Optional Components

All components are **opt-in** to avoid unnecessary complexity:

- **Pod Disruption Budget (PDB)**: Ensures availability during node maintenance
- **Horizontal Pod Autoscaler (HPA)**: Automatic scaling based on CPU/memory
- **Ingress**: External traffic routing with TLS support

## Usage

### Basic Configuration

```starlark
# In your BUILD.bazel file
load("//tools:enhanced_helm_chart_release.bzl", "enhanced_helm_chart_release_macro")

enhanced_helm_chart_release_macro(
    domain = "myapp",
    charts = {
        "production_chart": {
            "description": "MyApp production services",
            "apps": ["api_server", "background_processor"],
            "features": {
                "pdb": "true",      # Enable PDB for high availability
                "hpa": "true",      # Enable autoscaling
                "ingress": "true"   # Enable external access
            }
        }
    }
)
```

### Service Configuration

The framework automatically detects service types based on naming conventions:
- Names containing `processor`, `worker` → `type: "processor"`
- All others → `type: "api"`

Override with explicit configuration:

```yaml
# In generated values.yaml
myapp:
  apps:
    my_service:
      enabled: true
      type: "api"  # or "processor", "job"
      command: "start-my-service"
      replicas: 2
      port: 8080
      healthPath: "/health"
```

### Feature Configuration

#### Pod Disruption Budget
```yaml
myapp:
  apps:
    my_api:
      podDisruptionBudget:
        enabled: true
        minAvailable: 1  # or "50%"
```

#### Horizontal Pod Autoscaler
```yaml
myapp:
  apps:
    my_api:
      autoscaling:
        enabled: true
        minReplicas: 2
        maxReplicas: 10
        targetCPUUtilizationPercentage: 70
        targetMemoryUtilizationPercentage: 80
```

#### Ingress
```yaml
# Global ingress configuration
ingress:
  enabled: true
  host: "api.example.com"
  ingressClassName: "nginx"
  tls:
    enabled: true
    configs:
      - secretName: "api-tls"
        hosts: ["api.example.com"]

# Per-app ingress configuration
myapp:
  apps:
    my_api:
      ingress:
        enabled: true
        path: "/api/v1"
        pathType: "Prefix"
```

## Chart Templates

The framework provides these templates:

- `deployment-enhanced.yaml`: Generic deployment supporting all service types
- `service-enhanced.yaml`: Service creation for API services only
- `pdb.yaml`: Pod Disruption Budget (optional)
- `hpa.yaml`: Horizontal Pod Autoscaler (optional)
- `ingress.yaml`: Ingress with multi-app support (optional)
- `_helpers-enhanced.tpl`: Helper functions and labels

## Integration with Release System

The framework integrates seamlessly with the existing `release_app` pattern:

```starlark
# App definition with release_app
release_app(
    name = "my_api",
    binary_target = "//myapp:my_api",
    language = "python",
    domain = "myapp",
    description = "My API service",
)

# Chart generation references the same apps
enhanced_helm_chart_release_macro(
    domain = "myapp",
    charts = {
        "chart": {
            "apps": ["my_api"],  # Matches release_app name
            "features": {"ingress": "true"}
        }
    }
)
```

## Naming Conventions

The framework follows consistent naming patterns:

- **Deployments**: `{domain}-{chart_name}-{app_name}`
- **Services**: `{domain}-{chart_name}-{app_name}-service`
- **PDBs**: `{domain}-{chart_name}-{app_name}-pdb`
- **HPAs**: `{domain}-{chart_name}-{app_name}-hpa`
- **Ingress**: `{domain}-{chart_name}-ingress`

## Examples

### Simple API Service
```starlark
enhanced_helm_chart_release_macro(
    domain = "demo",
    charts = {
        "api_chart": {
            "apps": ["hello_api"],
            "features": {"ingress": "true"}
        }
    }
)
```

### Complex Multi-Service Application
```starlark
enhanced_helm_chart_release_macro(
    domain = "manman",
    charts = {
        "production_chart": {
            "description": "ManMan production deployment",
            "apps": [
                "experience_api",    # type: api
                "status_api",        # type: api  
                "worker_dal_api",    # type: api
                "status_processor"   # type: processor
            ],
            "features": {
                "pdb": "true",       # PDB for all services
                "hpa": "true",       # HPA for APIs (auto-detected)
                "ingress": "true"    # Ingress for APIs only
            },
            "custom_values": {
                "global.env.APP_ENV": "production",
                "ingress.host": "api.manman.com"
            }
        }
    }
)
```

### Environment-Specific Charts
```starlark
enhanced_helm_chart_release_macro(
    domain = "myapp",
    charts = {
        # Development - minimal features
        "dev_chart": {
            "apps": ["api_server"],
            "features": {"ingress": "true"},
            "custom_values": {
                "ingress.host": "localhost"
            }
        },
        
        # Staging - some features
        "staging_chart": {
            "apps": ["api_server", "processor"],
            "features": {
                "hpa": "true",
                "ingress": "true"
            }
        },
        
        # Production - all features
        "prod_chart": {
            "apps": ["api_server", "processor", "worker"],
            "features": {
                "pdb": "true",
                "hpa": "true",
                "ingress": "true"
            }
        }
    }
)
```

## Migration from Manual Charts

To migrate from manual Helm charts:

1. **Identify service types** in your existing deployments
2. **Map configuration** to the enhanced framework structure
3. **Enable features** as needed (PDB, HPA, Ingress)
4. **Test with generated charts** before removing manual charts
5. **Update CI/CD** to use generated charts

## Benefits

- **Reduced Duplication**: Single chart definition supports multiple deployment modes
- **Consistent Standards**: Enforces naming conventions and best practices
- **Optional Complexity**: Only include features you need
- **Production Ready**: Built-in observability, resource management, and scaling
- **Easy Maintenance**: Updates to framework benefit all applications
- **Integration**: Works seamlessly with existing release system

## File Structure

```
tools/
├── enhanced_helm_chart_release.bzl     # Enhanced chart rule
├── templates/
│   ├── _helpers-enhanced.tpl           # Helper functions
│   ├── deployment-enhanced.yaml        # Generic deployment
│   ├── service-enhanced.yaml           # Service for APIs
│   ├── pdb.yaml                        # Pod Disruption Budget
│   ├── hpa.yaml                        # Horizontal Pod Autoscaler
│   ├── ingress.yaml                    # Ingress with multi-app support
│   └── values-enhanced-example.yaml    # Example configuration
└── BUILD.bazel                         # Template file groups
```