# ManManV2 Local Development

ManManV2 is a split-plane game server management platform. This directory provides Tilt configurations for running the entire system locally.

See [ARCHITECTURE.md](./ARCHITECTURE.md) for system design, [TOC.md](./TOC.md) for all documentation.

## Prerequisites

- Docker Desktop with Kubernetes enabled
- Bazel installed
- `grpcurl` for testing (optional but recommended)

```bash
# macOS
brew install grpcurl

# Linux
go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest
```

### One-time host DNS setup

The host manager runs bare-metal and needs to reach the in-cluster MinIO instance via presigned URLs. Add this entry to `/etc/hosts` so the K8s-internal hostname resolves to `localhost` (where Tilt port-forwards MinIO on port 9000):

```bash
echo "127.0.0.1 minio-dev.manmanv2-local-dev.svc.cluster.local" | sudo tee -a /etc/hosts
```

## Quick Start

### 1. Build Images (~2 minutes)

```bash
cd manmanv2
./scripts/build-images.sh
```

Builds: `manmanv2-api`, `manmanv2-processor`, `manmanv2-test-game-server`

### 2. Start Control Plane (~1 minute)

```bash
tilt up
```

Press `space` to open the Tilt UI. Wait for all services to go green:
- ✓ postgres-dev
- ✓ rabbitmq-dev
- ✓ otelcollector-dev
- ✓ manmanv2-api
- ✓ manmanv2-processor

### 3. Run Host Manager (Bare Metal)

In a new terminal:

```bash
export SERVER_ID=host-local-dev-1
export RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
bazel run //manmanv2/host:host
```

Expected output:
```
Host manager starting...
Connected to RabbitMQ
Published status.host.online
Listening for commands...
```

See [host/DEPLOYMENT.md](./host/DEPLOYMENT.md) for detailed host setup.

### 4. Run End-to-End Test

```bash
./scripts/test-flow.sh
```

Creates a game config, starts a session, verifies containers, stops and cleans up.

### 5. Explore

```bash
# View running game containers
docker ps | grep manmanv2

# Check the database
psql postgresql://postgres:password@localhost:5432/manmanv2 -c "\dt"

# Browse RabbitMQ (login: rabbit / password)
open http://localhost:15672

# Query the API
grpcurl -plaintext localhost:50051 list manman.ManManAPI
grpcurl -plaintext localhost:50051 manman.ManManAPI/ListServers
```

### Shutting Down

```bash
tilt down
# Ctrl+C the host manager terminal

# Clean up any leftover game containers
docker stop $(docker ps -q --filter "label=manmanv2.managed=true")
docker rm $(docker ps -aq --filter "label=manmanv2.managed=true")
```

---

## What Gets Started

| Service | Port | Description |
|---------|------|-------------|
| PostgreSQL | 5432 | State storage |
| RabbitMQ | 5672 | Command/status messaging |
| RabbitMQ Mgmt UI | 15672 | Queue management |
| ManManV2 API | 50051 | Control plane gRPC |
| Event Processor | — | DB sync, external events |
| Host Manager (you run) | — | Container orchestrator |

## Configuration

See [ENV.md](./ENV.md) for all environment variables.

Create a `.env` file in this directory (see `.env.example`) to override defaults. Key toggles:

```bash
# Disable a service to use a remote instance instead
ENABLE_MANMANV2_API=false tilt up
ENABLE_MANMANV2_PROCESSOR=false tilt up

# Use external infrastructure instead of Tilt-managed
BUILD_POSTGRES_ENV=custom
POSTGRES_URL=postgresql://user:pass@remote-db:5432/manmanv2
BUILD_RABBITMQ_ENV=custom
RABBITMQ_HOST=remote-rmq.example.com
```

## Development Workflow

1. `tilt up` — starts infrastructure and watches for file changes
2. Edit Go files in `api/`, `processor/`, `host/`, etc.
3. Tilt auto-rebuilds on save
4. Test via `grpcurl` or `./scripts/test-flow.sh`
5. `tilt logs <service>` for targeted log output

### Testing API Changes

```bash
grpcurl -plaintext localhost:50051 list manman.ManManAPI
grpcurl -plaintext localhost:50051 describe manman.ManManAPI.CreateGame
grpcurl -plaintext \
  -d '{"name": "test", "image": "test:latest"}' \
  localhost:50051 manman.ManManAPI/CreateGame
```

### Testing the Event Processor

```bash
tilt logs manmanv2-processor

# Verify database state
psql postgresql://postgres:password@localhost:5432/manmanv2 \
  -c "SELECT * FROM servers;"
```

## Loading Test Data

The `load-minecraft-config.sh` script seeds a complete Minecraft game configuration:

```bash
# Local control plane (default)
./scripts/load-minecraft-config.sh

# Remote control plane
./scripts/load-minecraft-config.sh --grpc-url=remote-api.example.com:50052
```

Creates: game entry, GameConfig with itzg/minecraft-server image, volume strategy, ServerGameConfig with port 25565.

## Troubleshooting

**`error: no Kubernetes cluster selected`**
Enable Kubernetes in Docker Desktop settings, or `kind create cluster` / `minikube start`.

**`bazel build failed` / images not building**
```bash
bazel clean --expunge && tilt down && tilt up
```

**`connection refused to localhost:5432`**
```bash
kubectl get pods -n manmanv2-local-dev | grep postgres
kubectl logs -n manmanv2-local-dev <postgres-pod>
```

**`AMQP connection error`**
Check RabbitMQ is running and the `manmanv2-dev` vhost exists at http://localhost:15672 → Virtual Hosts.

**`Cannot connect to API`**
Wait for all Tilt services to be green before connecting.

## Advanced Topics

### Multiple Hosts

```bash
SERVER_ID=host-1 bazel run //manmanv2/host:host  # terminal 1
SERVER_ID=host-2 bazel run //manmanv2/host:host  # terminal 2
```

### Debug Build

```bash
bazel build //manmanv2/host:host --compilation_mode=dbg
dlv exec ./bazel-bin/manmanv2/host/host_/host -- --server-id=host-debug
```

### Integration Tests

```bash
bazel test //manmanv2/processor:integration_test
```
