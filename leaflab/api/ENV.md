# LeafLab API — Environment Variables

Configuration for the leaflab-api service. All variables are optional unless marked required.

## Service Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PORT` | No | `50051` | gRPC listening port |
| `PG_DATABASE_URL` | Yes | — | PostgreSQL connection string (must include schema search_path, user, password, host, port, and dbname) |
| `RABBITMQ_URL` | No | `amqp://guest:guest@localhost:5672/` | RabbitMQ connection URL |

## Authentication

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LEAFLAB_API_AUTH_MODE` | No | `none` | Authentication mode: `none` (development only) or `oidc` |
| `LEAFLAB_API_OIDC_ISSUER` | If OIDC | — | OIDC issuer URL (required when `LEAFLAB_API_AUTH_MODE=oidc`) |
| `LEAFLAB_API_OIDC_CLIENT_ID` | If OIDC | — | OIDC client ID (required when `LEAFLAB_API_AUTH_MODE=oidc`) |

## Phase 1 Access Control

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LEAFLAB_PHASE1_GATE_OPEN` | No | `false` | Feature gate for Phase 1 access (A30: non-exposed to production). See #1187 for enforcement; removable in Phase 2 when FR5 scoping lands. **Must remain `false` in production deployments.** |

## Observability

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LEAFLAB_API_REFLECTION_ENABLED` | No | `false` | Enable gRPC reflection (development only) |

## Development

When running locally, the following suffices:
```bash
export PORT=50051
export PG_DATABASE_URL="postgresql://postgres:password@localhost:5432/leaflab?sslmode=disable"
export RABBITMQ_URL="amqp://guest:guest@localhost:5672/"
export LEAFLAB_API_AUTH_MODE=none
```
