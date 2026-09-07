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

## Database

All services that need PostgreSQL use `PG_DATABASE_URL`. The individual `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSL_MODE` variables are no longer used.

| Variable | Description |
|----------|-------------|
| `PG_DATABASE_URL` | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/dbname` |

Services that read this variable: **API**, **log-processor** (archival), **UI** (DB-backed sessions).

---

## Control API — RestartDeployment

| Variable | Default | Description |
|----------|---------|-------------|
| `RESTART_STALL_TIMEOUT` | `45s` | How long a `RestartDeployment`-recorded `pending_restarts` row (see `ARCHITECTURE.md` "Pending Restarts") may sit `'pending'` before `PendingRestartReaper` (#1732) expires it. ~3x the ~15s window the UI's now-removed client-side poll bound (deleted by #1733) used to give the dispatched Stop real container-stop time, without letting a record sit indefinitely. Accepts Go duration syntax (e.g. `90s`, `2m`). |
| `RESTART_REAPER_INTERVAL` | `10s` | How often `PendingRestartReaper` (#1732) ticks to expire stalled `pending_restarts` rows. Combined with `RESTART_STALL_TIMEOUT`, worst-case stall-detection latency is `RESTART_STALL_TIMEOUT + RESTART_REAPER_INTERVAL` (~55s at the defaults) — keep this well below `RESTART_STALL_TIMEOUT` or the bound is meaningless. Accepts Go duration syntax (e.g. `5s`, `30s`). |

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

## Event Processor

```bash
EXTERNAL_EXCHANGE=external            # existing external-facing RabbitMQ exchange
LIVE_EXCHANGE=manmanv2.htmxsse        # dedicated exchange for live status→UI triggers (see manmanv2/events, ARCHITECTURE.md § Event Processor → UI (Live Status))
```

`LIVE_EXCHANGE` defaults to `manmanv2/events.ExchangeName` (`manmanv2.htmxsse`) and only needs to be set explicitly to point at a non-default exchange. `manmanv2/ui` must be configured to consume from the same exchange name.

## Platform-Wide Variables

`GRPC_AUTH_MODE` appears on every component. Set it consistently across the platform — mismatched modes will cause `codes.Unauthenticated` errors.
