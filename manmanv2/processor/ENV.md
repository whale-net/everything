# Processor Environment Variables

## Required

| Variable | Description |
|----------|-------------|
| `RABBITMQ_URL` | RabbitMQ connection URL (e.g. `amqp://guest:guest@localhost:5672/`) |
| `DB_PASSWORD` | PostgreSQL database password |

## Database

| Variable | Default | Description |
|----------|---------|-------------|
| `PG_DATABASE_URL` | — | Full PostgreSQL URL. If set, overrides all `DB_*` variables |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `postgres` | PostgreSQL user |
| `DB_NAME` | `manman` | Database name |
| `DB_SSL_MODE` | `disable` | SSL mode (`disable`, `require`, `verify-full`) |

## RabbitMQ / Messaging

| Variable | Default | Description |
|----------|---------|-------------|
| `QUEUE_NAME` | `processor-events` | Queue to consume messages from |
| `EXTERNAL_EXCHANGE` | `external` | Exchange name for outbound external events |

## Stale Detection

| Variable | Default | Description |
|----------|---------|-------------|
| `STALE_HOST_THRESHOLD_SECONDS` | `90` | Seconds before a host is considered stale and marked offline |
| `STALE_SESSION_THRESHOLD_SECONDS` | `30` | Seconds before a session is considered stale |

## S3 (Backup Scheduler)

| Variable | Default | Description |
|----------|---------|-------------|
| `S3_BUCKET` | — | S3 bucket for backup storage |
| `S3_REGION` | — | S3 region |
| `S3_ENDPOINT` | — | Optional custom S3-compatible endpoint (MinIO, OVH, etc.) |
| `S3_PUBLIC_ENDPOINT` | — | Optional public endpoint for pre-signed URLs |
| `S3_ACCESS_KEY` | — | Static credentials access key (for MinIO etc.) |
| `S3_SECRET_KEY` | — | Static credentials secret key |

## Control API gRPC (Restart Scheduler)

These variables configure the gRPC connection to the control-api used by the restart scheduler to stop and start sessions.

| Variable | Default | Description |
|----------|---------|-------------|
| `API_ADDRESS` | `localhost:50051` | gRPC address of the control-api (host:port) |
| `GRPC_AUTH_MODE` | `none` | Auth mode: `none` or `service_account` |
| `GRPC_AUTH_TOKEN_URL` | — | OIDC token endpoint URL (required when `GRPC_AUTH_MODE=service_account`) |
| `GRPC_AUTH_CLIENT_ID` | — | OIDC client ID for service account auth |
| `GRPC_AUTH_CLIENT_SECRET` | — | OIDC client secret for service account auth |
| `RESTART_STOP_TIMEOUT_SECONDS` | `120` | Seconds to wait for a session to stop before the restart job fails and retries |

## Observability

| Variable | Default | Description |
|----------|---------|-------------|
| `LOG_LEVEL` | `info` | Log level (`debug`, `info`, `warn`, `error`) |
| `HEALTH_CHECK_PORT` | `8080` | Port for the HTTP health check endpoint |
