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
| `MIGRATE_AUTO_DOWN` | `false` | No | Allow the default run to auto-migrate DOWN when the DB is ahead of this image's migrations (e.g. after a rollback), instead of failing loudly. See `libs/go/migrate/README.md` "Rollback detection" — a standing switch, not scoped to one rollback. |
| `MIGRATE_BYPASS_VERSION` | off | No | Ceiling, not a target: if the DB is ahead of this image's migrations but at or below this version, leave the schema as-is (no migration runs). For additive-only migrations (e.g. a new column) safe to keep even when running an older image. |

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

# Tolerate the DB being ahead up to version 3 (e.g. an additive migration
# already applied) without failing or rolling back
bazel run //leaflab/migrate:leaflab-migrate -- --bypass-version=3
```

## Local Development (Tilt)

The migration runs as a K8s Job on `tilt up`. It re-runs automatically when the image changes. No manual invocation is needed during normal dev.

```bash
DB_HOST=postgres-dev.leaflab-local-dev.svc.cluster.local
DB_PASSWORD=password
DB_NAME=leaflab
```
