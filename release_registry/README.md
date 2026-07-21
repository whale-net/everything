# Release Registry — Local Development

Release registry service for tracking application metadata, git commits, container artifacts, and environment promotions. See [ARCHITECTURE.md](./ARCHITECTURE.md) for system design, [TOC.md](./TOC.md) for all documentation.

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

## Quick Start

### 1. Build the Service (~2 minutes)

```bash
cd release_registry
bazel build //release_registry/api:registry_api
```

### 2. Run Locally with Tilt

```bash
tilt up
```

Press `space` to open the Tilt UI. Wait for all services to go green:
- `postgresql-dev`
- `release_registry-api`

### 3. Test via grpcurl

```bash
# List available RPCs
grpcurl -plaintext localhost:50054 list release_registry.v1.RegistryService

# Register an app
grpcurl -plaintext \
  -d '{"metadata":{"domain":"games","name":"manmanv2"}}' \
  localhost:50054 release_registry.v1.RegistryService/RegisterApp

# Resolve active artifact for dev environment
grpcurl -plaintext \
  -d '{"app":"games-manmanv2","env":"dev"}' \
  localhost:50054 release_registry.v1.RegistryService/Resolve
```

### Shutting Down

```bash
tilt down
```

---

## What Gets Started

| Service | Port | Description |
|---------|------|-------------|
| PostgreSQL | 5432 | State storage (Tilt-managed) |
| Registry API | 50054 | gRPC service endpoint |

## Configuration

See [ENV.md](./ENV.md) for all environment variables.

Create a `.env` file in this directory to override defaults. Key toggles:

```bash
# Disable the registry service (use remote instance instead)
ENABLE_RELEASE_REGISTRY_API=false tilt up

# Use external database
BUILD_POSTGRES_ENV=custom
POSTGRES_URL=postgresql://user:pass@remote-db:5432/release_registry
```

## Development Workflow

1. `tilt up` — starts infrastructure and watches for file changes
2. Edit Go files in `api/`, modify `.proto` files in `protos/`
3. Tilt auto-rebuilds on save (Bazel-driven)
4. Test via `grpcurl` or the CLI wrapper (TBD)
5. `tilt logs release_registry-api` for targeted log output

### Testing Proto Changes

After editing `.proto` files, regenerate Go code:

```bash
bazel build //release_registry/protos:registry_go_proto
```

Then verify with grpcurl:

```bash
grpcurl -plaintext localhost:50054 describe release_registry.v1.RegistryService/RegisterCommit
```

## Troubleshooting

**`connection refused to localhost:50054`**
Wait for the API pod to start: `kubectl get pods -n infra | grep registry`

**`proto file not found`**
Regenerate after proto changes: `bazel build //release_registry/...`

**`database connection failed`**
Ensure PostgreSQL is running and `PG_DATABASE_URL` points to a valid instance.
