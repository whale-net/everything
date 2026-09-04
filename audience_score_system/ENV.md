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

Read via `//libs/go/temporal`'s `ConfigFromEnv` (`web` and `worker` both,
as of issue #1614; `mcp` also, as of issue #1650, to build the
`sync.ScheduleManager` `trigger_channel_sync` calls), plus
`ASS_SYNC_INTERVAL` (issue #1574, NFR4, widened by issue-#1650's follow-up
work and again to a 24h default for quota headroom), also now read by
`web` and `worker`.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `TEMPORAL_HOST` | web, worker, mcp | `localhost:7233` | Temporal frontend service `host:port`. |
| `TEMPORAL_NAMESPACE` | web, worker, mcp | `default` | Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | web, worker, mcp | `audience-score-system-sync` | Task queue name for the per-Channel sync worker (`sync.TaskQueue`). Unlike `//libs/go/temporal`'s own zero-default, `web`'s, `worker`'s, and `mcp`'s `main.go` all fall back to `sync.TaskQueue` when unset, mirroring `tools/app_registry/worker`'s identical fallback-to-package-constant pattern. |
| `ASS_SYNC_INTERVAL` | web, worker | `24h` | How often `sync.ChannelSyncWorkflow` runs per connected Channel (a Go `time.Duration` string, e.g. `24h`). Must fall within NFR4's ~1-24 hour band (`sync.MinSyncInterval`/`sync.MaxSyncInterval`) — both `web` and `worker` fail fast at startup on an out-of-band or unparseable value rather than silently clamping it. Raised from an earlier 3h default (still spent enough quota on the schedule-discovery `search.list` call per Channel to threaten YouTube's default daily project quota); `mcp`'s `trigger_channel_sync` tool lets a caller force an out-of-band run without waiting for this cadence. |

