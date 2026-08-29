# LeafLab UI — Environment Variables

> All environment variables required to run leaflab-ui, the HTMX browser
> surface for LeafLab (A8, FR13). Read this when configuring, deploying, or
> debugging.

All variables are read from `main.go`'s `LoadConfig`, the source of record for the defaults
below.

## Core

| Variable | Default | Required | Description |
|----------|---------|----------|--------------|
| `HOST` | `0.0.0.0` | No | Bind address. |
| `PORT` | `8000` | No | HTTP port. |
| `PG_DATABASE_URL` | — | Yes | PostgreSQL connection string for the DB-backed session store (`libs/go/htmxauth`'s `NewDBSessionManager`). This UI **always** uses DB-backed sessions and never falls back to cookie-only storage — a missing value fails boot (`NewApp` returns an error) rather than degrading silently, because cookie sessions cannot refresh access tokens or survive a BFF pod restart (FR13). Matches `leaflab-api` and `leaflab-migrate`'s own `PG_DATABASE_URL` — same database, same schema (the `leaflab_ui_session` table added by migration `014_htmxauth_sessions`), not a separate database. |

## Browser auth (OIDC / htmxauth)

| Variable | Default | Required | Description |
|----------|---------|----------|--------------|
| `AUTH_MODE` | `none` | No | HTTP session auth mode: `none` or `oidc`. `none` runs with no login and the developer holding every role — development only. |
| `OIDC_ISSUER` | — | Required when `AUTH_MODE=oidc` | OIDC issuer URL. Must be the same Keycloak realm `leaflab-api` validates gRPC tokens against — see `../api/ENV.md`'s `LEAFLAB_API_OIDC_ISSUER` and `libs/go/grpcauth/KEYCLOAK.md`. |
| `OIDC_CLIENT_ID` | — | Required when `AUTH_MODE=oidc` | OAuth client ID (confidential client — this is a server-side BFF, not a public client). |
| `OIDC_CLIENT_SECRET` | — | Required when `AUTH_MODE=oidc` | OAuth client secret. |
| `OIDC_REDIRECT_URI` | `http://localhost:8000/auth/callback` | No | OAuth callback URL registered with the OIDC provider. |
| `OIDC_POST_LOGOUT_REDIRECT_URI` | derived from `OIDC_REDIRECT_URI`'s origin | No | Where RP-initiated logout returns the browser to. `htmxauth` derives a default when unset. |
| `SECRET_KEY` | `dev-secret-key-change-in-production` | Recommended in production | Session cookie encryption key. Must be overridden outside development. |

## gRPC forwarding to leaflab-api

| Variable | Default | Required | Description |
|----------|---------|----------|--------------|
| `LEAFLAB_API_URL` | `leaflab-api:50051` | No | `leaflab-api`'s gRPC endpoint. |
| `GRPC_AUTH_MODE` | `none` | No | `none` or `oidc`, per `libs/go/grpcauth`. Selects how the BFF authenticates its calls to `leaflab-api`. Must match `leaflab-api`'s `LEAFLAB_API_AUTH_MODE` (a mismatch produces `codes.Unauthenticated` on every call). |

The BFF forwards the **logged-in user's own access token** on every `leaflab-api` call via
`grpcauth.NewUserTokenDialOption` — it holds no service-account credentials of its own and
mints no tokens (NFR18.1: this layer is transport, not policy). There is no separate
client-credential variable to configure.

## Modes

**Development (no auth):**
```bash
AUTH_MODE=none
GRPC_AUTH_MODE=none
PG_DATABASE_URL=postgres://postgres:password@localhost:5432/leaflab?sslmode=disable
LEAFLAB_API_URL=localhost:50051
```

**Production (OIDC + token forwarding):**
```bash
AUTH_MODE=oidc
GRPC_AUTH_MODE=oidc
OIDC_ISSUER=https://auth.example.com/realms/whale
OIDC_CLIENT_ID=leaflab-ui
OIDC_CLIENT_SECRET=<from-oidc-provider>
OIDC_REDIRECT_URI=https://leaflab.example.com/auth/callback
SECRET_KEY=<random-32-chars>
PG_DATABASE_URL=postgres://user:pass@postgres:5432/leaflab?sslmode=disable
LEAFLAB_API_URL=leaflab-api:50051
```

## Service Dependencies

- **leaflab-api**: board/device data (gRPC, token-forwarded)
- **PostgreSQL**: session store (same database/schema as `leaflab-api`/`leaflab-migrate`)

## Local Development

`leaflab-ui` is **not yet wired into `leaflab/Tiltfile`** — deployment integration is a later
task on this plan (see `../ui/BUILD.bazel`'s `release_app` comment). Run it directly against a
Tilt-provisioned `leaflab-api` and Postgres:

```bash
cd leaflab && tilt up   # starts RabbitMQ, Postgres, leaflab-migrate, leaflab-processor, leaflab-api

PG_DATABASE_URL=postgres://postgres:password@localhost:5432/leaflab?sslmode=disable \
LEAFLAB_API_URL=localhost:50051 \
bazel run //leaflab/ui:ui
```
