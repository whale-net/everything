# ManManV2 — Environment Variables

> All environment variables for the ManManV2 platform.
> Read this when configuring, deploying, or debugging runtime behavior.

## Component References

Each component documents its own env vars:
- [ui/ENV.md](ui/ENV.md) — UI service
- [api/S3_CONFIG.md](api/S3_CONFIG.md) — S3/object storage

## gRPC Authentication

All gRPC endpoints (API :50051, log-processor :50053) support JWT authentication via Keycloak. Default is `none` (dev mode — no Keycloak needed).

### API & Log-Processor (server side — incoming requests)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_OIDC_ISSUER` | `""` | Keycloak realm URL, e.g. `https://auth.company.com/realms/myrealm` |
| `GRPC_OIDC_CLIENT_ID` | `""` | Expected audience / client ID in token |

### Log-Processor & Host (client side — outgoing calls to API)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_AUTH_TOKEN_URL` | `""` | Keycloak token endpoint, e.g. `https://auth.company.com/realms/myrealm/protocol/openid-connect/token` |
| `GRPC_AUTH_CLIENT_ID` | `""` | Service account client ID |
| `GRPC_AUTH_CLIENT_SECRET` | `""` | Service account client secret |

### UI (client side — forwards logged-in user's token per request)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |

See [ui/ENV.md](ui/ENV.md) for full UI configuration.

## Local Development (`.env` / Tilt)

```bash
# Control Plane — enable/disable services (default: true)
ENABLE_MANMANV2_API=true
ENABLE_MANMANV2_PROCESSOR=true

# Build Options
BUILD_TEST_GAME_SERVER=true

# Infrastructure — set to 'custom' to use external instances
BUILD_POSTGRES_ENV=default       # or 'custom'
BUILD_RABBITMQ_ENV=default       # or 'custom'
POSTGRES_URL=postgresql://...    # if BUILD_POSTGRES_ENV=custom
RABBITMQ_HOST=...                # if BUILD_RABBITMQ_ENV=custom
RABBITMQ_PORT=5672
RABBITMQ_USER=...
RABBITMQ_PASSWORD=...

# S3/Object Storage (logs and backups)
S3_ENDPOINT=http://minio:9000
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_BUCKET=manmanv2-dev
```

## Database

All services that need PostgreSQL use `PG_DATABASE_URL`. The individual `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_PASSWORD`, `DB_NAME`, `DB_SSL_MODE` variables are no longer used.

| Variable | Description |
|----------|-------------|
| `PG_DATABASE_URL` | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/dbname` |

Services that read this variable: **API**, **log-processor** (archival), **UI** (DB-backed sessions).

---

## Control API — RestartDeployment

| Variable | Default | Description |
|----------|---------|-------------|
| `RESTART_STALL_TIMEOUT` | `45s` | How long a `RestartDeployment`-recorded `pending_restarts` row (see `ARCHITECTURE.md` "Pending Restarts") may sit `'pending'` before `PendingRestartReaper` (#1732) expires it. ~3x the ~15s window the UI's now-removed client-side poll bound (deleted by #1733) used to give the dispatched Stop real container-stop time, without letting a record sit indefinitely. Accepts Go duration syntax (e.g. `90s`, `2m`). |
| `RESTART_REAPER_INTERVAL` | `10s` | How often `PendingRestartReaper` (#1732) ticks to expire stalled `pending_restarts` rows. Combined with `RESTART_STALL_TIMEOUT`, worst-case stall-detection latency is `RESTART_STALL_TIMEOUT + RESTART_REAPER_INTERVAL` (~55s at the defaults) — keep this well below `RESTART_STALL_TIMEOUT` or the bound is meaningless. Accepts Go duration syntax (e.g. `5s`, `30s`). |

## Host Manager

```bash
SERVER_ID=host-local-dev-1
RABBITMQ_URL=amqp://rabbit:password@localhost:5672/manmanv2-dev
DOCKER_SOCKET=/var/run/docker.sock

