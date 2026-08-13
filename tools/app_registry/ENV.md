# App Registry — Environment Variables

> All environment variables read by `app-registry-api`,
> `app-registry-migration`, and `app-registry-worker`.
>
> **AR-4a** added `libs/go/temporal` (client/worker bootstrap) and a
> Temporal dev server in Tilt. **AR-4b** adds the first real consumer:
> `app-registry-worker`, which drains `writeback_outbox` and runs
> `WritebackWorkflow` — see its own section below and
> [`worker/README.md`](worker/README.md).

## Database

Both the server and the migration runner read `PG_DATABASE_URL` first; the
migration runner falls back to the discrete `DB_*` variables below if it is
unset (see `libs/go/migrate`'s `DefaultConfig`). The server has no fallback —
it requires `PG_DATABASE_URL`.

| Variable | Component | Default | Description |
|----------|-----------|---------|--------------|
| `PG_DATABASE_URL` | server, migration | *(required for server)* | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/dbname?sslmode=disable` |
| `DB_HOST` | migration | `localhost` | Used only when `PG_DATABASE_URL` is unset |
| `DB_PORT` | migration | `5432` | Used only when `PG_DATABASE_URL` is unset |
| `DB_USER` | migration | `postgres` | Used only when `PG_DATABASE_URL` is unset |
| `DB_PASSWORD` | migration | `""` | Used only when `PG_DATABASE_URL` is unset |
| `DB_NAME` | migration | `postgres` | Used only when `PG_DATABASE_URL` is unset |
| `DB_SSL_MODE` | migration | `disable` | Used only when `PG_DATABASE_URL` is unset |

## Server (`app-registry-api`)

| Variable | Default | Description |
|----------|---------|--------------|
| `PORT` | `50051` | gRPC listen port |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` — see `libs/go/grpcauth` |
| `GRPC_OIDC_ISSUER` | `""` | Keycloak/OIDC realm URL; required when `GRPC_AUTH_MODE=oidc` |
| `GRPC_OIDC_CLIENT_ID` | `""` | Expected audience in the JWT; required when `GRPC_AUTH_MODE=oidc` |

Standard `libs/go/logging` environment auto-detection also applies
(`APP_NAME`, `APP_DOMAIN`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_*_DISABLED`,
etc.) — see that package's doc comment for the full list.

### Role model (AR-3a)

Server-side enforcement lives in `server/auth`; see
[`ARCHITECTURE.md`](ARCHITECTURE.md) "Authorization" for the role table and
[`libs/go/grpcauth/KEYCLOAK.md`](../../libs/go/grpcauth/KEYCLOAK.md) for how
to configure the Keycloak side. In short: `AppRegistry.ReconcileApps`,
`ArtifactRegistry.RecordBuild`/`RecordArtifact` require
`app-registry-builder`; `AppRegistry.SetAppStatus` and every
`EnvironmentRegistry` write (`UpsertEnvironment`/`ArchiveEnvironment`)
require `app-registry-admin`; `PromotionRegistry.Promote`/`Rollback`
require the environment-scoped `app-registry-promoter-<environment_key>`
via `server/auth.RequirePromoter` (AR-3c); all read RPCs require only that
the caller is authenticated (any role).

**`GRPC_AUTH_MODE=none` and local/CI dev claims:** `libs/go/grpcauth`'s
dev-mode claims default to `Roles: ["admin"]`, which satisfies none of this
service's `app-registry-*` role checks. `server/main.go` overrides this via
`grpcauth.ServerConfig.DevRoles`, set to `server/auth.AllRoles()` — so in
`none` mode (the Tiltfile default, and the default for any CI path that
hasn't opted into `oidc`) every request is treated as holding every
app-registry role, and local dev / the AR-2c CI recording path keep working
unchanged. This only matters in `none` mode; `oidc` mode always uses the
token's real roles.

## CLI (`app-registry`)

| Variable | Default | Description |
|----------|---------|--------------|
| `APP_REGISTRY_ADDRESS` | `localhost:50051` | `app-registry-api` address; overridden by `--address` |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` — must match the server |
| `GRPC_AUTH_TOKEN_URL` | `""` | Keycloak token endpoint; required when `GRPC_AUTH_MODE=oidc` (e.g. `https://auth.example.com/realms/whale/protocol/openid-connect/token`) |
| `GRPC_AUTH_CLIENT_ID` | `""` | Keycloak service-account client ID (e.g. `app-registry-builder`); required when `GRPC_AUTH_MODE=oidc` |
| `GRPC_AUTH_CLIENT_SECRET` | `""` | Keycloak service-account client secret; required when `GRPC_AUTH_MODE=oidc` |
| `GRPC_USE_TLS` / `GRPC_TLS_SKIP_VERIFY` / `GRPC_CA_CERT_PATH` / `GRPC_TLS_SERVER_NAME` | — | TLS options — see `libs/go/grpcclient` |

The CLI fetches and auto-refreshes a client-credentials token via
`grpcauth.NewServiceAccountDialOption`, the same mechanism
`manmanv2/host` and `manmanv2/log-processor` use to reach their API. See
KEYCLOAK.md section 6 "CI — GitHub Actions" for the shape of a workflow job
setting these four variables.

### CI wiring (AR-3d)

`.github/workflows/release.yml`'s recording steps and
`.github/workflows/promote.yml` set the four CLI variables above from GitHub
Actions secrets/variables — see [DEPLOY.md](DEPLOY.md) §4 and §6 for the full
placement rationale:

| GitHub Actions name | Kind | Maps to | Used by |
|---|---|---|---|
| `vars.APP_REGISTRY_ADDRESS` | Repository variable | `APP_REGISTRY_ADDRESS` | both |
| `vars.APP_REGISTRY_AUTH_TOKEN_URL` | Repository variable | `GRPC_AUTH_TOKEN_URL` | both |
| `vars.APP_REGISTRY_BUILDER_ENV` | Repository variable | `GRPC_AUTH_CLIENT_ID=app-registry-builder-<value>`, falls back to `dev` when unset | `release.yml` recording steps, plus (AR-7f) "Build helm charts with versioning" |
| `secrets.APP_REGISTRY_BUILDER_CLIENT_SECRET` | Repository secret | `GRPC_AUTH_CLIENT_SECRET` | `release.yml` recording steps, plus (AR-7f) "Build helm charts with versioning" |
| `secrets.APP_REGISTRY_PROMOTER_CLIENT_SECRET` | Environment secret, one per GitHub Environment (e.g. `promotion-dev`/`promotion-prod`) | `GRPC_AUTH_CLIENT_SECRET` (`GRPC_AUTH_CLIENT_ID=app-registry-promoter-<registry_environment>`) | `promote.yml` only |

AR-7f (issue #558) adds one more consumer of the builder credential: the
release plan step's "Build helm charts with versioning" reuses these same
four variables so `tools/release_helper_go`'s `build-helm-chart` can call
`ArtifactRegistry.CheckChartHermeticity` when `APP_REGISTRY_CICD_OPT_IN` is
`true` — see ARCHITECTURE.md "Compose-time chart hermeticity (AR-7f, issue
#558)". No new variable was introduced for this; it dials the same server
with the same credential the later steps in that job already use.

`GRPC_AUTH_MODE=oidc` is hardcoded in both workflows rather than read from a
variable — the CLI must match whatever the server runs, and the server is
expected to run `oidc` in every environment these workflows target.

`vars.APP_REGISTRY_ADDRESS` must include the port (e.g.
`dev-app-registry.whalenet.dev:443`) — `libs/go/grpcclient`'s TLS
auto-detect (`shouldUseTLS`) only enables TLS when the address contains
`:443` or starts with `https://`; a bare hostname dials plaintext against a
TLS-only ingress and fails to connect (see issue #539). The Keycloak
clients backing the builder credential are environment-scoped
(`app-registry-builder-dev` / `app-registry-builder-prod`, mirroring the
promoter clients) — there is no bare `app-registry-builder` client.

## Temporal (`libs/go/temporal`)

| Variable | Default | Description |
|----------|---------|--------------|
| `TEMPORAL_HOST` | `localhost:7233` | Temporal frontend service `host:port`. Named to match `friendly_computing_machine`'s existing `TEMPORAL_HOST` convention. |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | *(none)* | Default task queue name; no fallback. `app-registry-worker` falls back to `writeback.TaskQueue` (`"app-registry-writeback"`) itself when this is unset — see below. |

See [`libs/go/temporal/README.md`](../../libs/go/temporal/README.md) for the
client/worker API. See
[`TESTING.md`](TESTING.md#temporal-ar-4a) for exercising a local Temporal
dev server via Tilt.

## Worker (`app-registry-worker`, AR-4b)

Every variable above under Database and Temporal also applies (the worker
needs `PG_DATABASE_URL` to drain the outbox and `TEMPORAL_HOST` to run
`WritebackWorkflow`). Additionally:

| Variable | Default | Description |
|----------|---------|--------------|
| `APP_REGISTRY_ADDRESS` | `localhost:50051` | `app-registry-api` address the stub `Writeback` activity reads state from via `GetEnvironmentState` — see `worker/writeback/stub.go`. Any authenticated credential works; that RPC requires no specific role. |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc`, for the client above — same semantics as the CLI's variable of the same name. |
| `GRPC_AUTH_TOKEN_URL` / `GRPC_AUTH_CLIENT_ID` / `GRPC_AUTH_CLIENT_SECRET` | `""` | Required when `GRPC_AUTH_MODE=oidc` — same as the CLI. |
| `WRITEBACK_OUTPUT_DIR` | `/tmp/app-registry-writeback` | Local directory the stub `Writeback` activity's `Publish` writes rendered `<environment_key>.json` documents (plus a `.state_hash` sidecar) to. Not a gitops path or S3 bucket — see `worker/README.md`, publishing anywhere is explicitly out of scope for AR-4b. |
| `WRITEBACK_BATCH_SIZE` | `20` | Max outbox rows claimed per drain pass. |
| `WRITEBACK_POLL_INTERVAL` | `5s` | Delay between drain passes (Go duration syntax, e.g. `5s`, `500ms`). |
| `WRITEBACK_CLAIM_STALE_AFTER` | `2m` | How long a `'claimed'` outbox row is left alone before a later pass reclaims it — must exceed `WritebackWorkflow`'s activity `StartToCloseTimeout` (30s) comfortably, or a still-healthy claim gets needlessly reclaimed. This is the knob that makes "worker killed mid-run" recoverable — see `worker/README.md`'s verification section. |
| `WORKER_ID` | `app-registry-worker-<hostname>` | Recorded in `writeback_outbox.claimed_by`, for operator visibility into which process holds a claim. |

### Artifact reaper (AR-7b, issue #558)

The stale-row reaper (`worker/reaper`) runs as a third loop in the same
`app-registry-worker` process, alongside the Temporal worker and the outbox
drainer, sweeping `artifact` rows stuck in `allocated`/`publishing` to
`failed` — see ARCHITECTURE.md "The reaper is not optional" and
`worker/README.md`.

| Variable | Default | Description |
|----------|---------|--------------|
| `ARTIFACT_REAPER_TIMEOUT` | `30m` | How long an `artifact` row may sit in `allocated` or `publishing` (measured from `state_changed_at`) before the next sweep moves it to `failed` with `fail_reason = 'stale'`. Go duration syntax. **AR-7d (issue #558): `state_changed_at` is stamped once, at plan time, for every target in a release run** — `release.yml`'s `plan-release` job calls `BeginPublishBatch` for the whole matrix before it fans out (see ARCHITECTURE.md "The run log" -> "As built (AR-7d)"), so a target whose matrix leg hasn't started yet has been "publishing" since plan time, not since its own push began. `release.yml`'s per-leg `Begin publish (image)` step re-arms this clock (a `publishing -> publishing` heartbeat) immediately before that leg's own push, and revives (`failed -> publishing`) a row the reaper already expired while the leg was still queued — so a reap that races a slow-to-schedule leg does not lose the eventual push, but it does cost that leg a transient, misleading `failed` state in `app-registry builds status` until it runs. **Set this comfortably longer than the WHOLE release run** (every matrix leg's cross-arch image build, end to end, plus queueing delay) **, not just the slowest individual leg** — a value sized only for one leg reaps every target that hasn't started yet almost immediately after plan time. |
| `ARTIFACT_REAPER_POLL_INTERVAL` | `5m` | Delay between sweep passes (Go duration syntax). Coarser than `WRITEBACK_POLL_INTERVAL` — this is a background hygiene sweep, not a redelivery loop reacting to a worker crash. |

## Local Development (Tilt)

```bash
# Enable/disable services (default: true)
ENABLE_APP_REGISTRY_MIGRATION=true
ENABLE_APP_REGISTRY_API=true
ENABLE_APP_REGISTRY_WORKER=true
ENABLE_TEMPORAL=true

# Infrastructure — set to 'custom' to use an external Postgres
BUILD_POSTGRES_ENV=default       # or 'custom'
PG_DATABASE_URL=postgres://...   # if BUILD_POSTGRES_ENV=custom
```

Local access (`tilt up` from `tools/app_registry/`): API forwarded to
`localhost:50061`, Postgres to `localhost:5432`, Temporal gRPC to
`localhost:7233` and Web UI to `localhost:8233`. The worker has no forwarded
port (it serves nothing) — inspect it via `tilt logs app-registry-worker` or
a shell into its pod (`WRITEBACK_OUTPUT_DIR` lives inside the container).
`ENABLE_APP_REGISTRY_WORKER=true` with `ENABLE_TEMPORAL=false` skips the
worker (it has nothing to poll) with a printed warning rather than deploying
into a guaranteed-crash loop.

## Postgres MCP (Claude Code plugin)

`.mcp.json` at the plugin root (`tools/app_registry/.mcp.json`, part of the
`app-registry` Claude Code plugin — see `.claude-plugin/marketplace.json`)
wires up three read-restricted (`--access-mode=restricted`) crystaldba
`postgres-mcp` servers via `uvx`, one per environment:

| Server | Connection |
|---|---|
| `app-registry-pg-tilt` | Hardcoded to the Tiltfile default (`postgres://postgres:password@localhost:5432/app_registry`) — matches `setup_postgres`'s local credentials, not a secret |
| `app-registry-pg-dev` | `APP_REGISTRY_DEV_DATABASE_URI` (shell env var, not set by default) |
| `app-registry-pg-prod` | `APP_REGISTRY_PROD_DATABASE_URI` (shell env var, not set by default) |

These are separate from `PG_DATABASE_URL` above (which the server/migration
processes read) so that dev and prod can be queried side by side from the
same Claude Code session without swapping a single variable.

`postgres-mcp`'s `pyproject.toml` declares an unpinned `mcp[cli]>=1.25.0`
dependency; `uvx` resolves that to `mcp==2.x`, which dropped
`mcp.server.fastmcp` and breaks `postgres-mcp` at import time
(`ModuleNotFoundError: No module named 'mcp.server.fastmcp'`). Each server's
`args` therefore pin it down with `--with "mcp<2"` before `postgres-mcp`.
See [crystaldba/postgres-mcp#187](https://github.com/crystaldba/postgres-mcp/issues/187).
If reconnecting still fails with the same error, `uv`'s package cache may
need a `uv cache clean postgres-mcp` / `uv cache clean mcp`.
