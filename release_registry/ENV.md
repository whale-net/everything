# release_registry — Environment Variables

> All environment variables for the release registry service.
> Read this when configuring, deploying, or debugging runtime behavior.

## gRPC Server

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_PORT` | `50054` | gRPC listen port |
| `ENABLE_RELEASE_REGISTRY_API` | `true` | Toggle to skip starting the API service (`false tilt up`) |

## Database

| Variable | Default | Description |
|----------|---------|-------------|
| `PG_DATABASE_URL` | — | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/release_registry` |
| `BUILD_POSTGRES_ENV` | `default` | Set to `'custom'` to use an external database instead of Tilt-managed PostgreSQL |

## Keycloak OIDC (gRPC Auth)

All gRPC endpoints support JWT authentication via Keycloak. Default is `none` (dev mode — no Keycloak needed).

### Server Side (incoming requests — interceptor)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_OIDC_ISSUER` | `""` | Keycloak realm URL, e.g. `https://auth.company.com/realms/myrealm` (required for `oidc`) |
| `GRPC_OIDC_CLIENT_ID` | `""` | Expected audience / client ID in token (required for `oidc`) |

### Client Side (service account — outgoing calls)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_AUTH_TOKEN_URL` | `""` | Keycloak token endpoint URL (required for `oidc`) |
| `GRPC_AUTH_CLIENT_ID` | `""` | Service account client ID |
| `GRPC_AUTH_CLIENT_SECRET` | `""` | Service account client secret |

See [manmanv2/ENV.md](../manmanv2/ENV.md) for the full gRPC auth policy across the platform.

## Tilt Infrastructure Overrides

```bash
# Use external RabbitMQ (for future event publishing)
BUILD_RABBITMQ_ENV=default       # or 'custom'
RABBITMQ_HOST=...                # if BUILD_RABBITMQ_ENV=custom
RABBITMQ_PORT=5672
RABBITMQ_USER=...
RABBITMQ_PASSWORD=...

# S3/Object Storage (TBD — artifact storage metadata)
S3_ENDPOINT=http://minio:9000
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_BUCKET=release-registry-dev
```

---

## Auth Policy Summary

| RPC | Authentication Required |
|-----|------------------------|
| `Resolve` | None (open) |
| `RegisterApp` | Service account (`oidc`) |
| `RegisterCommit` | Gated on `github.event_name == 'push'` context + service account |
| `RegisterArtifact` | Service account (`oidc`) |
| `Promote` | Service account (`oidc`) |
