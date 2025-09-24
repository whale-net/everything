# ManMan Helm Chart - Generic Service Configuration

This Helm chart has been refactored to support a generic, single-version approach that eliminates duplication while supporting all application deployment modes.

## Supported Service Types

### API Services (`type: "api"`)
- **Experience API**: User-facing game server management
- **Status API**: Read-only monitoring and health endpoints
- **Worker DAL API**: Data access layer for workers

**Features:**
- HTTP service exposure
- Health check probes on HTTP endpoints
- Automatic Kubernetes Service creation
- Ingress integration

### Processor Services (`type: "processor"`)
- **Status Processor**: Background event processing (headless)

**Features:**
- No Kubernetes Service (headless)
- Optional health endpoints for monitoring
- Background processing workloads

## Key Improvements

### 1. Eliminated Template Duplication
- **Before**: 12 separate template files (deployment + service + pdb per service)
- **After**: 5 generic templates that handle all services

### 2. Generic Service Configuration
```yaml
services:
  my-service:
    enabled: true
    type: "api"  # or "processor"
    component: "my-component"
    command: "start-my-service"
    # ... rest of config
```

### 3. Flexible Resource Management
- Global defaults with per-service overrides
- Configurable probe settings
- Resource limits and requests

### 4. Enhanced Features
- Pod Disruption Budgets
- Horizontal Pod Autoscaling
- Advanced ingress configuration
- Comprehensive labeling strategy

## Configuration Examples

### API Service Configuration
```yaml
services:
  my-api:
    enabled: true
    type: "api"
    component: "my-api"
    command: "start-my-api"
    replicas: 2
    port: 8080
    healthPath: "/health"
    image:
      name: "my-registry/my-api"
      tag: "v1.0.0"
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        cpu: 500m
        memory: 512Mi
```

### Processor Service Configuration
```yaml
services:
  my-processor:
    enabled: true
    type: "processor"
    component: "my-processor"
    command: "start-my-processor"
    replicas: 1
    # Optional health endpoint for monitoring
    healthPort: 8000
    healthPath: "/health"
    image:
      name: "my-registry/my-processor"
      tag: "v1.0.0"
```

### Ingress Configuration
```yaml
ingress:
  enabled: true
  host: "api.example.com"
  servicePaths:
    my-api: "/api/v1"
    my-other-api: "/api/v2"
  tls:
    enabled: true
    configs:
      - secretName: api-tls
        hosts:
          - api.example.com
```

## Migration from Old Structure

The chart maintains backwards compatibility through the deprecated `apis` and `processors` sections. To migrate:

1. **Disable old configuration**:
   ```yaml
   apis:
     experience:
       enabled: false
   processors:
     status:
       enabled: false
   ```

2. **Enable new configuration**:
   ```yaml
   services:
     manman-experience:
       enabled: true
       type: "api"
       # ... rest of config
   ```

## Deployment Scenarios

### Development Environment
- Enable all services with minimal resources
- Use ingress with localhost
- Enable autoscaling for testing

### Production Environment
- Configure resource limits based on load
- Use proper TLS certificates
- Configure Pod Disruption Budgets
- Enable monitoring and logging

### Microservice-Only Deployment
- Disable unwanted services
- Deploy only specific components
- Use external ingress controllers

## Template Structure

```
templates/
├── _helpers.tpl                    # Common functions and labels
├── deployment.yaml                 # Generic deployment for all services
├── service.yaml                    # Service creation for API services
├── ingress-new.yaml               # Updated ingress with service mapping
├── pod-disruption-budget.yaml     # PDB for all services
├── horizontal-pod-autoscaler.yaml # HPA for all services
└── migration-job.yaml             # Database migration job
```

## Benefits

1. **Single Chart Version**: No need for multiple chart versions
2. **Reduced Duplication**: 90% less template code
3. **Generic Configuration**: Supports current and future services
4. **Enhanced Features**: PDB, HPA, advanced ingress
5. **Backwards Compatible**: Existing deployments continue to work
6. **Flexible**: Supports all deployment modes (API, processor, mixed)

## Future Extensibility

Adding new services is now trivial:

```yaml
services:
  my-new-service:
    enabled: true
    type: "api"  # or "processor"
    component: "my-new-service"
    command: "start-my-new-service"
    # Standard configuration applies
```

No template changes required for new services.