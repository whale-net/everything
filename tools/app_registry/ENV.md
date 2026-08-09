# App Registry — Environment Variables

> All environment variables read by `app-registry-api` and
> `app-registry-migration`. AR-1 ships no business logic, so this only covers
> connectivity, auth mode, and observability — not yet promotion/writeback
> configuration (AR-3/AR-4).
>
> **AR-4a** adds `libs/go/temporal` (client/worker bootstrap only — no
> outbox, no workflow, no `app-registry-worker` binary yet; that's AR-4b) and
> a Temporal dev server in Tilt. Its env vars are listed below for
> completeness, but nothing in `app-registry-api`/`app-registry-migration`
> reads them yet.

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
| `secrets.APP_REGISTRY_BUILDER_CLIENT_SECRET` | Repository secret | `GRPC_AUTH_CLIENT_SECRET` (`GRPC_AUTH_CLIENT_ID=app-registry-builder`) | `release.yml` recording steps only |
| `secrets.APP_REGISTRY_PROMOTER_CLIENT_SECRET` | Environment secret, one per `dev`/`stage`/`prod` GitHub Environment | `GRPC_AUTH_CLIENT_SECRET` (`GRPC_AUTH_CLIENT_ID=app-registry-promoter-<environment>`) | `promote.yml` only |

`GRPC_AUTH_MODE=oidc` is hardcoded in both workflows rather than read from a
variable — the CLI must match whatever the server runs, and the server is
expected to run `oidc` in every environment these workflows target.

## Temporal (`libs/go/temporal`, AR-4a foundations only)

| Variable | Default | Description |
|----------|---------|--------------|
| `TEMPORAL_HOST` | `localhost:7233` | Temporal frontend service `host:port`. Named to match `friendly_computing_machine`'s existing `TEMPORAL_HOST` convention. |
| `TEMPORAL_NAMESPACE` | `default` | Temporal namespace. |
| `TEMPORAL_TASK_QUEUE` | *(none)* | Default task queue name; no fallback. |

See [`libs/go/temporal/README.md`](../../libs/go/temporal/README.md) for the
client/worker API. See
[`TESTING.md`](TESTING.md#temporal-ar-4a) for exercising a local Temporal
dev server via Tilt.

## Local Development (Tilt)

```bash
# Enable/disable services (default: true)
ENABLE_APP_REGISTRY_MIGRATION=true
ENABLE_APP_REGISTRY_API=true
ENABLE_TEMPORAL=true

# Infrastructure — set to 'custom' to use an external Postgres
BUILD_POSTGRES_ENV=default       # or 'custom'
PG_DATABASE_URL=postgres://...   # if BUILD_POSTGRES_ENV=custom
```

Local access (`tilt up` from `tools/app_registry/`): API forwarded to
`localhost:50061`, Postgres to `localhost:5432`, Temporal gRPC to
`localhost:7233` and Web UI to `localhost:8233`.
