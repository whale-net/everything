# krill — Environment Variables

M1 scaffolding (issue #2487) ships two binaries: `migrate` and `api`. Both
read the variables below. `mcp` (later M1 tasks) will get its own section
once it exists.

## Database

Read via `//libs/go/db` (`api`) and `//libs/go/migrate` (`migrate`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `PG_DATABASE_URL` | migrate, api | *(required)* | PostgreSQL connection string. `migrate` applies `krill/migrate/schema/migrations` (starting with `001_scope`) and then seeds the one `scope` row (LB1, NFR2) -- see `migrate/seed`'s package doc comment. `api` uses this to construct its connection pool (`libs/go/db.NewPool`), which `/healthz` pings on every request. |

krill's forge coordinates (`repo_full_name`, `default_branch`) are **not**
environment-driven -- `migrate/seed` seeds them explicitly as constants
(NFR2: "the seeder to do this explicitly rather than leaving it to
template inference"), since M1 runs against exactly one repo/scope.

## `api` server

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `KRILL_API_ADDR` | api | `:8080` | Address `api`'s HTTP surface listens on. |

`api` exposes `/healthz` only in this task -- a live database connectivity
check, not a static 200 (`krill/api/main.go`'s doc comment). No spec
endpoints exist yet.

## Telemetry

Read via `//libs/go/logging` (`api`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | api | — | OTLP collector endpoint for traces/logs. Unset leaves telemetry export inert rather than failing boot, same convention every other domain's binaries follow. |
