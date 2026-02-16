# ManManV2 Tilt Setup - Summary

This document summarizes the ManManV2 local development environment setup.

## What Was Created

### 1. Tiltfile (`manman-v2/Tiltfile`)

A new Tiltfile for running ManManV2 **control plane** services in Kubernetes:

**Infrastructure (via common.tilt utilities):**
- PostgreSQL database (port 5432)
- RabbitMQ message queue (ports 5672, 15672)
- OpenTelemetry collector

**ManManV2 Services:**
- `manmanv2-api` - Control plane API (gRPC, port 50051)
- `manmanv2-processor` - Event processor (RabbitMQ consumer)

**Test Images:**
- `manmanv2-test-game-server` - Alpine-based test game server

**Key Features:**
- Uses Bazel to build Go images with platform detection
- Deploys services as Kubernetes Deployments (not Helm for simplicity)
- Port forwards API to localhost:50051
- Environment-based configuration via `.env`
- Shares infrastructure setup with existing manman Tiltfile (via common.tilt)

**Namespace:** `manmanv2-local-dev`

### 2. Documentation

#### `README.md` - Main documentation
Complete guide covering:
- Architecture overview with ASCII diagrams
- Quick start instructions
- Service configuration table
- Development workflow
- Troubleshooting guide
- Advanced topics (multi-host, custom infrastructure, debugging)

#### `README-HOST.md` - Host manager guide
Detailed instructions for running the host manager on bare metal:
- Prerequisites and architecture
- Building wrapper and test images
- 3 options for running the host
- Configuration reference
- Verification steps
- End-to-end testing examples
- Troubleshooting common issues

#### `QUICK-START.md` - 5-minute guide
Streamlined getting-started guide:
- Step-by-step instructions
- Time estimates for each step
- Common issues and fixes
- Quick exploration commands

#### `SETUP-SUMMARY.md` - This file
Overview of what was created and design decisions.

### 3. Configuration

#### `.env.example`
Template configuration file with:
- Service toggles (ENABLE_MANMANV2_API, etc.)
- Infrastructure options (default vs. custom Postgres/RabbitMQ)
- S3/object storage configuration
- Host manager environment variables
- Event processor settings

### 4. Helper Scripts

#### `scripts/build-images.sh`
Automated image builder with:
- Platform auto-detection (arm64/amd64)
- Color-coded output
- Error handling and failure tracking
- Build summary
- Options: `--platform`, `--skip-test-server`, `--help`

#### `scripts/test-flow.sh`
End-to-end test script that:
- Creates game and server game config
- Starts a session
- Verifies containers are running
- Stops session and verifies cleanup
- Options: `--api-endpoint`, `--server-id`, `--cleanup`, `--help`

## Design Decisions

### 1. Kept Existing Manman Tiltfile Untouched

The original `/home/alex/whale_net/everything/manman/Tiltfile` was not modified. It continues to manage ManMan v1 services:
- experience-api (Python)
- worker-dal-api (Python)
- status-api (Python)
- status-processor (Python)
- management-ui (Go)
- migration (Python)

ManManV2 runs in a **separate namespace** to avoid conflicts.

### 2. Host Manager Runs on Bare Metal, Not K8s

The host manager is **excluded** from the Tiltfile because:
- Requires direct Docker socket access (`/var/run/docker.sock`)
- Manages Docker containers on the host machine
- Runs best as a native binary, not containerized
- Simplifies local development (no volume mounts, privileged containers)

This matches the production deployment model where host managers run on bare metal game servers.

### 3. Simple K8s Manifests Instead of Helm

For local development, the Tiltfile uses inline Kubernetes YAML instead of Helm charts:
- **Simpler** to understand and modify
- **Faster** rebuild cycles (no Helm build step)
- **Easier** to debug (plain YAML visible in Tilt UI)
- Helm charts still available for production (`//manman:manman_chart`)

### 4. Test Game Server as Docker Build

The test game server (`manman/wrapper/testdata/`) is built with regular Docker instead of Bazel because:
- It's just a test fixture, not a production service
- Simple Dockerfile with no dependencies
- Faster iteration for wrapper development
- Can be easily modified without Bazel knowledge

### 5. Shared Common.tilt Utilities

Both manman and manman-v2 Tiltfiles use `../tools/tilt/common.tilt`:
- **Consistent** infrastructure setup (postgres, rabbitmq, otel)
- **Reusable** functions (build_images_from_apps, setup_postgres, etc.)
- **Maintainable** - changes to common code benefit all domains
- **Platform-aware** - auto-detects arm64 vs. amd64

## Directory Structure

```
manman-v2/                          # New directory for ManManV2 dev env
├── Tiltfile                        # Control plane services (K8s)
├── README.md                       # Main documentation
├── README-HOST.md                  # Host manager guide
├── QUICK-START.md                  # 5-minute guide
├── SETUP-SUMMARY.md                # This file
├── .env.example                    # Configuration template
└── scripts/
    ├── build-images.sh             # Image builder script
    └── test-flow.sh                # E2E test script

manman/                             # Existing source code (unchanged)
├── Tiltfile                        # ManMan v1 Tiltfile (unchanged)
├── api/                            # ManManV2 control plane API
├── processor/                      # ManManV2 event processor
├── host/                           # ManManV2 host manager (bare metal)
├── wrapper/                        # ManManV2 wrapper sidecar
│   └── testdata/
│       ├── Dockerfile              # Test game server
│       └── test_game_server.sh     # Test script
├── protos/                         # Protobuf definitions
└── BUILD.bazel                     # Bazel build config

tools/tilt/
└── common.tilt                     # Shared Tilt utilities (unchanged)
```

