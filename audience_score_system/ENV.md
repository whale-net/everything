# Audience Score System — Environment Variables

> All environment variables read by `audience-score-system-migration`,
> `-web`, `-mcp`, and `-worker` — one `ENV.md` per domain per `AGENTS.md`.
> Channel-connect (C2, YouTube Data/Analytics scopes) variables are added
> by the task that introduces them (#1571).

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

## Web (C1: Google OAuth sign-in/sign-up)

Read by `web`'s `main.go` and `web/auth`. See `web/auth/auth.go`'s package
doc comment for why these are Google-only and deliberately separate from
`libs/go/htmxauth`'s generic-OIDC `OIDC_*` variable names.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `ASS_HTTP_ADDR` | web | `:8080` | HTTP listen address for the `web` binary. |
| `ASS_GOOGLE_CLIENT_ID` | web | *(required)* | Google OAuth2 client ID (Google Cloud Console "OAuth client ID") for the sign-in consent screen. |
| `ASS_GOOGLE_CLIENT_SECRET` | web | *(required)* | Google OAuth2 client secret paired with `ASS_GOOGLE_CLIENT_ID`. |
| `ASS_OAUTH_REDIRECT_BASE_URL` | web | *(required)* | This app's own externally-reachable origin, e.g. `https://audience-score.whalenet.dev` — `web/auth` appends `/oauth/google/callback` to build the redirect URL Google calls back to. |
| `ASS_SESSION_SECRET` | web | *(required)* | Secret used to sign/protect the short-lived OAuth2 state cookie (CSRF) during the login round trip. |
| `ASS_TOKEN_ENCRYPTION_KEY` | web | *(required)* | Secret hashed (SHA-256) into a 32-byte AES-256-GCM key used to encrypt any stored Google refresh token at rest — mirrors `libs/go/htmxauth.DBSessionManager`'s `encKey` derivation. |

## Not yet added

YouTube Data/Analytics API scope/credentials for Channel connect (C2, LB1)
will be documented here by the task that introduces them (#1571) — do not
assume a var name from this file in advance of that task.
