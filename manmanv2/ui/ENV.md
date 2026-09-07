# ManManV2 UI - Environment Variables

## Required

| Variable | Description | Example |
|----------|-------------|---------|
| `SECRET_KEY` | Session encryption key (also derives the AES key for refresh token encryption) | `random-32-char-string` |
| `CONTROL_API_URL` | Control API gRPC endpoint | `control-api:50051` |
| `LOG_PROCESSOR_URL` | Log processor gRPC endpoint | `log-processor:50053` |

## Recommended

| Variable | Description | Example |
|----------|-------------|---------|
| `DATABASE_URL` | PostgreSQL connection string for DB-backed sessions. Without this, sessions fall back to cookie storage and access tokens cannot be refreshed — users will experience gRPC failures after the access token expires (typically 5–15 min). | `postgres://user:pass@postgres:5432/manman` |

## Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `HOST` | `0.0.0.0` | Bind address |
| `PORT` | `8000` | HTTP port |
| `AUTH_MODE` | `none` | HTTP authentication mode: `none` or `oidc` |
| `GRPC_AUTH_MODE` | `none` | gRPC token forwarding mode: `none` or `oidc` |
| `RABBITMQ_URL` | *(unset)* | Backs the `/api/live/deployments` SSE hub's `manmanv2.htmxsse` exchange consumer (issue #1724). Unset, or the broker unreachable at startup, degrades to live updates disabled — `/api/live/deployments` returns `503` and the UI otherwise starts and serves `/sessions` normally (NFR3/NFR8). Format: `amqp[s]://username:password@host:port/vhost`. |
| `MANMANV2_SSE_HEARTBEAT_INTERVAL` | `5s` | `/api/live/deployments` heartbeat interval (parsed via `time.ParseDuration`, e.g. `10s`, `1m`). Must stay positive and satisfy `MANMANV2_SSE_ADVERTISED_RETRY_INTERVAL < 2 * MANMANV2_SSE_HEARTBEAT_INTERVAL` — see below. An unparseable or non-positive value falls back to this default (logged as a `WARNING`). |
| `MANMANV2_SSE_MAX_STREAM_LIFETIME` | `1h` | `/api/live/deployments` maximum single-connection lifetime before the server closes the stream (client reconnects automatically). Parsed via `time.ParseDuration`; unparseable falls back to the default. |
| `MANMANV2_SSE_SUBSCRIBER_BUFFER_DEPTH` | `100` | Per-topic event channel buffer depth for `/api/live/deployments` subscribers. Parsed via `strconv.Atoi`; unparseable falls back to the default. |
| `MANMANV2_SSE_ADVERTISED_RETRY_INTERVAL` | `2s` | SSE `retry:` field advertised to the browser's EventSource reconnect logic for `/api/live/deployments`. Parsed via `time.ParseDuration`; unparseable falls back to the default. **Constraint:** must be `< 2 * MANMANV2_SSE_HEARTBEAT_INTERVAL`, or `initializeSSEHub` logs a `WARNING` and falls back to `htmxsse.DefaultConfig()`'s values entirely rather than starting with an invalid hub config. |

## OIDC (Required when AUTH_MODE=oidc)

| Variable | Description | Example |
|----------|-------------|---------|
| `OIDC_ISSUER` | OIDC provider URL | `https://auth.example.com` |
| `OIDC_CLIENT_ID` | OAuth client ID | `manmanv2-ui` |
| `OIDC_CLIENT_SECRET` | OAuth client secret | `secret123` |
| `OIDC_REDIRECT_URI` | OAuth callback URL | `https://manman.example.com/auth/callback` |

## gRPC Auth (Required when GRPC_AUTH_MODE=oidc)

The UI forwards the logged-in user's access token to the API and log-processor on every gRPC call. No service account credentials are needed — the user's own token is used.

| Variable | Description |
|----------|-------------|
| `GRPC_AUTH_MODE` | Set to `oidc` to enable token forwarding |

> `GRPC_AUTH_MODE` should match `GRPC_AUTH_MODE` on the API and log-processor servers.

## Modes

**Development (no auth, no DB):**
```bash
AUTH_MODE=none
GRPC_AUTH_MODE=none
SECRET_KEY=dev-secret
CONTROL_API_URL=localhost:50051
LOG_PROCESSOR_URL=localhost:50053
# DATABASE_URL not set — cookie sessions, no token refresh needed in dev
```

**Production (OIDC auth + DB sessions):**
```bash
AUTH_MODE=oidc
GRPC_AUTH_MODE=oidc
SECRET_KEY=<random-32-chars>
OIDC_ISSUER=https://auth.company.com
OIDC_CLIENT_ID=manmanv2-ui
OIDC_CLIENT_SECRET=<from-oidc-provider>
OIDC_REDIRECT_URI=https://manman.company.com/auth/callback
CONTROL_API_URL=control-api:50051
LOG_PROCESSOR_URL=log-processor:50053
DATABASE_URL=postgres://user:pass@postgres:5432/manman
```

## Service Dependencies

- **control-api**: Session/server management (gRPC)
- **log-processor**: Real-time log streaming (gRPC → SSE)