## Comparison: ManMan v1 vs. ManManV2 Tiltfiles

| Aspect | ManMan v1 | ManManV2 |
|--------|-----------|----------|
| **Namespace** | `manman-local-dev` | `manmanv2-local-dev` |
| **Language** | Python | Go |
| **Services** | 6 (APIs, processors, UI) | 2 (API, processor) |
| **Deployment** | Helm chart | Inline K8s YAML |
| **Database** | `manman` | `manmanv2` |
| **RabbitMQ vhost** | `manman-dev` | `manmanv2-dev` |
| **Ingress** | Yes (nginx, port 30080) | No (gRPC only) |
| **Port forwards** | 8080, 9001, 9002 | 50051 |
| **Host component** | Python worker (K8s) | Go binary (bare metal) |

## Running Both Simultaneously

ManMan v1 and ManManV2 can run side-by-side:

```bash
# Terminal 1: ManMan v1
cd manman
tilt up

# Terminal 2: ManManV2
cd manman-v2
tilt up

# Terminal 3: ManManV2 Host Manager
export SERVER_ID=host-local-dev-1
export RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
bazel run //manman/host:host
```

**No conflicts** because:
- Different namespaces
- Different database names
- Different RabbitMQ vhosts
- Different port forwards

**Shared resources:**
- PostgreSQL pod (different databases)
- RabbitMQ pod (different vhosts)
- Docker daemon (for wrapper containers)

## Image Build Targets

All images are built with Bazel:

```bash
# ManManV2 API
bazel run //manman/api:manmanv2-api_image_load --platforms=//tools:linux_amd64

# ManManV2 Processor
bazel run //manman/processor:manmanv2-processor_image_load --platforms=//tools:linux_amd64

# ManManV2 Wrapper (required for host)
bazel run //manman/wrapper:manmanv2-wrapper_image_load --platforms=//tools:linux_amd64

# Test game server (Docker build, not Bazel)
docker build -t manmanv2-test-game-server \
  -f manman/wrapper/testdata/Dockerfile \
  manman/wrapper/testdata/
```

Or use the helper script:
```bash
./scripts/build-images.sh
```

## Environment Variables

### Control Plane (Tiltfile)

| Variable | Default | Description |
|----------|---------|-------------|
| `ENABLE_MANMANV2_API` | `true` | Enable API service |
| `ENABLE_MANMANV2_PROCESSOR` | `true` | Enable processor service |
| `BUILD_TEST_GAME_SERVER` | `true` | Build test game server image |
| `BUILD_POSTGRES_ENV` | `default` | Use Tilt-managed Postgres |
| `BUILD_RABBITMQ_ENV` | `default` | Use Tilt-managed RabbitMQ |
| `S3_ENDPOINT` | `http://minio:9000` | S3 endpoint for logs/backups |

### Host Manager (Bare Metal)

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_ID` | *(required)* | Unique host identifier |
| `RABBITMQ_URL` | `amqp://...` | RabbitMQ connection with vhost |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker daemon socket |
| `WRAPPER_IMAGE` | `manmanv2-wrapper:latest` | Wrapper container image |

## Testing

### Quick Test

```bash
./scripts/test-flow.sh
```

### Manual Test

```bash
# 1. Start control plane
tilt up

# 2. Build images
./scripts/build-images.sh

# 3. Run host manager
export SERVER_ID=host-local-dev-1
export RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
bazel run //manman/host:host

# 4. In another terminal, test API
grpcurl -plaintext localhost:50051 list manman.ManManAPI

# 5. Create and start a session
grpcurl -plaintext \
  -d '{"name": "test", "image": "manmanv2-test-game-server:latest"}' \
  localhost:50051 manman.ManManAPI/CreateGame

# ... follow README-HOST.md for full flow
```

## Troubleshooting

See detailed troubleshooting sections in:
- [README.md](./README.md#troubleshooting)
- [README-HOST.md](./README-HOST.md#troubleshooting)

Common issues:
- Kubernetes not enabled → Enable in Docker Desktop
- Images not found → Run `./scripts/build-images.sh`
- API not reachable → Wait for Tilt to show all services green
- Host can't connect → Check RabbitMQ port forward (5672)

## Next Steps

1. **Review the Tiltfile**: Understand how services are configured
2. **Read the architecture docs**: See `../manman/manman-v2.md`
3. **Run the quick start**: Follow `QUICK-START.md`
4. **Explore the API**: Use `grpcurl` to test endpoints
5. **Run integration tests**: `./scripts/test-flow.sh`
6. **Add your own game**: Replace test-game-server with real game image

## Future Enhancements

Potential improvements:
- [ ] Add Minio for local S3 testing
- [ ] Add migration job to Tiltfile
- [ ] Create Helm chart for local dev (optional)
- [ ] Add log aggregation (Loki/Grafana)
- [ ] Add metrics (Prometheus/Grafana)
- [ ] Create smoke test suite
- [ ] Add pre-commit hooks for image building
- [ ] Document production deployment differences

## Feedback

If you encounter issues or have suggestions, please:
1. Check troubleshooting sections in READMEs
2. Review logs in Tilt UI
3. Check Docker container logs
4. Review RabbitMQ message flow
5. File an issue with details
