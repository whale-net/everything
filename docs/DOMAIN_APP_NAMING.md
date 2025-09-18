# Domain+App Naming Pattern Examples

This document shows how the domain+app naming pattern creates direct correspondence between Helm charts and their associated release apps.

## Naming Pattern

All artifacts (images and charts) use consistent `domain+app` naming:

| Artifact Type | Pattern | Example |
|---------------|---------|---------|
| Container Image | `{domain}-{app}:{version}` | `demo-hello_fastapi:v1.0.0` |
| Helm Chart Name | `{domain}-{app}` | `demo-hello_fastapi` |
| Bazel Chart Target | `{domain}_{app}_helm_chart` | `demo_hello_fastapi_helm_chart` |
| Bazel Package Target | `{domain}_{app}_helm_package` | `demo_hello_fastapi_helm_package` |

## Direct Reference Examples

### Example 1: FastAPI Demo App

```starlark
# In demo/hello_fastapi/BUILD.bazel
release_app(
    name = "hello_fastapi",
    domain = "demo",
    helm_chart = True,
)
```

**Generated Artifacts:**
- **Container Image**: `ghcr.io/whale-net/demo-hello_fastapi:v1.0.0`
- **Helm Chart**: `demo-hello_fastapi` (in chart repository)
- **Bazel Targets**:
  - `//demo/hello_fastapi:demo_hello_fastapi_helm_chart`
  - `//demo/hello_fastapi:demo_hello_fastapi_helm_package`

**Chart Values:**
```yaml
image:
  repository: ghcr.io/whale-net/demo-hello_fastapi
  tag: "v1.0.0"  # Baked-in version
```

### Example 2: API Gateway App

```starlark
# In api/gateway/BUILD.bazel
release_app(
    name = "gateway",
    domain = "api",
    helm_chart = True,
)
```

**Generated Artifacts:**
- **Container Image**: `ghcr.io/whale-net/api-gateway:v2.1.0`
- **Helm Chart**: `api-gateway` (in chart repository)
- **Bazel Targets**:
  - `//api/gateway:api_gateway_helm_chart`
  - `//api/gateway:api_gateway_helm_package`

## Benefits of Domain+App Pattern

1. **Direct Correspondence**: Chart names directly map to image names
2. **Namespace Organization**: Charts are organized by domain (demo-, api-, web-, etc.)
3. **Predictable Targeting**: Bazel targets follow predictable naming
4. **Conflict Avoidance**: Domain prefix prevents naming conflicts between apps
5. **Consistency**: Same pattern used across all artifact types

## Usage in Scripts

The naming pattern enables direct scripting:

```bash
# Build chart for specific domain+app
DOMAIN="demo"
APP="hello_fastapi"
bazel build //demo/hello_fastapi:${DOMAIN}_${APP}_helm_chart

# Install chart using predictable name
helm install my-release everything/${DOMAIN}-${APP}

# Reference matching container image
docker pull ghcr.io/whale-net/${DOMAIN}-${APP}:latest
```

This design makes the relationship between charts and their applications explicit and provides a clean, scalable approach for monorepo artifact management.