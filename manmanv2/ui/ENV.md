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
| `AUTH_MODE` | `none` | Authentication mode: `none` or `oidc` |

## OIDC (Required when AUTH_MODE=oidc)

| Variable | Description | Example |
|----------|-------------|---------|
| `OIDC_ISSUER` | OIDC provider URL | `https://auth.example.com` |
| `OIDC_CLIENT_ID` | OAuth client ID | `manmanv2-ui` |
| `OIDC_CLIENT_SECRET` | OAuth client secret | `secret123` |
| `OIDC_REDIRECT_URI` | OAuth callback URL | `https://manman.example.com/auth/callback` |

## Modes

**Development (no auth):**
```bash
AUTH_MODE=none
SECRET_KEY=dev-secret
CONTROL_API_URL=localhost:50051
LOG_PROCESSOR_URL=localhost:50053
```

**Production (OIDC auth):**
```bash
AUTH_MODE=oidc
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
