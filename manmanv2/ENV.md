# ManManV2 — Environment Variables

> All environment variables for the ManManV2 platform.
> Read this when configuring, deploying, or debugging runtime behavior.

## Component References

Each component documents its own env vars:
- [ui/ENV.md](ui/ENV.md) — UI service
- [api/S3_CONFIG.md](api/S3_CONFIG.md) — S3/object storage

## Local Development (`.env` / Tilt)

```bash
# Control Plane — enable/disable services (default: true)
ENABLE_MANMANV2_API=true
ENABLE_MANMANV2_PROCESSOR=true

# Build Options
BUILD_TEST_GAME_SERVER=true

# Infrastructure — set to 'custom' to use external instances
BUILD_POSTGRES_ENV=default       # or 'custom'
BUILD_RABBITMQ_ENV=default       # or 'custom'
POSTGRES_URL=postgresql://...    # if BUILD_POSTGRES_ENV=custom
RABBITMQ_HOST=...                # if BUILD_RABBITMQ_ENV=custom
RABBITMQ_PORT=5672
RABBITMQ_USER=...
RABBITMQ_PASSWORD=...

# S3/Object Storage (logs and backups)
S3_ENDPOINT=http://minio:9000
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_BUCKET=manmanv2-dev
```

## Host Manager

```bash
SERVER_ID=host-local-dev-1
RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
DOCKER_SOCKET=/var/run/docker.sock
```

## Platform-Wide Variables

<!-- TODO: Document env vars shared across multiple ManManV2 control plane components -->