# gRPC auth (service account, for calling the API)
GRPC_AUTH_MODE=none                     # or 'oidc'
GRPC_AUTH_TOKEN_URL=                    # Keycloak token endpoint
GRPC_AUTH_CLIENT_ID=                    # service account client ID
GRPC_AUTH_CLIENT_SECRET=               # service account client secret
```

## Event Processor

```bash
EXTERNAL_EXCHANGE=external            # existing external-facing RabbitMQ exchange
LIVE_EXCHANGE=manmanv2.htmxsse        # dedicated exchange for live status→UI triggers (see manmanv2/events, ARCHITECTURE.md § Event Processor → UI (Live Status))
```

`LIVE_EXCHANGE` defaults to `manmanv2/events.ExchangeName` (`manmanv2.htmxsse`) and only needs to be set explicitly to point at a non-default exchange. `manmanv2/ui` must be configured to consume from the same exchange name.

### Temporal (backup scheduler, M7)

| Variable | Default | Description |
|----------|---------|-------------|
| `TEMPORAL_HOST` | `localhost:7233` | Temporal frontend `host:port` the `backupsched` package's client dials (`libs/go/temporal.DefaultHostPort`). |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace (`libs/go/temporal.DefaultNamespace`). |
| `TEMPORAL_TASK_QUEUE` | `manmanv2-processor` | Task queue the event-processor worker polls, named after this worker binary per `ARCHITECTURE.md`'s task-queue convention (`manmanv2/processor/backupsched.DefaultTaskQueue`). |

The event-processor worker upserts a Temporal Schedule (`manmanv2-backup-scan`, 1-minute interval) at startup that drives `BackupScanWorkflow`/`DispatchBackupWorkflow` — see `ARCHITECTURE.md` § "Backup Scheduler (Temporal)". This is the sole backup scheduler; the earlier River-based one was removed in a hard cutover.

## Platform-Wide Variables

`GRPC_AUTH_MODE` appears on every component. Set it consistently across the platform — mismatched modes will cause `codes.Unauthenticated` errors.

## MCP server (`manmanv2/mcp`)

| Variable | Required | Description |
|---|---|---|
| `OIDC_ISSUER` | yes | Keycloak realm issuer URL used to verify caller bearer tokens |
| `OIDC_CLIENT_ID` | yes | Expected token audience |
| `PG_DATABASE_URL` | yes | Postgres for `mcp_idempotency_record` (write-tool idempotency keys) |
| `PORT` | no | Listen port (default `8081`) |
| `MCP_PUBLIC_URL` | no (set in deployed envs) | Externally reachable MCP URL. Serves RFC 9728 metadata at `/.well-known/oauth-protected-resource` (unauthenticated, `authorization_servers` = `OIDC_ISSUER`) and is advertised in 401 challenges. Unset: no metadata, clients fall back to a manual bearer token |
| `UI_PUBLIC_URL` | with `GRANT_*` | The manmanv2 UI's public URL: it hosts the OAuth authorization server (`/register`, `/authorize`, `/token`) and is advertised as the authorization server in the metadata |
| `GRANT_CLIENT_ID` / `GRANT_CLIENT_SECRET` / `GRANT_REDIRECT_URI` / `GRANT_ENCRYPTION_KEY` | no (set in deployed envs) | Delegated-grant config shared with the UI (see below). Set: clients authenticate with opaque credentials issued by the UI and the MCP acts as the user via their stored grant. Unset: Keycloak tokens are accepted directly and clients need `--client-id` |
| `MCP_WHAGENT_JWKS_URL` / `MCP_WHAGENT_ISSUER` | no | Enable whagent-net persona credentials: JWTs whose `iss` equals `MCP_WHAGENT_ISSUER` are verified against this JWKS (audience = `MCP_PUBLIC_URL`, `sub_iss` must equal `OIDC_ISSUER`) and the claim's (`sub_iss`, `sub`) is mapped to a manmanv2 user through the `whagent_identity_link` table (created by the UI's link flow, below); the call is then served as that user via their stored grant. Both must be set together, and still require `GRANT_*` (the stored grant yields the user's token for the control API) and `MCP_PUBLIC_URL` or the binary refuses to start. An unlinked identity returns the tool error `unauthenticated: whagent identity could not be resolved: ...` telling the user to run "Link manmanv2 identity" on whagent-net's `/grants` page; a linked user with no usable grant gets the same prefix with a re-link hint. Unset: no change |
| `MCP_RESOURCE_METADATA_URL` | no | Overrides the metadata URL advertised in 401 challenges (default derived from `MCP_PUBLIC_URL`) |
| `CONTROL_API_URL` | yes | Control API gRPC address; caller token is forwarded on every call |

Tilt: opt-in via `ENABLE_MANMANV2_MCP=true` with `MCP_OIDC_ISSUER` and `MCP_OIDC_CLIENT_ID` (required), optional `MCP_RESOURCE_METADATA_URL` and `MCP_CONTROL_API_URL`; forwarded to `localhost:8081`. Image `manmanv2-mcp` is also in the `manmanv2_chart` Helm composition. Helm: set `apps.manmanv2-mcp.env.{OIDC_ISSUER,OIDC_CLIENT_ID,PG_DATABASE_URL,CONTROL_API_URL}` (plus optional `MCP_RESOURCE_METADATA_URL`) in the deployment's values; the chart does not default them. Verified with `helm template --set apps.manmanv2-mcp.env.*`.

**Deployed envs (dev, prod) run in grant mode with the whagent verifier.** Values live in the deployment's values (outside this repo; the chart does not default them). Per env, set under `apps.manmanv2-mcp.env`:

| Key | Value |
|---|---|
| `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `PG_DATABASE_URL`, `CONTROL_API_URL` | As before |
| `MCP_PUBLIC_URL` | Exact audience whagent-net mints for manmanv2 MCP (also the RFC 9728 resource) |
| `UI_PUBLIC_URL` | The env's manmanv2 UI URL |
| `GRANT_CLIENT_ID` / `GRANT_CLIENT_SECRET` / `GRANT_REDIRECT_URI` / `GRANT_ENCRYPTION_KEY` | Identical to the UI's values (same secret refs) |
| `MCP_WHAGENT_JWKS_URL` | `http://whagent-net-api.<ns>.svc:8090/.well-known/jwks.json` (api's `additionalPorts` JWKS port, `WHAGENT_JWKS_ADDR`) |
| `MCP_WHAGENT_ISSUER` | The env's whagent-net api `iss` value (must match what `WHAGENT_ISSUER`-style config on whagent-net-api mints) |

**Linking a whagent-net identity (UI).** The manmanv2 UI serves `GET /link/whagent`, `POST /link/whagent/confirm` and `GET /link/whagent/complete`; whagent-net's `/grants` page starts the flow ("Link manmanv2 identity"). Requires the MCP OAuth settings above (`MCP_PUBLIC_URL`, `UI_PUBLIC_URL`, `GRANT_*`, `AUTH_MODE=oidc`, `PG_DATABASE_URL`), plus per env on `apps.manmanv2-ui.env`:

| Key | Value |
|---|---|
| `WHAGENT_UI_JWKS_URL` | whagent-net ui's JWKS: `<WHAGENT_UI_PUBLIC_URL>/.well-known/jwks.json` (or its in-cluster equivalent). Distinct from the MCP's `MCP_WHAGENT_JWKS_URL` (that verifies api-minted agent credentials) |
| `WHAGENT_UI_ISSUER` | whagent-net ui's `WHAGENT_UI_PUBLIC_URL`, exactly; assertions with another `iss`, or a `return_url` on another origin, are rejected |

Both set together, or neither (link routes not served). On confirm the UI writes `whagent_identity_link` (migration 049: `(iss, sub) -> user_sub`, one link per whagent identity; a different user for an existing pair is refused), records the assertion's `jti` in `whagent_link_assertion` (single use), and, if the user has no stored `mcp` grant yet, sends them through the one-time Keycloak consent before returning to whagent-net. The MCP needs no new variables.

Operator check after release: `helm template` renders all keys; the pod starts with no "requires grant mode" error and logs the whagent verifier as configured; an opaque-credential client (`claude mcp add mm2 <url> --transport http`) still connects. Apply dev first, then prod via the release action.

**OAuth (same model as krill, ASS, whagent-net).** The UI hosts `libs/go/auth`'s Provider (`/register`, `/authorize`, `/token`) using its own Keycloak session; the MCP verifies the opaque credential it issues (`mcp_credential`, migration 048). Because the control API needs the user's Keycloak token, `/authorize` first runs a one-time Keycloak consent (`libs/go/grpcauth/grantflow`) that stores the user's offline grant; the MCP exchanges it for a fresh token per request, so persona and `aud`/roles are the user's. Plain `claude mcp add mm2 <url> --transport http` then works with no `--client-id`.

UI env (when `MCP_PUBLIC_URL` is set; requires `AUTH_MODE=oidc` and `PG_DATABASE_URL`): `MCP_PUBLIC_URL`, `UI_PUBLIC_URL`, and the same `GRANT_*` as the MCP. `GRANT_CLIENT_ID`/`GRANT_CLIENT_SECRET` are the UI's existing `OIDC_CLIENT_ID`/`OIDC_CLIENT_SECRET`; `GRANT_REDIRECT_URI` is `<UI_PUBLIC_URL>/mcp/consent/callback` (add it to that Keycloak client, with the `offline_access` scope enabled). `GRANT_ENCRYPTION_KEY` must match in both apps.

There is no unauthenticated mode: the server refuses to start without the OIDC settings. Callers need a `gamer`, `server-manager`, or `manmanv2-admin` realm role; any other caller is refused every tool.
