# ManManV2 Local Development

Complete local development environment for ManManV2, the split-plane game server management system.

## Overview

ManManV2 uses a **split-plane architecture**:

- **Control Plane** (Cloud/K8s): API, database, event processor, message queue
- **Execution Plane** (Bare Metal): Host managers that orchestrate Docker containers

This directory provides Tilt configurations for running the entire system locally.

## Quick Start

### 1. Start Control Plane (K8s via Tilt)

```bash
cd manman-v2
tilt up
```

This starts:
- PostgreSQL (localhost:5432)
- RabbitMQ (localhost:5672, management UI at :15672)
- ManManV2 API (gRPC on localhost:50051)
- Event Processor (RabbitMQ consumer → PostgreSQL sync)

### 2. Build Required Images

```bash
# Build test game server (optional, for testing)
docker build -t manmanv2-test-game-server \
  -f ../manman/testdata/Dockerfile \
  ../manman/testdata/
```

### 3. Run Host Manager (Bare Metal)

```bash
# Option A: Using bazel run
bazel run //manman/host:host -- \
  --server-id=host-local-dev-1 \
  --rabbitmq-url=amqp://rabbit:password@localhost:5672/manmanv2-dev

# Option B: Using environment variables
export SERVER_ID=host-local-dev-1
export RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
bazel run //manman/host:host
```

See [README-HOST.md](./README-HOST.md) for detailed host setup instructions.

## Architecture Diagram

```
┌────────────────────────────────────────────────────────┐
│         CONTROL PLANE (K8s - Tilt)                     │
│                                                         │
│  ┌──────────────┐      ┌─────────────────┐            │
│  │  API (gRPC)  │      │ Event Processor │            │
│  │  :50051      │      │ (Worker)        │            │
│  └──────┬───────┘      └────────┬────────┘            │
│         │                       │                      │
│    ┌────▼──────────┐   ┌───────▼────────┐            │
│    │  PostgreSQL   │   │   RabbitMQ     │            │
│    │  :5432        │   │   :5672, :15672│            │
│    └───────────────┘   └───────┬────────┘            │
└────────────────────────────────┼─────────────────────┘
                                 │
                    RabbitMQ (commands, status, health)
                                 │
┌────────────────────────────────▼─────────────────────┐
│     EXECUTION PLANE (Bare Metal - Docker)            │
│                                                       │
│  ┌─────────────────────────────────────────┐         │
│  │  Host Manager (You Run This)            │         │
│  │  - RabbitMQ consumer                    │         │
│  │  - Docker SDK for containers            │         │
│  │  - Session lifecycle orchestration      │         │
│  │  - Stdin forwarding via attach          │         │
│  │  - Orphan recovery                      │         │
│  └────┬──────────┬──────────┬──────────────┘         │
│       │ attach   │ attach   │ attach                 │
│  ┌────▼─────┐ ┌─▼──────┐ ┌─▼──────────┐             │
│  │  Game    │ │ Game   │ │   Game     │             │
│  │Container │ │Container│ │ Container  │             │
│  └──────────┘ └────────┘ └────────────┘             │
└───────────────────────────────────────────────────────┘
```

## What Gets Started

### Control Plane (via Tilt)

| Service | Type | Port | Description |
|---------|------|------|-------------|
| PostgreSQL | Database | 5432 | State storage |
| RabbitMQ | Message Queue | 5672 | Command/status messaging |
| RabbitMQ Mgmt | Web UI | 15672 | Queue management |
| ManManV2 API | gRPC Server | 50051 | Control plane API |
| Event Processor | Worker | - | DB sync, external events |

### Execution Plane (You Run)

| Component | Type | Port | Description |
|-----------|------|------|-------------|
| Host Manager | Binary | - | Container orchestrator |
| Game Server(s) | Container | varies | Actual game servers |

## Configuration

### Environment Variables

Create a `.env` file in this directory (see `.env.example`):

```bash
# Control Plane
ENABLE_MANMANV2_API=true
ENABLE_MANMANV2_PROCESSOR=true

# Build Options
BUILD_TEST_GAME_SERVER=true

# Infrastructure (optional - defaults to Tilt-managed)
BUILD_POSTGRES_ENV=default   # or 'custom'
BUILD_RABBITMQ_ENV=default   # or 'custom'
# POSTGRES_URL=postgresql://...  # if custom
# RABBITMQ_HOST=...              # if custom

# S3/Object Storage (for logs and backups)
S3_ENDPOINT=http://minio:9000
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_BUCKET=manmanv2-dev

# Host Manager (for bare metal execution)
SERVER_ID=host-local-dev-1
RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
DOCKER_SOCKET=/var/run/docker.sock
```

### Service Toggle

Disable services by setting environment variables:

```bash
# Disable API (use remote instead)
ENABLE_MANMANV2_API=false tilt up

# Disable processor (use remote instead)
ENABLE_MANMANV2_PROCESSOR=false tilt up
```

