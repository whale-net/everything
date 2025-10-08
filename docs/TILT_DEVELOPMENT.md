# Local Development with Tilt

This guide explains how to use [Tilt](https://tilt.dev/) for local Kubernetes development in the Everything monorepo.

## Overview

Tilt provides a fast, iterative development experience for Kubernetes applications. This monorepo includes Tilt configurations for:

- **Demo Apps**: Simple example apps showcasing all app types (external-api, internal-api, worker, job)
- **Manman**: Production multi-service application with database, message queue, and observability

## Prerequisites

1. **Tilt**: Install from [tilt.dev](https://docs.tilt.dev/install.html)
   ```bash
   # macOS
   brew install tilt-dev/tap/tilt
   
   # Linux
   curl -fsSL https://raw.githubusercontent.com/tilt-dev/tilt/master/scripts/install.sh | bash
   ```

2. **Kubernetes Cluster**: One of the following:
   - [Docker Desktop](https://www.docker.com/products/docker-desktop) (easiest, includes K8s)
   - [Minikube](https://minikube.sigs.k8s.io/docs/start/)
   - [Kind](https://kind.sigs.k8s.io/)
   - [k3d](https://k3d.io/)

3. **Bazel**: Required for building images and helm charts
   ```bash
   brew install bazelisk
   ```

4. **Helm**: Required for deploying charts
   ```bash
   brew install helm
   ```

## Quick Start

### 1. Setup Environment

Create your `.env` file from the example:

```bash
cp .env.example .env
```

Edit `.env` to enable the services you want:

```bash
# Enable demo apps
TILT_ENABLE_DEMO=true
DEMO_ENABLE_FASTAPI=true

# Optionally enable manman
TILT_ENABLE_MANMAN=false
```

### 2. Build Helm Charts

Before running Tilt, you need to build the helm charts:

```bash
# Build demo charts (if enabled)
bazel build //demo:fastapi_chart
bazel build //demo:internal_api_chart
bazel build //demo:worker_chart
bazel build //demo:job_chart

# Or build all demo charts at once
bazel build //demo/...

# Build manman chart (if enabled)
bazel build //manman:manman_chart
```

### 3. Start Tilt

```bash
tilt up
```

This will:
- Create Kubernetes namespaces
- Deploy infrastructure (ingress controller, databases, etc.)
- Build and deploy your applications
- Open the Tilt UI in your browser (http://localhost:10350)

### 4. Access Your Services

The Tilt UI will show all running services with their logs and status. Access points:

- **Demo FastAPI**: http://localhost:30080 (via ingress) or http://localhost:8000 (direct)
- **Demo Internal API**: http://localhost:8080 (direct port-forward)
- **Manman Experience API**: http://localhost:30080/experience/ (if enabled)

### 5. Stop Tilt

```bash
tilt down
```

## Architecture

### Directory Structure

```
.
├── Tiltfile                 # Root orchestrator
├── .env.example             # Configuration template
├── demo/
│   ├── Tiltfile             # Demo apps configuration
│   └── .env.example         # Demo-specific settings
└── manman/
    └── Tiltfile             # Manman services configuration
```

### How It Works

1. **Root Tiltfile**: Orchestrates which domains to load (demo, manman)
2. **Domain Tiltfiles**: Configure and deploy apps for each domain
3. **Bazel Integration**: Uses Bazel-built OCI images and Helm charts
4. **Helm Charts**: Deploy apps with proper Kubernetes resources

### Build Flow

```
Source Code → Bazel Build → OCI Image → Docker → Kubernetes → Tilt Watch
```

Tilt watches your source files and automatically rebuilds when changes are detected.

## Configuration

### Environment Variables

All configuration is done via `.env` files:

- **Root `.env`**: Controls which domains are enabled
- **`demo/.env`**: Controls which demo apps run
- **`manman/.env`**: Controls manman services (if exists)

### Service Control

Enable/disable services by setting environment variables:

```bash
# In .env
TILT_ENABLE_DEMO=true
TILT_ENABLE_MANMAN=false

# In demo/.env
DEMO_ENABLE_FASTAPI=true
DEMO_ENABLE_INTERNAL_API=false
DEMO_ENABLE_WORKER=false
```

## App Types

The monorepo supports 4 app types, each with different Kubernetes resources:

| App Type | Deployment | Service | Ingress | Use Case |
|----------|------------|---------|---------|----------|
| `external-api` | ✅ | ✅ | ✅ | Public HTTP APIs |
| `internal-api` | ✅ | ✅ | ❌ | Internal services |
| `worker` | ✅ | ❌ | ❌ | Background processors |
| `job` | ❌ (Job) | ❌ | ❌ | Migrations, cron tasks |

### Demo Apps Examples

- **hello-fastapi**: `external-api` - FastAPI app with ingress
- **hello-internal-api**: `internal-api` - API without ingress
- **hello-worker**: `worker` - Background processor
- **hello-python**: `worker` - Simple Python worker
- **hello-go**: `worker` - Simple Go worker
- **hello-job**: `job` - One-time job

## Development Workflows

### Single App Development

Focus on one app to reduce resource usage:

```bash
# In .env
TILT_ENABLE_DEMO=true
TILT_ENABLE_MANMAN=false

# In demo/.env
DEMO_ENABLE_FASTAPI=true
DEMO_ENABLE_INTERNAL_API=false
DEMO_ENABLE_WORKER=false
DEMO_ENABLE_HELLO_PYTHON=false
DEMO_ENABLE_HELLO_GO=false
DEMO_ENABLE_JOB=false
```

### Full Stack Development

Run everything:

```bash
# In .env
TILT_ENABLE_DEMO=true
TILT_ENABLE_MANMAN=true

# Enable all services in respective .env files
```

### Multi-Service Development

Work on related services:

```bash
# In demo/.env
DEMO_ENABLE_FASTAPI=true
DEMO_ENABLE_INTERNAL_API=true
DEMO_ENABLE_WORKER=true
```

## Troubleshooting

### Tilt Won't Start

**Problem**: Tilt fails with "no Kubernetes context"

**Solution**: Ensure Kubernetes is running:
```bash
kubectl cluster-info
```

If not running, start Docker Desktop or your K8s cluster.

### Helm Charts Not Found

**Problem**: Tilt fails with "chart not found"

**Solution**: Build the helm charts first:
```bash
bazel build //demo/...
```

### Image Pull Errors

**Problem**: Kubernetes can't find the image

**Solution**: The Tiltfile uses `image_load` targets which require building first:
```bash
# For demo apps
bazel build //demo/hello_fastapi:hello-fastapi_image_load

# Then run tilt
tilt up
```

### Port Already in Use

**Problem**: Port 30080 or 8000 already in use

**Solution**: 
1. Stop other services using those ports
2. Or modify the port forwards in the Tiltfile

### Bazel Build Fails

**Problem**: Bazel can't build images or charts

**Solution**:
```bash
# Clean bazel cache
bazel clean --expunge

# Rebuild
bazel build //demo/...
```

### Platform Issues (ARM64 vs AMD64)

**Problem**: Images don't run in Kubernetes

**Solution**: The Tiltfile uses platform-specific builds. Adjust the `--platforms` flag:

```python
# For ARM64 (M1/M2 Macs)
'bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_arm64'

# For AMD64 (Intel Macs/Linux)
'bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_x86_64'
```

## Advanced Usage

### Custom Namespace

Modify the namespace in the domain Tiltfile:

```python
# demo/Tiltfile
namespace = 'my-custom-namespace'
```

### Custom Helm Values

Override values during deployment:

```python
k8s_yaml(
    helm(
        'bazel-bin/demo/fastapi_chart',
        name='hello-fastapi',
        namespace=namespace,
        set=[
            'global.environment=dev',
            'apps.hello-fastapi.replicas=2',  # Scale to 2 replicas
            'apps.hello-fastapi.resources.requests.memory=512Mi',
        ]
    )
)
```

### Resource Groups

Organize resources in the Tilt UI:

```python
k8s_resource(
    workload='hello-fastapi-dev',
    labels=['api', 'frontend'],  # Group by labels
)
```

### Live Reload

Tilt automatically rebuilds when source changes. For faster iteration:

```python
# Use sync instead of rebuild for static files
docker_build(
    'my-app',
    context='.',
    live_update=[
        sync('./src', '/app/src'),
        run('pip install -r requirements.txt', trigger='./requirements.txt'),
    ]
)
```

## Integration with Bazel

### Bazel-Generated Helm Charts

The monorepo uses Bazel to generate Helm charts automatically:

```starlark
# BUILD.bazel
release_helm_chart(
    name = "fastapi_chart",
    apps = ["//demo/hello_fastapi:hello-fastapi_metadata"],
    chart_name = "hello-fastapi",
    namespace = "demo",
    environment = "production",
    domain = "demo",
)
```

Tilt deploys these pre-generated charts:

```python
# Tiltfile
k8s_yaml(
    helm('bazel-bin/demo/fastapi_chart', ...)
)
```

### Bazel-Built Images

Images are built using Bazel with cross-compilation support:

```python
local_resource(
    'build-hello-fastapi',
    'bazel run //demo/hello_fastapi:hello-fastapi_image_load --platforms=//tools:linux_arm64',
    deps=['demo/hello_fastapi'],
)
```

## Comparison with Other Tools

### Tilt vs Docker Compose

**Tilt** (Current approach):
- ✅ Real Kubernetes environment
- ✅ Matches production closely
- ✅ Full K8s features (ingress, services, namespaces)
- ✅ Better for microservices
- ❌ Requires K8s cluster

**Docker Compose**:
- ✅ Simpler setup
- ✅ Faster startup
- ❌ Different from production
- ❌ Limited networking
- ✅ Good for simple apps

### Tilt vs Skaffold

Both are similar, but Tilt has:
- Better UI/UX
- More flexible configuration
- Better resource organization
- Live updates

## Best Practices

1. **Start Small**: Enable only the services you need
2. **Use Labels**: Organize resources with labels in the Tilt UI
3. **Watch Logs**: Use Tilt UI to monitor logs in real-time
4. **Resource Limits**: Set appropriate CPU/memory limits for local dev
5. **Clean Up**: Run `tilt down` when done to free resources
6. **Version Control**: Don't commit `.env` files (use `.env.example`)
7. **Build First**: Always build helm charts before running tilt
8. **Platform Awareness**: Use correct `--platforms` flag for your architecture

## References

- [Tilt Documentation](https://docs.tilt.dev/)
- [Tilt Extensions](https://github.com/tilt-dev/tilt-extensions)
- [Everything Monorepo README](../README.md)
- [Helm Chart Documentation](../tools/helm/README.md)
- [Release System Documentation](../docs/HELM_RELEASE.md)

## Getting Help

- **Tilt UI**: http://localhost:10350 (shows resource status and logs)
- **Tilt Logs**: Run `tilt logs <resource-name>` in terminal
- **Kubernetes**: Use `kubectl` for debugging
  ```bash
  kubectl get pods -n demo-dev
  kubectl describe pod <pod-name> -n demo-dev
  kubectl logs <pod-name> -n demo-dev
  ```

---

**Pro Tip**: Add the Tilt UI to your bookmarks for quick access during development!
