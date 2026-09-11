# krill — Environment Variables

M1 scaffolding (issue #2487) ships two binaries: `migrate` and `api`. Both
read the variables below. `krill/importer/cmd`'s `import` CLI (issue
#2492) reads `PG_DATABASE_URL` too (via a `--database-url` flag that
defaults to it), but is not a deployed binary and takes its other inputs
(`--path`, `--session-id`) as flags -- see `krill/README.md`'s Binaries
table. `mcp` (later M1 tasks) will get its own section once it exists.

## Database

Read via `//libs/go/db` (`api`) and `//libs/go/migrate` (`migrate`, which
falls back to the discrete `DB_*` vars below if `PG_DATABASE_URL` is unset
-- see `libs/go/migrate/README.md`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `PG_DATABASE_URL` | migrate, api | *(required)* | PostgreSQL connection string. `migrate` applies `krill/migrate/schema/migrations` (starting with `001_scope`) and then seeds the one `scope` row (LB1, NFR2) -- see `migrate/seed`'s package doc comment. `api` uses this to construct its connection pool (`libs/go/db.NewPool`), which `/healthz` pings on every request. |
| `DB_HOST` | migrate | `localhost` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_PORT` | migrate | `5432` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_USER` | migrate | `postgres` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_PASSWORD` | migrate | `""` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_NAME` | migrate | `postgres` | Used only when `PG_DATABASE_URL` is unset. |
| `DB_SSL_MODE` | migrate | `disable` | Used only when `PG_DATABASE_URL` is unset. |
| `MIGRATE_AUTO_DOWN` | migrate | `false` | Allow the default run to auto-migrate DOWN when the DB is ahead of this image's migrations (e.g. after a rollback), instead of failing loudly. See `libs/go/migrate/README.md` "Rollback detection" -- a standing switch, not scoped to one rollback. |
| `MIGRATE_BYPASS_VERSION` | migrate | off | Ceiling, not a target: if the DB is ahead of this image's migrations but at or below this version, leave the schema as-is (no migration runs). |

krill's forge coordinates (`repo_full_name`, `default_branch`) are **not**
environment-driven -- `migrate/seed` seeds them explicitly as constants
(NFR2: "the seeder to do this explicitly rather than leaving it to
template inference"), since M1 runs against exactly one repo/scope.

## `api` server

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `KRILL_API_ADDR` | api | `:8080` | Address `api`'s HTTP surface listens on. |

`api` exposes `/healthz` (a live database connectivity check, not a static
200 -- `krill/api/main.go`'s doc comment), `POST /sessions/init` (FR3), and
the M1 entity write endpoints (FR1/FR2/FR4, issue #2490) -- see
`krill/README.md`'s Endpoints table. No new configuration was added for the
write endpoints; they read the same `PG_DATABASE_URL` pool as `/healthz`.

## Telemetry

Read via `//libs/go/logging` (`api`).

| Variable | Component | Default | Description |
|----------|-----------|---------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | api | — | OTLP collector endpoint for traces/logs. Unset leaves telemetry export inert rather than failing boot, same convention every other domain's binaries follow. |
