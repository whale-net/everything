# Tilt Quick Reference

## Starting Development

```bash
# ManMan
cd manman && tilt up

# Friendly Computing Machine
cd friendly_computing_machine && tilt up

# Stop everything
tilt down
```

## Common Commands

```bash
# View Tilt UI in browser
tilt up

# Run in background
tilt up --hud=false

# Tail logs
tilt logs <resource-name>

# Force rebuild
tilt trigger <resource-name>

# Get resource status
tilt get all
```

## Helper Script

```bash
# List all apps
./tools/scripts/tilt_helper.py list

# Get app info (JSON)
./tools/scripts/tilt_helper.py info manman-experience-api

# Build specific app
./tools/scripts/tilt_helper.py build my-app --platform linux/arm64

# Generate Tiltfile config
./tools/scripts/tilt_helper.py generate --apps app1,app2 -o tilt-config.txt
```

## Environment Variables

### ManMan

```bash
# .env file in manman/
APP_ENV=dev
MANMAN_BUILD_POSTGRES_ENV=default      # or 'custom'
MANMAN_BUILD_RABBITMQ_ENV=default      # or 'custom'
MANMAN_POSTGRES_URL=postgresql://...   # when custom
MANMAN_RABBITMQ_HOST=localhost         # when custom
MANMAN_ENABLE_EXPERIENCE_API=true
MANMAN_ENABLE_WORKER_DAL_API=true
MANMAN_ENABLE_STATUS_API=true
MANMAN_ENABLE_STATUS_PROCESSOR=true
MANMAN_ENABLE_OTEL_LOGGING=true
```

### FCM

```bash
# .env file in friendly_computing_machine/
APP_ENV=dev
FCM_BUILD_POSTGRES_ENV=default
FCM_POSTGRES_URL=postgresql://...
FCM_ENABLE_BOT=true
FCM_ENABLE_API=true
```

## Access URLs

### ManMan
- Experience API: http://localhost:30080/experience/
- Worker DAL API: http://localhost:30080/workerdal/
- Status API: http://localhost:30080/status/
- PostgreSQL: localhost:5432 (postgres/password)
- RabbitMQ: localhost:5672 (rabbit/password)
- RabbitMQ Mgmt: http://localhost:15672

### FCM
- PostgreSQL: localhost:5433 (postgres/password)

## Troubleshooting

### Port Already in Use
```bash
# Find what's using the port
lsof -i :5432

# Kill the process
kill <PID>

# Or change port in Tiltfile
k8s_resource(workload='postgres-dev', port_forwards='5433:5432')
```

### Bazel Build Fails
```bash
# Clean Bazel cache
bazel clean --expunge

# Rebuild
tilt trigger <resource>
```

### Image Load Fails
```bash
# Check platform
uname -m

# M1/M2 Mac → arm64 → use linux/arm64
# Intel → x86_64 → use linux/amd64

# Update Tiltfile platform detection or override:
platform = 'linux/arm64'  # or 'linux/amd64'
```

### Pods Not Starting
```bash
# Check pod status
kubectl get pods -n manman-dev

# Check pod logs
kubectl logs -n manman-dev <pod-name>

# Describe pod
kubectl describe pod -n manman-dev <pod-name>
```

## Useful Kubernetes Commands

```bash
# Get all resources in namespace
kubectl get all -n manman-dev

# Get pod logs
kubectl logs -f -n manman-dev <pod-name>

# Execute command in pod
kubectl exec -it -n manman-dev <pod-name> -- bash

# Port forward manually
kubectl port-forward -n manman-dev svc/my-service 8080:8080

# Delete stuck resources
kubectl delete namespace manman-dev --force --grace-period=0
```

## Database Operations

### ManMan Database

```bash
# Connect to database
psql -h localhost -p 5432 -U postgres -d manman
# Password: password

# Run migrations
cd manman
uv run host run-migration

# Create migration
uv run host create-migration "description"
```

## RabbitMQ Operations

```bash
# Access management UI
open http://localhost:15672
# User: rabbit
# Password: password

# List queues via CLI (if rabbitmqctl available in pod)
kubectl exec -n manman-dev rabbitmq-dev-0 -- rabbitmqctl list_queues
```

## Docker Commands

```bash
# List images built by Tilt
docker images | grep -E 'manman|fcm'

# Remove all Tilt images
docker rmi $(docker images | grep -E 'manman|fcm' | awk '{print $3}')

# View image layers
docker history <image-name>
```

## File Watching

Tilt watches these paths:
- `./src/**/*.py` - Python source files
- `./src/**/*.go` - Go source files
- `BUILD.bazel` - Build file changes
- `pyproject.toml` - Dependency changes
- `go.mod` - Go dependency changes

Tilt ignores:
- `.git/**`
- `**/__pycache__/**`
- `*.pyc`
- `.venv/**`
- `bazel-*/**`

## Performance Tips

```bash
# Disable unused services
MANMAN_ENABLE_WORKER_DAL_API=false tilt up

# Use external infrastructure (faster startup)
MANMAN_BUILD_POSTGRES_ENV=custom \
MANMAN_POSTGRES_URL=postgresql://external-db/manman \
tilt up

# Run Tilt with fewer resources
tilt up --hud=false --stream  # Minimal UI

# Parallel builds (if multiple domains)
cd manman && tilt up &
cd fcm && tilt up &
```

## Common Patterns

### Add New Domain

1. Create `domain/Tiltfile`:
```starlark
load('ext://namespace', 'namespace_create')
load('ext://dotenv', 'dotenv')
load('../tools/tilt/common.tilt', 'setup_postgres', 'bazel_build_image')

namespace = 'domain-dev'
namespace_create(namespace)
dotenv()

db_url = setup_postgres(namespace, 'mydb')
bazel_build_image('my-app', './src', '//domain:app_image_load')
```

2. Create `.env`:
```bash
APP_ENV=dev
DOMAIN_ENABLE_FEATURE=true
```

3. Start:
```bash
cd domain && tilt up
```

### Custom Bazel Build

```starlark
custom_build(
    'my-app',
    'bazel run //path:app_image_load --platforms=//tools:linux_arm64',
    ['./src'],
    deps=['./BUILD.bazel', './pyproject.toml'],
    skips_local_docker=False,
    disable_push=True,
)
```

### Deploy Helm Chart

```starlark
k8s_yaml(
    helm(
        './charts/myapp',
        name='myapp',
        namespace=namespace,
        set=[
            'image.tag=dev',
            'env.key=value',
        ]
    )
)
```

## Resources

- **Tilt Docs**: https://docs.tilt.dev
- **Bazel Docs**: https://bazel.build/docs
- **Everything Docs**: See `/docs/TILT_INTEGRATION.md`
- **Common Utils**: See `/tools/tilt/README.md`