## Development Workflow

### Typical Flow

1. **Start infrastructure**: `tilt up` in `manman-v2/`
2. **Make code changes**: Edit Go files in `manman/api/`, `manman/processor/`, etc.
3. **Auto-rebuild**: Tilt watches files and rebuilds automatically
4. **Test via API**: Use `grpcurl` to test endpoints
5. **Run host locally**: Start host manager to test full flow
6. **View logs**: Use Tilt UI or `tilt logs <service>`

### Testing API Changes

```bash
# List all API methods
grpcurl -plaintext localhost:50051 list manman.ManManAPI

# Describe a method
grpcurl -plaintext localhost:50051 describe manman.ManManAPI.CreateGame

# Call a method
grpcurl -plaintext \
  -d '{"name": "test", "image": "test:latest"}' \
  localhost:50051 \
  manman.ManManAPI/CreateGame
```

### Testing Event Processor

```bash
# Check processor logs in Tilt UI
tilt logs manmanv2-processor

# Publish a test message to RabbitMQ
# (requires rabbitmq management CLI or Python/Go script)

# Verify database was updated
psql postgresql://postgres:password@localhost:5432/manmanv2 \
  -c "SELECT * FROM servers;"
```

### Testing Host Manager

See [README-HOST.md](./README-HOST.md) for detailed testing instructions.

## Directory Structure

```
manman-v2/
├── Tiltfile              # Control plane services (K8s)
├── README.md             # This file
├── README-HOST.md        # Host manager setup guide
├── .env.example          # Example configuration
└── scripts/
    ├── build-images.sh   # Build all required images
    └── test-flow.sh      # End-to-end test script

../manman/                # Source code (parent directory)
├── api/                  # Control plane API (gRPC)
├── processor/            # Event processor
├── host/                 # Host manager
├── testdata/             # Integration test fixtures
├── protos/               # Protobuf definitions
└── BUILD.bazel           # Bazel build config
```

## Troubleshooting

### Tilt won't start

**Problem**: `error: no Kubernetes cluster selected`

**Solution**: Ensure you have a local Kubernetes cluster running:
```bash
# For Docker Desktop
# Enable Kubernetes in Docker Desktop settings

# For kind
kind create cluster

# For minikube
minikube start
```

### Images not building

**Problem**: `bazel build failed`

**Solution**: Clear Bazel cache and rebuild:
```bash
bazel clean --expunge
tilt down
tilt up
```

### Database connection errors

**Problem**: `connection refused to localhost:5432`

**Solution**: Check PostgreSQL pod is running:
```bash
kubectl get pods -n manmanv2-local-dev | grep postgres
kubectl logs -n manmanv2-local-dev postgres-dev-...
```

### RabbitMQ connection errors

**Problem**: `AMQP connection error`

**Solution**:
1. Check RabbitMQ is running: `kubectl get pods -n manmanv2-local-dev | grep rabbitmq`
2. Verify vhost exists: http://localhost:15672 → Virtual Hosts
3. Create vhost if needed: Admin → Virtual Hosts → Add "manmanv2-dev"

## Advanced Topics

### Running Multiple Hosts

Test multi-host scenarios by running multiple host managers:

```bash
# Terminal 1
SERVER_ID=host-1 bazel run //manman/host:host

# Terminal 2
SERVER_ID=host-2 bazel run //manman/host:host

# Terminal 3
SERVER_ID=host-3 bazel run //manman/host:host
```

### Custom PostgreSQL/RabbitMQ

Use external infrastructure instead of Tilt-managed:

```bash
# In .env or export
BUILD_POSTGRES_ENV=custom
POSTGRES_URL=postgresql://user:pass@remote-db.example.com:5432/manmanv2

BUILD_RABBITMQ_ENV=custom
RABBITMQ_HOST=remote-rmq.example.com
RABBITMQ_PORT=5672
RABBITMQ_USER=myuser
RABBITMQ_PASSWORD=mypassword
```

### Debugging with Delve

```bash
# Build binary with debug symbols
bazel build //manman/host:host --compilation_mode=dbg

# Run with Delve
dlv exec ./bazel-bin/manman/host/host_/host -- \
  --server-id=host-debug
```

### Integration Tests

```bash
# Run processor integration tests
bazel test //manman/processor:integration_test
```

## Related Documentation

- [ManManV2 Architecture](../manman/manman-v2.md)
- [Phase 6 Completion Report](../manman/PHASE_6_COMPLETE.md)
- [Control Plane API](../manman/protos/api.proto)
- [Event Processor Design](../manman/PHASE_6_STATUS.md)

## Getting Help

- **Architecture Questions**: See `../manman/manman-v2.md`
- **API Reference**: Use `grpcurl -plaintext localhost:50051 describe`
- **Build Issues**: Check Bazel logs with `bazel build --verbose_failures`
- **Runtime Issues**: Check Tilt logs in UI or `tilt logs <service>`
