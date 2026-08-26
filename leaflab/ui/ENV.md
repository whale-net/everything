# LeafLab UI — Environment Variables

Configuration for the leaflab-ui (BFF) service. All variables are optional unless marked required.

## Service Configuration

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `HOST` | No | `0.0.0.0` | HTTP listening address |
| `PORT` | No | `8000` | HTTP listening port |

## Authentication

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `AUTH_MODE` | No | `none` | Authentication mode: `none` (development only) or `oidc` |
| `OIDC_ISSUER` | If OIDC | — | OIDC issuer URL (required when `AUTH_MODE=oidc`) |
| `OIDC_CLIENT_ID` | If OIDC | — | OIDC client ID (required when `AUTH_MODE=oidc`) |
| `OIDC_CLIENT_SECRET` | If OIDC | — | OIDC client secret (required when `AUTH_MODE=oidc`) |
| `OIDC_REDIRECT_URI` | No | `http://localhost:8000/auth/callback` | OIDC redirect URI (must match provider configuration) |
| `OIDC_POST_LOGOUT_REDIRECT_URI` | No | — | Optional: override post-logout redirect URI; derives from `OIDC_REDIRECT_URI` origin if unset |
| `SECRET_KEY` | No | `dev-secret-key-change-in-production` | Session cookie signing secret (change in production) |

## Database

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `PG_DATABASE_URL` | Yes | — | PostgreSQL connection string for session storage (always required; leaflab-ui never falls back to cookie-only sessions) |

## API Service

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LEAFLAB_API_URL` | No | `leaflab-api:50051` | leaflab-api gRPC endpoint (address:port) |
| `GRPC_AUTH_MODE` | No | `none` | gRPC auth mode for forwarding user tokens to leaflab-api: `none` or `oidc` (must match API's `LEAFLAB_API_AUTH_MODE`) |

## Phase 1 Access Control

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `LEAFLAB_PHASE1_GATE_OPEN` | No | `false` | Feature gate for Phase 1 access (A30: non-exposed to production). See #1187 for enforcement; removable in Phase 2 when FR5 scoping lands. **Must remain `false` in production deployments.** |

## Development

When running locally, the following suffices:
```bash
export HOST=0.0.0.0
export PORT=8000
export AUTH_MODE=none
export SECRET_KEY=dev-secret-key-change-in-production
export PG_DATABASE_URL="postgresql://postgres:password@localhost:5432/leaflab?sslmode=disable"
export LEAFLAB_API_URL=localhost:50051
export GRPC_AUTH_MODE=none
```
