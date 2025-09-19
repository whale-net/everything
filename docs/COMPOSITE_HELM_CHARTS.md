# Composite Helm Charts for Multi-App Deployments

This document describes the composite chart feature that allows deploying multiple applications together in a single Helm chart, addressing the need for coordinated multi-app deployments while reducing cross-deployment coordination complexity.

## Problem Statement

In a microservices architecture, you often have groups of related services that need to be deployed together:

- **Web Services**: Frontend, API gateway, and web APIs that form a complete web application
- **Data Pipeline**: ETL services, processors, and data APIs that work together
- **Admin Services**: Monitoring, logging, and admin tools that support the main application

The current 1:1 app-to-chart mapping requires:
- Deploying each service individually 
- Coordinating versions across multiple deployments
- Managing dependencies between services manually
- Complex ingress and service discovery configuration

## Solution: Composite Charts

Composite charts bundle multiple apps into a single deployment while maintaining the benefits of individual app development:

```starlark
# Create a composite chart for all web services
release_composite_helm_chart(
    name = "web_services_composite",
    composite_name = "web-services",
    description = "Complete web application stack",
    chart_version = "0.1.0",
    domain = "web",
    apps = [
        "web/frontend",     # React frontend
        "api/gateway",      # API gateway
        "api/user_service", # User management API
        "api/product_service", # Product catalog API
    ],
)
```

## Features

### 🎯 **Multi-App Coordination**
- **Single Deployment**: Deploy all related services with one command
- **Coordinated Versioning**: All apps use consistent image versions
- **Shared Configuration**: Common settings (ingress, service account, etc.)
- **Dependency Management**: Services can reference each other directly

### 📦 **Flexible Configuration**
- **Per-App Settings**: Individual configuration for each app
- **Global Settings**: Shared configuration across all apps
- **Selective Deployment**: Enable/disable individual apps
- **Environment Overrides**: Different settings per environment

### 🔧 **Operational Benefits**
- **Reduced Complexity**: Single chart instead of multiple deployments
- **Atomic Rollouts**: All services update together or not at all
- **Simplified Monitoring**: Single deployment to monitor
- **Easier Troubleshooting**: All related services in one namespace

## Chart Structure

### Generated Files

```
web-services/
├── Chart.yaml              # Composite chart metadata
├── values.yaml             # Multi-app configuration
└── templates/
    ├── deployment.yaml      # Deployments for all apps
    ├── service.yaml         # Services for all apps  
    ├── ingress.yaml         # Shared ingress with routing
    ├── serviceaccount.yaml  # Shared service account
    └── _helpers.tpl         # Template helpers
```

### Configuration Structure

```yaml
# values.yaml for composite chart
global:
  imageRegistry: ghcr.io/whale-net
  imagePullSecrets: []

apps:
  frontend:
    enabled: true
    image:
      repository: ghcr.io/whale-net/web-frontend
      tag: "v1.2.3"
    service:
      port: 80
      targetPort: 3000
    
  gateway:
    enabled: true  
    image:
      repository: ghcr.io/whale-net/api-gateway
      tag: "v2.1.0"
    service:
      port: 80
      targetPort: 8080
    config:
      env:
        FRONTEND_URL: "http://web-services-frontend"
        USER_SERVICE_URL: "http://web-services-user-service"

ingress:
  enabled: true
  hosts:
    - host: myapp.example.com
      paths:
        - path: /
          serviceName: frontend
        - path: /api
          serviceName: gateway
```

## Usage Examples

### Building Composite Charts

```bash
# Build a composite chart
bazel run //tools:release -- helm-composite-build web-services \
  "web/frontend,api/gateway,api/user_service" \
  --chart-version 0.2.0 \
  --domain web \
  --description "Complete web application stack"
```

### Deploying Composite Charts

```bash
# Add chart repository
helm repo add everything https://whale-net.github.io/everything

# Deploy the composite chart
helm install my-web-app everything/web-services

# Upgrade with new versions
helm upgrade my-web-app everything/web-services \
  --set apps.frontend.image.tag=v1.3.0 \
  --set apps.gateway.image.tag=v2.2.0

# Disable specific apps
helm upgrade my-web-app everything/web-services \
  --set apps.user_service.enabled=false
```

### Custom Configuration

