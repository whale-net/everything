# Audience Score System — Environment Variables

> All environment variables read by `audience-score-system-migration`,
> `-web`, `-mcp`, and `-worker` — one `ENV.md` per domain per `AGENTS.md`.

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

Read via `//libs/go/temporal`'s `ConfigFromEnv` (`worker`), plus
`worker`'s own `ASS_SYNC_INTERVAL` (issue #1574, NFR4).

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `TEMPORAL_HOST` | worker | `localhost:7233` | Temporal frontend service `host:port`. |
| `TEMPORAL_NAMESPACE` | worker | `default` | Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | worker | `audience-score-system-sync` | Task queue name for the per-Channel sync worker (`sync.TaskQueue`). Unlike `//libs/go/temporal`'s own zero-default, `worker`'s `main.go` falls back to `sync.TaskQueue` when unset, mirroring `tools/app_registry/worker`'s identical fallback-to-package-constant pattern. |
| `ASS_SYNC_INTERVAL` | worker | `20m` | How often `sync.ChannelSyncWorkflow` runs per connected Channel (a Go `time.Duration` string, e.g. `20m`). Must fall within NFR4's ~15-30 minute band (`sync.MinSyncInterval`/`sync.MaxSyncInterval`) — `worker` fails fast at startup on an out-of-band or unparseable value rather than silently clamping it. |

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
| `ASS_TOKEN_ENCRYPTION_KEY` | web | *(required)* | Secret hashed (SHA-256) into a 32-byte AES-256-GCM key used to encrypt any stored Google refresh token at rest — mirrors `libs/go/htmxauth.DBSessionManager`'s `encKey` derivation. This same key also encrypts `channel_credential`'s YouTube token ciphertext (see "OAuth scopes" below) — one key, two independent token stores. |

## OAuth scopes (C2: YouTube Channel-connect)

Read by `web`'s `main.go` and `web/channel` (issue #1571). This is a
SEPARATE OAuth grant from "Web (C1: Google OAuth sign-in/sign-up)" above —
see `../ARCHITECTURE.md` "OAuth grants" for why. It reuses C1's
`ASS_GOOGLE_CLIENT_ID`/`ASS_GOOGLE_CLIENT_SECRET`/
`ASS_OAUTH_REDIRECT_BASE_URL` (same Google OAuth2 client, a different
redirect path — `web/channel` appends `/oauth/youtube/callback`) and
`ASS_TOKEN_ENCRYPTION_KEY` (same derivation, a different table:
`channel_credential`, migration 004, vs. `web_session`, migration 003). No
net-new environment variable is introduced by this grant.

**Scope set requested at first Channel-connect consent** (NFR1/LB1 —
`audience_score_system/web/channel.Scopes`):

| Scope | Why |
|-------|-----|
| `https://www.googleapis.com/auth/yt-analytics.readonly` | Owner-level YouTube Analytics access. Requested up front even though M1 only surfaces views/retention/CTR/impressions, so a later milestone (C16) never forces a re-consent of every connected Creator (LB1). |
| `https://www.googleapis.com/auth/youtube.readonly` | YouTube Data API access sufficient to list a Channel's own scheduled/private draft uploads (C6) via the authenticated owner's uploads playlist or `search.list?forMine=true` — verified against `developers.google.com/identity/protocols/oauth2/scopes`, which documents this scope (not the broader `youtube` manage scope) as sufficient for those owner-only reads. |

**`yt-analytics-monetary.readonly`: explicitly NOT requested.** M1's
`store.VideoMetrics` has no monetary field, and there is no near-term plan
to add one. Adding this scope later is exactly the re-consent cost LB1
exists to avoid paying twice, so it is deferred until a monetary-adjacent
field is genuinely wanted — at which point that is its own
scope-and-re-consent decision, not something to have silently pre-granted
here.

## MCP (C4-C7, C9, C10: MCP server foundation)

Read by `mcp`'s `main.go`. See `ARCHITECTURE.md`'s "MCP server: caller
authentication" for how a caller authenticates — there is no MCP-specific
client-secret variable here, because `mcp_credential` (migration 005)
stores only a SHA-256 hash, not a reversible secret.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `ASS_MCP_ADDR` | mcp | `:8081` | HTTP listen address for the `mcp` binary (streamable HTTP transport). |
