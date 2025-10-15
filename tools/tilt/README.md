# Tilt Integration for Everything Monorepo

This directory contains Tilt configuration and utilities for local development.

## Overview

The Everything monorepo uses a **domain-centric Tilt architecture**:

- **Root Tiltfile**: Minimal, provides documentation only
- **Domain Tiltfiles**: Each domain (e.g., manman, fcm) has its own complete Tiltfile
- **Common utilities**: Shared Starlark functions in `tools/tilt/common.tilt`

## Quick Start

### Running a Domain

Navigate to the domain directory and start Tilt:

```bash
# ManMan development
cd manman
tilt up

# Friendly Computing Machine development
cd friendly_computing_machine
tilt up
```

### Prerequisites

1. **Kubernetes cluster**: Docker Desktop, Minikube, or k3d
2. **Tilt**: Install from https://tilt.dev
3. **Bazel**: Install from https://bazel.build
4. **kubectl**: Kubernetes CLI

## Architecture

### Domain-Specific Tiltfiles

Each domain manages its own complete development environment:

**Responsibilities:**
- Infrastructure dependencies (postgres, rabbitmq, etc.)
- Bazel-based image builds with cross-compilation
- Helm chart deployments
- Port forwarding and access
- Domain-specific configuration

**Benefits:**
- Self-contained: Each domain can be developed independently
- No cross-domain interference
- Easy onboarding: Just `cd domain && tilt up`
- Clear ownership: Domain teams control their Tilt config

### Common Utilities (`tools/tilt/common.tilt`)

Shared Starlark functions that domains can import:

- `bazel_build_image()`: Build images using Bazel with cross-compilation
- `setup_postgres()`: PostgreSQL from dev-util
- `setup_rabbitmq()`: RabbitMQ from dev-util
- `setup_otelcollector()`: OpenTelemetry Collector
- `setup_nginx_ingress()`: Nginx Ingress Controller
- Configuration helpers and output formatting

## Bazel Integration

### How It Works

Tilt uses Bazel to build container images:

1. **Discovery**: Tilt watches source files for changes
2. **Build**: Runs `bazel run //path:app_image_load --platforms=//tools:linux_arm64`
3. **Load**: Bazel loads the image into Docker
4. **Deploy**: Tilt deploys to Kubernetes

### Cross-Compilation

**Critical**: Images must be built for the correct architecture:

- **M1/M2 Macs**: Use `--platforms=//tools:linux_arm64`
- **Intel Macs/PCs**: Use `--platforms=//tools:linux_x86_64`

The `bazel_build_image()` function handles this automatically.

### Example

```starlark
load('../tools/tilt/common.tilt', 'bazel_build_image', 'detect_platform')

platform = detect_platform()

bazel_build_image(
    'my-api',                           # Image name
    './src',                            # Watch path
    '//my-domain:my_api_image_load',   # Bazel target
    platform=platform
)
```

## Infrastructure

### dev-util Helm Charts

