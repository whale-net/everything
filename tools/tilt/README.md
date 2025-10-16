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

**Infrastructure Setup:**
- `setup_dev_util()`: Add dev-util Helm repository
- `setup_postgres()`: PostgreSQL database - returns dict with `url`, `host`, `port`, `user`, `password`, `database`, `service_info`
- `setup_rabbitmq()`: RabbitMQ message queue - returns dict with `host`, `port`, `user`, `password`, `service_info`, `mgmt_service_info`
- `setup_otelcollector()`: OpenTelemetry Collector
- `setup_nginx_ingress()`: Nginx Ingress Controller

**Image Building:**
- `build_images_from_apps()`: Build multiple images with Bazel, automatically handles cross-compilation

**Configuration:**
- `build_apps_config()`: Build app configuration with environment variables
- `deploy_helm_chart()`: Deploy Helm chart using helm template + k8s_yaml

**Utilities:**
- `detect_platform()`: Auto-detect platform (linux/arm64 or linux/amd64)
- `get_bazel_platform()`: Map platform to Bazel platform target
- `get_watch_paths()`: Get watch paths for a domain
- `get_env_bool()`: Get boolean environment variable
- `get_custom_or_default()`: Get custom value or default from environment
- `print_startup_banner()`: Print formatted startup banner
- `print_service_info()`: Print service information
- `print_access_info()`: Print access URLs for apps
- `print_footer_info()`: Print useful commands and tips

## Bazel Integration

### How It Works

Tilt uses Bazel to build container images with automatic cross-compilation:

1. **Discovery**: Tilt watches source files for changes
2. **Build**: Runs `bazel run //path:app_image_load --platforms=//tools:linux_arm64`
3. **Tag**: Tags the image with Tilt's expected reference
4. **Deploy**: Tilt updates Kubernetes resources with the new image

### Cross-Compilation

**Critical**: Images must be built for the correct architecture:

- **M1/M2 Macs**: Use `--platforms=//tools:linux_arm64`
- **Intel Macs/PCs**: Use `--platforms=//tools:linux_x86_64`

The `build_images_from_apps()` function handles this automatically using `detect_platform()`.

### Example

