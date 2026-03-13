# ManManV2 UI - Environment Variables

## Required

| Variable | Description | Example |
|----------|-------------|---------|
| `SECRET_KEY` | Session encryption key | `random-32-char-string` |
| `CONTROL_API_URL` | Control API gRPC endpoint | `control-api:50051` |
| `LOG_PROCESSOR_URL` | Log processor gRPC endpoint | `log-processor:50053` |

## Optional

| Variable | Default | Description |
|----------|---------|-------------|
| `HOST` | `0.0.0.0` | Bind address |
| `PORT` | `8000` | HTTP port |
| `AUTH_MODE` | `none` | HTTP authentication mode: `none` or `oidc` |
| `GRPC_AUTH_MODE` | `none` | gRPC token forwarding mode: `none` or `oidc` |

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

**Development (no auth):**
```bash
AUTH_MODE=none
GRPC_AUTH_MODE=none
SECRET_KEY=dev-secret
CONTROL_API_URL=localhost:50051
LOG_PROCESSOR_URL=localhost:50053
```

**Production (OIDC auth):**
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
```

## Service Dependencies

- **control-api**: Session/server management (gRPC)
- **log-processor**: Real-time log streaming (gRPC → SSE)
