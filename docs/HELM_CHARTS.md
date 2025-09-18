# Helm Charts in Everything Monorepo

This document describes the Helm chart support in the Everything monorepo, which enables packaging and distributing Kubernetes applications alongside container images.

## Overview

The monorepo now supports automatic Helm chart generation for applications, addressing the challenge of deploying applications in a monorepo setup where the traditional "one repo per app with Git tags" approach doesn't scale.

### Key Features

- **Automatic Chart Generation**: Charts are generated from templates with baked-in image versions
- **GitHub Pages Hosting**: Chart repository hosted on GitHub Pages for easy access
- **Version Coordination**: Image versions are automatically baked into chart releases
- **Monorepo Support**: Multiple charts can be released independently
- **Seamless Integration**: Works with existing release workflow

## Chart Naming Convention

Charts follow the same domain+app naming pattern as container images for consistency:

**Target Names:**
- Chart generation target: `{domain}_{app}_helm_chart`
- Chart package target: `{domain}_{app}_helm_package`

**Chart Names:**
- Chart name in repository: `{domain}-{app}`
- Image reference: `ghcr.io/whale-net/{domain}-{app}:{version}`

**Example for `hello_fastapi` (domain: `demo`):**
```bash
# Bazel targets
//demo/hello_fastapi:demo_hello_fastapi_helm_chart
//demo/hello_fastapi:demo_hello_fastapi_helm_package

# Chart name in repository
demo-hello_fastapi

# Referenced image
ghcr.io/whale-net/demo-hello_fastapi:v1.2.3
```

This ensures direct correspondence between Helm charts and their associated container images.

## Enabling Helm Charts for an App

To enable Helm chart generation for an application, update the `release_app` call in your app's `BUILD.bazel` file:

```starlark
load("//tools:release.bzl", "release_app")

release_app(
    name = "my_app",
    binary_target = ":my_app",
    language = "python",
    domain = "api",
    description = "My awesome application",
    helm_chart = True,           # Enable Helm chart generation
    chart_version = "0.1.0",     # Optional: explicit chart version
)
```

### Parameters

- `helm_chart` (bool): Enable Helm chart generation (default: False)
- `chart_version` (str): Helm chart version (defaults to app version)

## Generated Chart Structure

When enabled, the system generates a complete Helm chart with:

- **Chart.yaml**: Chart metadata with app information
- **values.yaml**: Default values with baked-in image version
- **templates/**: Kubernetes manifests (Deployment, Service, ServiceAccount, Ingress)
- **templates/_helpers.tpl**: Helm template helpers

### Chart Features

- **Multi-platform image support**: Uses the same registry/tag format as containers
- **Health checks**: Configurable readiness/liveness probes
- **Ingress support**: Optional ingress with SSL/TLS
- **Resource management**: CPU/memory limits and requests
- **Autoscaling**: Horizontal Pod Autoscaler support
- **Security**: ServiceAccount and SecurityContext configuration

## Using the Release Helper

### Build a Chart

```bash
# Build Helm chart for an app
bazel run //tools:release -- helm-build my_app
```

### Package a Chart

```bash
# Package Helm chart into .tgz
bazel run //tools:release -- helm-package my_app
```

### Validate a Chart

```bash
# Validate chart structure
bazel run //tools:release -- helm-validate my_app

# Also run helm lint (if helm CLI is available)
bazel run //tools:release -- helm-validate my_app --lint
```

## Chart Repository

Charts are hosted on GitHub Pages at: `https://whale-net.github.io/everything`

### Adding the Repository

```bash
# Add the chart repository
helm repo add everything https://whale-net.github.io/everything

# Update repository index
helm repo update

# Search for charts
helm search repo everything/
```

### Installing Charts

```bash
# Install a chart
helm install my-release everything/hello_fastapi

# Install with custom values
helm install my-release everything/hello_fastapi \
  --set image.tag=v1.2.3 \
  --set service.type=NodePort

# Upgrade a release
helm upgrade my-release everything/hello_fastapi \
  --set image.tag=v1.2.4
```

## Deployment Workflow

### Manual Deployment

Deploy specific charts to GitHub Pages:

```bash
# Deploy specific charts
gh workflow run deploy-helm-charts.yml \
  -f charts=hello_fastapi,hello_python \
  -f version=v1.0.0

# Deploy all charts with Helm enabled
gh workflow run deploy-helm-charts.yml \
  -f charts=all
```

### Automatic Deployment

Charts are automatically deployed to GitHub Pages when:

1. A successful release workflow completes on the main branch
2. The release includes apps with Helm charts enabled

## Chart Versioning Strategy

### Image Version Coordination

The key innovation is "baking" image versions into chart releases:

1. **Chart Version**: Independent versioning for the chart itself (e.g., `0.1.0`)
2. **App Version**: Matches the container image version (e.g., `v1.2.3`)
3. **Image Tag**: Baked into `values.yaml` during chart generation

Example `values.yaml`:
```yaml
image:
  repository: ghcr.io/whale-net/demo-hello_fastapi
  tag: "v1.2.3"  # Baked-in version
  pullPolicy: IfNotPresent
```

### Release Coordination

When releasing an app with Helm charts:

1. Container image is built and tagged (e.g., `v1.2.3`)
2. Helm chart is generated with the same image version baked in
3. Chart is packaged and published to the repository
4. Users get a chart that references the exact image version

## Chart Customization

### Custom Templates

To customize chart templates for specific apps:

1. Create app-specific templates in `charts/templates/`
2. Use the same variable substitution format (`{{APP_NAME}}`, etc.)
3. Reference custom templates in the `release_app` call

### Configuration Options

Charts support extensive configuration through `values.yaml`:

```yaml
# Scaling
replicaCount: 3
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10

# Networking
service:
  type: LoadBalancer
  port: 443
ingress:
  enabled: true
  hosts:
    - host: myapp.example.com

# Resources
resources:
  limits:
    cpu: 500m
    memory: 512Mi
  requests:
    cpu: 250m
    memory: 256Mi

# Application
app:
  env:
    LOG_LEVEL: debug
    DATABASE_URL: postgresql://...
```

## Troubleshooting

### Chart Not Found

If a chart isn't being generated:

1. Verify `helm_chart = True` in the `release_app` call
2. Check that the app builds successfully
3. Validate chart templates are present in `tools/charts/templates/`

### Deployment Issues

For deployment problems:

1. Check GitHub Pages is enabled in repository settings
2. Verify workflow permissions include `pages: write`
3. Review workflow logs for specific errors

### Chart Validation Errors

For chart validation failures:

1. Run `helm-validate` with `--lint` for detailed errors
2. Check Chart.yaml syntax and required fields
3. Validate Kubernetes manifest templates

## Examples

### Basic FastAPI App

See `demo/hello_fastapi/BUILD.bazel` for a complete example of an app with Helm charts enabled.

### Deploying to Kubernetes

```bash
# Add repository
helm repo add everything https://whale-net.github.io/everything
helm repo update

# Install the chart
helm install hello-app everything/hello_fastapi

# Check deployment
kubectl get pods
kubectl get services

# Access the application
kubectl port-forward service/hello-app-hello-fastapi 8080:80
# Visit http://localhost:8080
```

This setup provides a complete solution for managing Kubernetes deployments in a monorepo environment while maintaining the coordination between application versions and their deployment manifests.