```starlark
load('../tools/tilt/common.tilt', 
     'build_images_from_apps', 'detect_platform', 'get_watch_paths')

platform = detect_platform()
watch_paths = get_watch_paths('manman')

# Define apps
APPS = {
    'experience-api': {
        'enabled_env': 'ENABLE_EXPERIENCE_API',
        'bazel_target': '//manman:experience-api_image_load',
        'image_name': 'manman-experience-api',
    },
    'worker-dal-api': {
        'enabled_env': 'ENABLE_WORKER_DAL_API',
        'bazel_target': '//manman:worker-dal-api_image_load',
        'image_name': 'manman-worker-dal-api',
    },
}

# Build all enabled images
build_images_from_apps(APPS, watch_paths, platform)
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

# Setup PostgreSQL - returns dict with connection info
db_info = setup_postgres('my-domain-dev', db_name='myapp')
db_url = db_info['url']  # Full connection string
print("Database at:", db_info['service_info'])

# Setup RabbitMQ - returns dict with connection info
rmq_info = setup_rabbitmq('my-domain-dev')
print("RabbitMQ at:", rmq_info['service_info'])
print("Management UI:", rmq_info['mgmt_service_info'])
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
load('../tools/tilt/common.tilt', 
     'build_images_from_apps', 'detect_platform', 'get_bazel_platform', 'get_watch_paths',
     'setup_dev_util', 'setup_postgres', 'setup_rabbitmq', 'setup_nginx_ingress',
     'build_apps_config', 'deploy_helm_chart', 'get_custom_or_default',
     'print_startup_banner', 'print_access_info', 'print_footer_info')

# ===========================
# Configuration
# ===========================

namespace = 'mydomain-local-dev'
namespace_create(namespace)
dotenv()

platform = detect_platform()
bazel_platform = get_bazel_platform(platform)

print_startup_banner("My Domain", namespace, platform)

# ===========================
# Infrastructure Setup
# ===========================

setup_dev_util(namespace)

# Nginx Ingress Controller (if needed)
setup_nginx_ingress(ingress_class='mydomain-nginx', http_port=30080, https_port=30443)

# PostgreSQL Database
db_info = setup_postgres(namespace, db_name='mydomain')
db_url = get_custom_or_default('BUILD_POSTGRES_ENV', 'POSTGRES_URL', db_info['url'])

# RabbitMQ Message Queue (if needed)
rmq_info = setup_rabbitmq(namespace)
rabbitmq_host = get_custom_or_default('BUILD_RABBITMQ_ENV', 'RABBITMQ_HOST', rmq_info['host'])
rabbitmq_port = get_custom_or_default('BUILD_RABBITMQ_ENV', 'RABBITMQ_PORT', rmq_info['port'])

print("📊 Infrastructure configured:")
print("  Postgres:  {}".format("custom" if os.environ.get('BUILD_POSTGRES_ENV') == 'custom' else "local"))
print("  RabbitMQ:  {}".format("custom" if os.environ.get('BUILD_RABBITMQ_ENV') == 'custom' else "local"))

# ===========================
# Application Configuration
# ===========================

# Get watch paths
watch_paths = get_watch_paths('mydomain')

# Define apps
APPS = {
    'my-api': {
        'enabled_env': 'ENABLE_MY_API',
        'bazel_target': '//mydomain:my-api_image_load',
        'image_name': 'mydomain-my-api',
    },
    'my-worker': {
        'enabled_env': 'ENABLE_MY_WORKER',
        'bazel_target': '//mydomain:my-worker_image_load',
        'image_name': 'mydomain-my-worker',
    },
}

# ===========================
# Bazel Image Building
# ===========================

build_images_from_apps(APPS, watch_paths, platform)

# ===========================
# Helm Deployment
# ===========================

# Build apps configuration with infrastructure settings
apps_config = build_apps_config(
    APPS,
    'mydomain',
    env_vars={
        'POSTGRES_URL': db_url,
        'RABBITMQ_HOST': rabbitmq_host,
        'RABBITMQ_PORT': rabbitmq_port,
    }
)

# Customize app-specific helm config (if needed)
apps_config['my-api']['helm_config']['ingress.tlsEnabled'] = 'false'

# Deploy the helm chart
deploy_helm_chart(
    'mydomain',
    namespace,
    '//mydomain:mydomain_chart',
    'mydomain-host-services',
    apps_config,
    global_config={
        'ingressDefaults.enabled': 'true',
        'ingressDefaults.className': 'mydomain-nginx',
    }
)

# ===========================
# Access Information
# ===========================

print_access_info(
    'mydomain',
    APPS,
    ingress_port=30080,
    additional_services={
        'PostgreSQL': db_info['service_info'],
        'RabbitMQ': rmq_info['service_info'],
        'RabbitMQ Management': rmq_info['mgmt_service_info'],
    }
)

print_footer_info('mydomain')
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
load('../tools/tilt/common.tilt', 'setup_postgres', 'build_images_from_apps')

db_info = setup_postgres(namespace, 'mydb')
build_images_from_apps(APPS, watch_paths, platform)

# ❌ Bad - duplicate infrastructure setup
helm_resource('postgres-dev', 'dev-util/postgres-dev', ...)
custom_build('app1', 'bazel run ...', ...)
custom_build('app2', 'bazel run ...', ...)
```

### 3. Environment-Aware Configuration

Support both local dev and external infrastructure:
```starlark
# Allow using external database
db_info = setup_postgres(namespace, 'mydb')
db_url = get_custom_or_default('BUILD_POSTGRES_ENV', 'POSTGRES_URL', db_info['url'])

# Or manually:
if os.getenv('BUILD_POSTGRES_ENV') == 'custom':
    db_url = os.getenv('POSTGRES_URL')
else:
    db_info = setup_postgres(namespace, 'mydb')
    db_url = db_info['url']
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
   - `postgres` → `setup_postgres()` returns dict with connection info
   - `rabbitmq` → `setup_rabbitmq()` returns dict with connection info

2. **App images**: Convert to Bazel builds
   - `docker-compose build` → Define apps dict and use `build_images_from_apps()`

3. **Service deployment**: Use Helm charts with Tilt
   - `docker-compose up` → `deploy_helm_chart()` with `build_apps_config()`

4. **Configuration**: Use environment variables
   - Use `build_apps_config()` to pass env vars to all apps
   - Support custom infrastructure with `get_custom_or_default()`

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