**`web` reads these too, as of issue #1614:** `web/channel.Handler`
calls `sync.ScheduleManager.EnsureSchedule` right after a Channel-connect
callback reaches `connection_state = connected` (FR14/NFR4 — see
`ARCHITECTURE.md` "Schedule creation at connect time, not just at worker
startup"), so `web`'s `main.go` now constructs its own Temporal client
and `sync.ScheduleManager` at startup, following `worker/main.go`'s exact
pattern. **`web` and `worker` MUST be configured with the same
`ASS_SYNC_INTERVAL` value** (same default, same NFR4 band-check) — a
schedule's interval is fixed at whichever `EnsureSchedule` call creates it
first and never updated by a later call for the same Channel, so a
`web`/`worker` divergence here could silently create a Channel's schedule
at the wrong cadence depending on which binary connects it first.

`worker` also reads C2's `ASS_GOOGLE_CLIENT_ID`/`ASS_GOOGLE_CLIENT_SECRET`/
`ASS_TOKEN_ENCRYPTION_KEY` (see "OAuth scopes" below) as of issue #1576:
`SyncSchedule` refreshes/decrypts each Channel's `channel_credential` row
through the SAME `tokens.Store` construction `web` uses, so it needs the
same three variables to build it — no worker-specific credential
variable is introduced.

**`mcp` reads `TEMPORAL_HOST`/`TEMPORAL_NAMESPACE`/`TEMPORAL_TASK_QUEUE`
too, as of issue #1650:** the `trigger_channel_sync` tool
(`mcp/tools/sync_trigger.go`) forces an out-of-band run of a Channel's
`ChannelSyncWorkflow` via `sync.ScheduleManager.TriggerNow`, so `mcp`'s
`main.go` now constructs its own Temporal client and `sync.ScheduleManager`
at startup, the same pattern `web` and `worker` already use. `mcp` does
NOT read `ASS_SYNC_INTERVAL` — `TriggerNow` only patches an
already-created schedule (`ScheduleHandle.Trigger`), it never calls
`EnsureSchedule`, so no interval value is needed to build it.

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
| `ASS_GOOGLE_CLIENT_ID` | web, worker | *(required)* | Google OAuth2 client ID (Google Cloud Console "OAuth client ID") for the sign-in consent screen. `worker` reads the same variable to refresh a Channel's C2 credential (see "Temporal" above, issue #1576) — it never runs the sign-in flow itself. |
| `ASS_GOOGLE_CLIENT_SECRET` | web, worker | *(required)* | Google OAuth2 client secret paired with `ASS_GOOGLE_CLIENT_ID`. Same worker note as above. |
| `ASS_OAUTH_REDIRECT_BASE_URL` | web, mcp | *(required)* | This app's own externally-reachable origin, e.g. `https://audience-score.whalenet.dev` — `web/auth` appends `/oauth/google/callback` to build the redirect URL Google calls back to. **Also doubles as the OAuth2 issuer** (issue #1646, FR12/NFR4): `web` passes it as `mcpauth.ProviderConfig.Issuer`, so it is the base every `mcpauth` endpoint URL `web` advertises (`/authorize`, `/token`, `/register`, `/.well-known/oauth-authorization-server`) is built from — one origin, two roles (Google's redirect target and mcpauth's issuer), not two variables. `mcp` reads the SAME value (never calling it) purely to name it as the sole `authorization_servers` entry in its own protected-resource metadata (see "MCP" below) — `web` and `mcp` MUST agree on this exactly, or an MCP client's discovery chain (RFC 9728 → RFC 8414) points at the wrong authorization server. |
| `ASS_SESSION_SECRET` | web | *(required)* | Secret used to sign/protect the short-lived OAuth2 state cookie (CSRF) during the login round trip. |
| `ASS_TOKEN_ENCRYPTION_KEY` | web, worker | *(required)* | Secret hashed (SHA-256) into a 32-byte AES-256-GCM key used to encrypt any stored Google refresh token at rest — mirrors `libs/go/htmxauth.DBSessionManager`'s `encKey` derivation. This same key also encrypts `channel_credential`'s YouTube token ciphertext (see "OAuth scopes" below) — one key, two independent token stores. `worker` derives the same key to decrypt/refresh `channel_credential` for `SyncSchedule` (issue #1576). |

## OAuth scopes (C2: YouTube Channel-connect)

Read by `web`'s `main.go` and `web/channel` (issue #1571), and by
`worker`'s `main.go` and `tokens.Store` for the token-refresh half of the
same grant (issue #1576, `SyncSchedule`). This is a SEPARATE OAuth grant
from "Web (C1: Google OAuth sign-in/sign-up)" above — see
`../ARCHITECTURE.md` "OAuth grants" for why. It reuses C1's
`ASS_GOOGLE_CLIENT_ID`/`ASS_GOOGLE_CLIENT_SECRET`/
`ASS_OAUTH_REDIRECT_BASE_URL` (same Google OAuth2 client, a different
redirect path — `web/channel` appends `/oauth/youtube/callback`; `worker`
never redirects anywhere, it only refreshes an already-granted token) and
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
client-secret variable here, because `mcp_credential` (migration 006,
backed by `libs/go/mcpauth.CredentialStore`) stores only a SHA-256 hash,
not a reversible secret. `mcpauth.NewCredentialStore` preflights this
table at boot (a `SELECT 1 ... LIMIT 0` probe), so a missing migration 006
now fails `mcp` at startup rather than at first bearer-token verification.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `ASS_MCP_ADDR` | mcp | `:8081` | HTTP listen address for the `mcp` binary (streamable HTTP transport). |
| `ASS_MCP_PUBLIC_URL` | web, mcp | *(required)* | The externally reachable URL of the `mcp` server (issue #1646, FR12/NFR4) — the OAuth2 `resource` identifier both binaries must agree on exactly. `web` passes it as `mcpauth.ProviderConfig.Resource` when constructing its OAuth2 authorization server (see "Web" above); `mcp` passes the same value as `mcpauth.ProtectedResourceMetadataConfig.Resource` (`mcp/server.ResourceMetadataConfig.Resource`) — both the `resource` field its own `/.well-known/oauth-protected-resource` document advertises, and the base its `WWW-Authenticate: Bearer resource_metadata="..."` 401 challenge is built from (`mcpauth.ProtectedResourceMetadataURL`). A mismatch between the two binaries breaks MCP client discovery (RFC 9728), since the `resource` an MCP client requests a token for would no longer match the `resource` `mcp` actually serves. Both `web` and `mcp` fail fast at startup if unset. |

## Postgres MCP (Claude Code plugin)

`.mcp.json` at the plugin root (`audience_score_system/plugin/data/.mcp.json`,
symlinked to `.agents/plugins/audience-score-system-data` — see
`.claude-plugin/marketplace.json`) wires up three read-restricted
(`--access-mode=restricted`) crystaldba `postgres-mcp` servers via `uvx`, one
per environment, following `tools/app_registry`'s identical plugin pattern:

| Server | Connection |
|---|---|
| `audience-score-system-pg-tilt` | Hardcoded to the local default (`postgres://postgres:password@localhost:5432/audience_score_system`) — not a secret |
| `audience-score-system-pg-dev` | `ASS_DEV_DATABASE_URI` (shell env var, not set by default) |
| `audience-score-system-pg-prod` | `ASS_PROD_DATABASE_URI` (shell env var, not set by default) |

These are separate from `PG_DATABASE_URL` above (which the `web`/`mcp`/
`worker`/`migrate` binaries read) so that tilt, dev, and prod can be queried
side by side from the same Claude Code session without swapping a single
variable. This is also separate from the `audience-score-system` plugin
(`audience_score_system/plugin/user`), which gives streamable-HTTP MCP
access to the app's own `mcp` server (C4-C7, C9, C10 tools) rather than
direct Postgres access.
