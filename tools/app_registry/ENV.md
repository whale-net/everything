# App Registry — Environment Variables

> All environment variables read by `app-registry-api` and
> `app-registry-migration`. AR-1 ships no business logic, so this only covers
> connectivity, auth mode, and observability — not yet promotion/writeback
> configuration (AR-3/AR-4).

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

## Local Development (Tilt)

```bash
# Enable/disable services (default: true)
ENABLE_APP_REGISTRY_MIGRATION=true
ENABLE_APP_REGISTRY_API=true

# Infrastructure — set to 'custom' to use an external Postgres
BUILD_POSTGRES_ENV=default       # or 'custom'
PG_DATABASE_URL=postgres://...   # if BUILD_POSTGRES_ENV=custom
```

Local access (`tilt up` from `tools/app_registry/`): API forwarded to
`localhost:50061`, Postgres to `localhost:5432`.
