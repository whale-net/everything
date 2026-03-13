# ManManV2 — Environment Variables

> All environment variables for the ManManV2 platform.
> Read this when configuring, deploying, or debugging runtime behavior.

## Component References

Each component documents its own env vars:
- [ui/ENV.md](ui/ENV.md) — UI service
- [api/S3_CONFIG.md](api/S3_CONFIG.md) — S3/object storage

## gRPC Authentication

All gRPC endpoints (API :50051, log-processor :50053) support JWT authentication via Keycloak. Default is `none` (dev mode — no Keycloak needed).

### API & Log-Processor (server side — incoming requests)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_OIDC_ISSUER` | `""` | Keycloak realm URL, e.g. `https://auth.company.com/realms/myrealm` |
| `GRPC_OIDC_CLIENT_ID` | `""` | Expected audience / client ID in token |

### Log-Processor & Host (client side — outgoing calls to API)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_AUTH_TOKEN_URL` | `""` | Keycloak token endpoint, e.g. `https://auth.company.com/realms/myrealm/protocol/openid-connect/token` |
| `GRPC_AUTH_CLIENT_ID` | `""` | Service account client ID |
| `GRPC_AUTH_CLIENT_SECRET` | `""` | Service account client secret |

### UI (client side — forwards logged-in user's token per request)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |

See [ui/ENV.md](ui/ENV.md) for full UI configuration.

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

# gRPC auth (service account, for calling the API)
GRPC_AUTH_MODE=none                     # or 'oidc'
GRPC_AUTH_TOKEN_URL=                    # Keycloak token endpoint
GRPC_AUTH_CLIENT_ID=                    # service account client ID
GRPC_AUTH_CLIENT_SECRET=               # service account client secret
```

## Platform-Wide Variables

`GRPC_AUTH_MODE` appears on every component. Set it consistently across the platform — mismatched modes will cause `codes.Unauthenticated` errors.