```yaml
# custom-values.yaml
apps:
  frontend:
    replicaCount: 3
    resources:
      limits:
        cpu: 500m
        memory: 512Mi
  
  gateway:
    replicaCount: 2
    config:
      env:
        LOG_LEVEL: debug
        RATE_LIMIT: "1000"

ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt
  hosts:
    - host: myapp.production.com
      paths:
        - path: /
          serviceName: frontend
        - path: /api
          serviceName: gateway
  tls:
    - secretName: myapp-tls
      hosts:
        - myapp.production.com

shared:
  database:
    enabled: true
    host: postgres.production.com
    port: 5432
```

```bash
# Deploy with custom configuration
helm install my-web-app everything/web-services -f custom-values.yaml
```

## Implementation Patterns

### 1. Domain-Based Grouping

Group apps by business domain:

```starlark
# E-commerce web services
release_composite_helm_chart(
    name = "ecommerce_web",
    composite_name = "ecommerce-web", 
    apps = ["web/storefront", "api/products", "api/cart", "api/checkout"],
    domain = "ecommerce",
)

# Admin and monitoring services  
release_composite_helm_chart(
    name = "admin_services",
    composite_name = "admin",
    apps = ["admin/dashboard", "monitoring/metrics", "logging/collector"],
    domain = "ops",
)
```

### 2. Environment-Specific Compositions

Different compositions for different environments:

```starlark
# Development - includes debug tools
release_composite_helm_chart(
    name = "dev_stack",
    composite_name = "dev-environment",
    apps = ["web/frontend", "api/gateway", "tools/debugger", "tools/profiler"],
    domain = "dev",
)

# Production - optimized stack
release_composite_helm_chart(
    name = "prod_stack", 
    composite_name = "production",
    apps = ["web/frontend", "api/gateway", "api/cache"],
    domain = "prod",
)
```

### 3. Layered Architecture

Separate infrastructure from application services:

```starlark
# Infrastructure layer
release_composite_helm_chart(
    name = "infrastructure",
    composite_name = "infra",
    apps = ["infra/postgres", "infra/redis", "infra/nginx"],
    domain = "infra",
)

# Application layer (depends on infrastructure)
release_composite_helm_chart(
    name = "application",
    composite_name = "app",
    apps = ["api/user_service", "api/product_service", "web/frontend"],
    domain = "app",
)
```

## Benefits vs. Individual Charts

| Aspect | Individual Charts | Composite Charts |
|--------|------------------|------------------|
| **Deployment** | Multiple `helm install` commands | Single `helm install` command |
| **Coordination** | Manual version management | Automatic coordination |
| **Configuration** | Separate values files | Unified configuration |
| **Rollbacks** | Per-service rollback | Atomic rollback |
| **Dependencies** | External service discovery | Internal service references |
| **Operational Overhead** | High (N deployments) | Low (1 deployment) |
| **Development Flexibility** | High (independent releases) | Medium (coordinated releases) |

## When to Use Composite Charts

### ✅ **Good Use Cases**
- **Tightly Coupled Services**: Services that always deploy together
- **Complete Applications**: Frontend + backend + APIs forming one product
- **Environment Stacks**: Dev/staging environments with multiple tools
- **Data Pipelines**: ETL processes with multiple processing stages

### ❌ **Not Recommended For**
- **Independent Services**: Services with different release cycles
- **Shared Infrastructure**: Database, message queues used by many apps
- **Cross-Team Services**: Services owned by different teams
- **Large Scale**: Too many apps (>10) in one chart

## Migration Strategy

### From Individual Charts to Composite

1. **Identify Related Services**: Group services that are always deployed together
2. **Create Composite Chart**: Define the new composite chart with existing apps  
3. **Test in Development**: Validate the composite chart works correctly
4. **Migrate Gradually**: Move environments one at a time
5. **Cleanup**: Remove individual charts once migration is complete

### Hybrid Approach

You can mix individual and composite charts:

```bash
# Core infrastructure (individual)
helm install postgres stable/postgresql
helm install redis stable/redis

# Web application (composite)  
helm install web-app everything/web-services

# Analytics service (individual)
helm install analytics everything/analytics-service
```

This composite chart feature provides the flexibility to deploy multiple apps together when it makes sense, while maintaining the option to deploy apps individually when needed.