We use the [`whale-net/dev-util`](https://github.com/whale-net/dev-util) Helm repository for shared services:

- **postgres-dev**: PostgreSQL database
- **rabbitmq-dev**: RabbitMQ message queue
- **otelcollector-dev**: OpenTelemetry Collector

### Example Usage

```starlark
load('../tools/tilt/common.tilt', 'setup_postgres', 'setup_rabbitmq')

# Setup PostgreSQL
db_url = setup_postgres('my-domain-dev', db_name='myapp')

# Setup RabbitMQ
rmq = setup_rabbitmq('my-domain-dev')
print("RabbitMQ at:", rmq['host'])
```

## Helper Scripts

### `tools/scripts/tilt_helper.py`

Python script for Bazel integration:

```bash
# List all apps with release metadata
./tools/scripts/tilt_helper.py list

# Get info about an app
./tools/scripts/tilt_helper.py info manman-experience-api

# Build an app for Tilt
./tools/scripts/tilt_helper.py build manman-experience-api --platform linux/arm64

# Generate Tiltfile config
./tools/scripts/tilt_helper.py generate --apps manman-experience-api,manman-worker-dal
```

## Domain Tiltfile Template

Here's a template for creating a new domain Tiltfile:

```starlark
# My Domain Tiltfile
load('ext://namespace', 'namespace_create')
load('ext://dotenv', 'dotenv')
load('../tools/tilt/common.tilt', 'bazel_build_image', 'detect_platform', 
     'setup_postgres', 'setup_rabbitmq', 'setup_dev_util', 'print_startup_banner')

# Configuration
namespace = 'mydomain-dev'
namespace_create(namespace)
dotenv()

platform = detect_platform()
print_startup_banner("My Domain", namespace, platform)

# Setup dev-util repository
setup_dev_util(namespace)

# Infrastructure
db_url = setup_postgres(namespace, db_name='mydomain')
rmq = setup_rabbitmq(namespace)

# Build images
bazel_build_image(
    'mydomain-api',
    './src',
    '//mydomain:api_image_load',
    platform=platform
)

# Deploy Helm chart
k8s_yaml(
    helm(
        './charts/mydomain',
        name='mydomain',
        namespace=namespace,
        set=[
            'image.name=mydomain-api',
            'image.tag=dev',
            'env.db.url={}'.format(db_url),
        ]
    )
)

print("\n🚀 My Domain ready at http://localhost:30080")
```

## Environment Variables

### Common Variables

- `APP_ENV`: Application environment (default: `dev`)
- `TILT_ENABLE_<DOMAIN>`: Enable/disable domain (default: `true`)

### Domain-Specific Variables

Each domain can define its own environment variables. Example for ManMan:

- `MANMAN_BUILD_POSTGRES_ENV`: Set to `custom` to use external postgres
- `MANMAN_POSTGRES_URL`: Custom postgres URL
- `MANMAN_BUILD_RABBITMQ_ENV`: Set to `custom` to use external rabbitmq
- `MANMAN_RABBITMQ_HOST`: Custom rabbitmq host
- `MANMAN_ENABLE_EXPERIENCE_API`: Enable/disable Experience API

### Using .env Files

Create a `.env` file in your domain directory:

```bash
# manman/.env
APP_ENV=dev
MANMAN_BUILD_POSTGRES_ENV=default
MANMAN_ENABLE_EXPERIENCE_API=true
MANMAN_ENABLE_WORKER_DAL_API=true
MANMAN_ENABLE_STATUS_API=true
```

Tilt automatically loads `.env` files with the `dotenv()` extension.

## Troubleshooting

### Platform Issues

**Problem**: Image fails with "Exec format error"

**Solution**: Ensure you're building for the correct platform:

```bash
# Check your architecture
uname -m

# M1/M2 Mac: arm64 → use linux/arm64
# Intel Mac: x86_64 → use linux/amd64
```

### Bazel Build Failures

**Problem**: `bazel run` fails with target not found

**Solution**: Ensure your app has a `release_app()` macro in `BUILD.bazel`:

```starlark
load("//tools:release.bzl", "release_app")

release_app(
    name = "my_app",
    binary_target = ":my_app",
    language = "python",
    domain = "mydomain",
)
```

### Port Conflicts

**Problem**: Port already in use

**Solution**: 
1. Check what's using the port: `lsof -i :5432`
2. Change port forwarding in Tiltfile
3. Or stop conflicting service

### Helm Chart Not Found

**Problem**: Helm chart path not found

**Solution**: Ensure the chart path is relative to the Tiltfile:

```starlark
# If Tiltfile is in domain/, chart in domain/charts/app:
helm('./charts/app', ...)

# Not:
helm('charts/app', ...)  # Wrong - relative to workspace root
```

## Best Practices

### 1. Keep Domains Self-Contained

Each domain Tiltfile should:
- ✅ Include all dependencies it needs
- ✅ Use its own namespace
- ✅ Have its own port forwarding
- ❌ Not depend on other domain Tiltfiles
- ❌ Not share infrastructure with other domains

### 2. Use Common Utilities

Instead of duplicating code:
```starlark
# ✅ Good - use common utilities
load('../tools/tilt/common.tilt', 'setup_postgres')
db_url = setup_postgres(namespace, 'mydb')

# ❌ Bad - duplicate infrastructure setup
helm_resource('postgres-dev', 'dev-util/postgres-dev', ...)
```

### 3. Environment-Aware Configuration

Support both local dev and external infrastructure:
```starlark
# Allow using external database
if os.getenv('BUILD_POSTGRES_ENV') == 'custom':
    db_url = os.getenv('POSTGRES_URL')
else:
    db_url = setup_postgres(namespace, 'mydb')
```

### 4. Clear Output

Help developers understand what's running:
```starlark
print("\n📡 Access URLs:")
print("  API:      http://localhost:30080/api")
print("  Postgres: localhost:5432")
print("\n💡 Run 'tilt down' to stop all services")
```

## Migration from Docker Compose

If you have a `docker-compose.yml`:

1. **Infrastructure**: Move to dev-util helm charts
   - `postgres` → `setup_postgres()`
   - `rabbitmq` → `setup_rabbitmq()`

2. **App images**: Convert to Bazel builds
   - `docker-compose build` → `bazel_build_image()`

3. **Service deployment**: Use Helm charts
   - `docker-compose up` → `helm()` + `k8s_yaml()`

## Resources

- **Tilt Documentation**: https://docs.tilt.dev
- **Bazel Documentation**: https://bazel.build/docs
- **dev-util Charts**: https://github.com/whale-net/dev-util
- **Everything AGENTS.md**: See repository root for Bazel/release system details

## Contributing

When adding a new domain:

1. Create `domain/Tiltfile`
2. Add infrastructure dependencies using common utilities
3. Set up Bazel image builds with `bazel_build_image()`
4. Deploy with Helm charts
5. Document domain-specific env vars
6. Test with `tilt up` and `tilt down`

When updating common utilities:

1. Edit `tools/tilt/common.tilt`
2. Test with at least one domain
3. Update this README
4. Consider backward compatibility
