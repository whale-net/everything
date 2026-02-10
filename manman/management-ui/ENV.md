# Management UI - Environment Variables

## Required

| Variable | Description | Example |
|----------|-------------|---------|
| `SECRET_KEY` | Session encryption key | `random-32-char-string` |
| `EXPERIENCE_API_URL` | Experience API endpoint | `http://experience-api:8000` |

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
| `OIDC_CLIENT_ID` | OAuth client ID | `manman-ui` |
| `OIDC_CLIENT_SECRET` | OAuth client secret | `secret123` |
| `OIDC_REDIRECT_URI` | OAuth callback URL | `https://manage.manman.com/auth/callback` |

## Modes

**Development (no auth):**
```bash
AUTH_MODE=none
SECRET_KEY=dev-secret
EXPERIENCE_API_URL=http://localhost:8000
```

**Production (OIDC auth):**
```bash
AUTH_MODE=oidc
SECRET_KEY=<random-32-chars>
OIDC_ISSUER=https://auth.company.com
OIDC_CLIENT_ID=manman-management-ui
OIDC_CLIENT_SECRET=<from-oidc-provider>
OIDC_REDIRECT_URI=https://manage.manman.company.com/auth/callback
EXPERIENCE_API_URL=http://experience-api:8000
```
