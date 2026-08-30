# leaflab-ui — Environment Variables

> Read this when configuring, deploying, or debugging `leaflab-ui`.

| Variable | Default | Description |
|----------|---------|--------------|
| `HOST` | `0.0.0.0` | HTTP listen address |
| `PORT` | `8000` | HTTP listen port |
| `AUTH_MODE` | `none` | `none` or `oidc`. `none` treats every request as a fixed dev user (`htmxauth.AllRoles`) — for local development only; `leaflab/Tiltfile` uses it. **No deployed configuration should set `AUTH_MODE=none`** — see "M1 auth posture" below. |
| `SECRET_KEY` | `dev-secret-key-change-in-production` | Session encryption key. Encrypts refresh tokens at rest in `ui_sessions` and signs the short-lived OAuth-state cookie. Must be set to a real secret in any deployed environment. |
| `OIDC_ISSUER` | `""` | OIDC provider realm URL. Required when `AUTH_MODE=oidc`. |
| `OIDC_CLIENT_ID` | `""` | **This UI's own** OIDC client ID — distinct from `leaflab-api`'s (`leaflab/api/ENV.md`'s `GRPC_OIDC_CLIENT_ID`). Required when `AUTH_MODE=oidc`. |
| `OIDC_CLIENT_SECRET` | `""` | This UI's OIDC client secret. Required when `AUTH_MODE=oidc`. |
| `OIDC_REDIRECT_URI` | `http://localhost:8000/auth/callback` | Must exactly match a Valid Redirect URI registered on this UI's OIDC client: `<host>/auth/callback`. |
| `PG_DATABASE_URL` | `""` | PostgreSQL connection string, e.g. `postgres://user:pass@host:5432/leaflab?sslmode=disable`. **Always required** (NFR3) — leaflab-ui hard-fails at startup without it, naming DB-backed sessions as the reason; it never falls back to cookie-only sessions the way `manmanv2/ui` does. Backs both `libs/go/htmxauth`'s session store (`ui_sessions` table, migration 014) and the `leaflab_user` upsert (FR2, migration 013). |
| `LEAFLAB_API_URL` | `leaflab-api:50051` | `leaflab-api`'s gRPC address. |
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` — see `libs/go/grpcauth`. Controls whether the signed-in user's access token is attached to outgoing `leaflab-api` calls. |

## Two OIDC clients

This UI's OIDC client (`OIDC_CLIENT_ID` above) is separate from
`leaflab-api`'s own (`leaflab/api/ENV.md`'s `GRPC_OIDC_CLIENT_ID`). For
gRPC calls to `leaflab-api`'s three fenced read RPCs to succeed when
`GRPC_AUTH_MODE=oidc`, **this UI's client must be configured so a
signed-in user's access token includes `leaflab-api`'s client ID in its
`aud` claim** — otherwise every call to `ListBoardsWithState`,
`GetBoardDetail`, and `GetSensorReadingHistory` is rejected with
`codes.Unauthenticated` even from a legitimately signed-in user. See
`libs/go/grpcauth/KEYCLOAK.md`.

## M1 auth posture (FR1)

Being authenticated is the only requirement any route in this UI enforces.
There is no role check, no `leaflab_user_role` lookup, and no ownership
filter anywhere — authorization is out of scope until M2. `AUTH_MODE=none`
stays available for local development; since nothing at runtime
distinguishes "deployed" from "local", the requirement on deployed
environments is that **no deployed configuration enables `AUTH_MODE=none`**.

Standard `libs/go/logging` environment auto-detection also applies
(`APP_NAME`, `APP_DOMAIN`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_*_DISABLED`,
etc.) — see that package's doc comment for the full list.
