# Audience Score System — Environment Variables

> All environment variables read by `audience-score-system-migration`,
> `-web`, `-mcp`, and `-worker` — one `ENV.md` per domain per `AGENTS.md`.
> OAuth/YouTube variables (Google OAuth client credentials, YouTube API
> scopes/keys) are added by the tasks that introduce them (C1/C2); none
> exist yet.

## Database

Read via `//libs/go/db` (`web`, `mcp`, `worker`) and `//libs/go/migrate`
(`migrate`, which falls back to the discrete `DB_*` vars below if
`PG_DATABASE_URL` is unset — see `libs/go/migrate/README.md`).

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `PG_DATABASE_URL` | all | *(required)* | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/dbname?sslmode=disable` |
| `DB_HOST` | migration | `localhost` | Used only when `PG_DATABASE_URL` is unset |
| `DB_PORT` | migration | `5432` | Used only when `PG_DATABASE_URL` is unset |
| `DB_USER` | migration | `postgres` | Used only when `PG_DATABASE_URL` is unset |
| `DB_PASSWORD` | migration | `""` | Used only when `PG_DATABASE_URL` is unset |
| `DB_NAME` | migration | `postgres` | Used only when `PG_DATABASE_URL` is unset |
| `DB_SSL_MODE` | migration | `disable` | Used only when `PG_DATABASE_URL` is unset |
| `MIGRATE_AUTO_DOWN` | migration | `false` | Allow the default run to auto-migrate DOWN when the DB is ahead of this image's migrations (e.g. after a rollback), instead of failing loudly. See `libs/go/migrate/README.md` "Rollback detection" — a standing switch, not scoped to one rollback. |
| `MIGRATE_BYPASS_VERSION` | migration | off | Ceiling, not a target: if the DB is ahead of this image's migrations but at or below this version, leave the schema as-is (no migration runs). |

## Temporal

Read via `//libs/go/temporal`'s `ConfigFromEnv` (`worker`).

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `TEMPORAL_HOST` | worker | `localhost:7233` | Temporal frontend service `host:port`. |
| `TEMPORAL_NAMESPACE` | worker | `default` | Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | worker | *(none)* | Task queue name for the per-Channel sync worker. No default — must be set explicitly. |

## Logging

`//libs/go/logging`'s `Config.Level` is set programmatically per binary, not
read from the environment by the library itself. Following the
`manmanv2/processor` convention, each ASS binary's `main.go` reads
`LOG_LEVEL` and passes it into `logging.Configure`.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `LOG_LEVEL` | all | `info` | Minimum log level (`debug`, `info`, `warn`, `error`); parsed by each binary's own bootstrap, not by `libs/go/logging` directly. |

## Not yet added

OAuth client ID/secret for Google sign-in (C1) and the YouTube Data/Analytics
API scope/credentials for Channel connect (C2, LB1) will be documented here
by the tasks that introduce them — do not assume a var name from this file
in advance of that task.
