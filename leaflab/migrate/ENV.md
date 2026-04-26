# leaflab-migrate — Environment Variables

> Read this when running or debugging database migrations for LeafLab.

## Database

All variables are read by `libs/go/migrate.DefaultConfig()`.

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `DB_HOST` | `localhost` | No | PostgreSQL host |
| `DB_PORT` | `5432` | No | PostgreSQL port |
| `DB_USER` | `postgres` | No | PostgreSQL user |
| `DB_PASSWORD` | — | Yes | PostgreSQL password |
| `DB_NAME` | `postgres` | No | Target database name (use `leaflab`) |
| `DB_SSL_MODE` | `disable` | No | SSL mode (`disable`, `require`, `verify-full`) |

## Usage

```bash
# Run all pending migrations (default)
bazel run //leaflab/migrate:leaflab-migrate

# Rollback all
bazel run //leaflab/migrate:leaflab-migrate -- --down

# Run N steps
bazel run //leaflab/migrate:leaflab-migrate -- --steps=1

# Show history
bazel run //leaflab/migrate:leaflab-migrate -- --history
```

## Local Development (Tilt)

The migration runs as a K8s Job on `tilt up`. It re-runs automatically when the image changes. No manual invocation is needed during normal dev.

```bash
DB_HOST=postgres-dev.leaflab-local-dev.svc.cluster.local
DB_PASSWORD=password
DB_NAME=leaflab
```
