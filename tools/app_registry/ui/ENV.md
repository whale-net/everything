# App Registry UI — Environment Variables

All configuration is read from environment variables only — no config
files.

## Required

| Variable | Description | Example |
|----------|-------------|---------|
| `PG_DATABASE_URL` | PostgreSQL connection string for the `ui_sessions` table (`libs/go/htmxauth`, DB-backed sessions only — never falls back to cookie sessions). Same variable name as the rest of App Registry (see `../ENV.md`), deliberately not `DATABASE_URL`. | `postgres://user:pass@postgres:5432/app_registry?sslmode=disable` |

## Optional

| Variable | Default | Description |
|----------|---------|--------------|
| `HOST` | `0.0.0.0` | Bind address |
| `PORT` | `8000` | HTTP port |
| `AUTH_MODE` | `none` | HTTP authentication mode: `none` or `oidc` |
| `SECRET_KEY` | `dev-secret-key-change-in-production` | Session encryption key; also derives the AES key for refresh-token encryption |
| `REGISTRY_API_URL` | `app-registry-api:50051` | `app-registry-api` gRPC endpoint |
| `GRPC_AUTH_MODE` | `none` | gRPC token forwarding mode: `none` or `oidc` — should match `app-registry-api`'s own `GRPC_AUTH_MODE` |

## OIDC (required when `AUTH_MODE=oidc`)

| Variable | Description | Example |
|----------|-------------|---------|
| `OIDC_ISSUER` | OIDC provider URL | `https://auth.example.com` |
| `OIDC_CLIENT_ID` | OAuth client ID | `app-registry-ui` |
| `OIDC_CLIENT_SECRET` | OAuth client secret | `secret123` |
| `OIDC_REDIRECT_URI` | OAuth callback URL | `https://app-registry.example.com/auth/callback` |

## gRPC auth (required when `GRPC_AUTH_MODE=oidc`)

The UI forwards the logged-in user's own access token to `app-registry-api`
on every call (FR-40) — no service account credentials of the UI's own are
used for registry data.

## Modes

**Development (no auth, DB-backed sessions still required):**
```bash
AUTH_MODE=none
GRPC_AUTH_MODE=none
SECRET_KEY=dev-secret
REGISTRY_API_URL=localhost:50051
PG_DATABASE_URL=postgres://user:pass@localhost:5432/app_registry?sslmode=disable
```

**Production (OIDC auth + DB sessions):**
```bash
AUTH_MODE=oidc
GRPC_AUTH_MODE=oidc
SECRET_KEY=<random-32-chars>
OIDC_ISSUER=https://auth.company.com
OIDC_CLIENT_ID=app-registry-ui
OIDC_CLIENT_SECRET=<from-oidc-provider>
OIDC_REDIRECT_URI=https://app-registry.company.com/auth/callback
REGISTRY_API_URL=app-registry-api:50051
PG_DATABASE_URL=postgres://user:pass@postgres:5432/app_registry
```

## Service dependencies

- **app-registry-api**: all registry domain reads/writes (gRPC)
- **PostgreSQL**: `ui_sessions` table only (`libs/go/htmxauth`) — never
  registry domain tables
