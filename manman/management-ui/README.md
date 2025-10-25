# ManMan Management UI

Go-based HTMX web interface for managing ManMan services with dual authentication modes.

## Architecture

- **Backend**: Go HTTP server with `html/template`
- **Frontend**: Server-rendered HTML with HTMX for dynamic updates
- **Authentication**: `//libs/go/htmxauth` - Dual mode auth library
  - **No-Auth Mode**: Development/testing without authentication
  - **OIDC Mode**: Production authentication via Keycloak
- **API Client**: Generated Go client for Experience API

## Features

- **Dual Auth Modes**: Switch between no-auth and OIDC with environment variable
- **HTMX Real-time Updates**: Auto-refreshing worker status and server list
- **Worker Monitoring**: View active worker IDs
- **Server Management**: Track running game servers
- **Experience API Integration**: Consumes Experience API via generated Go client
- **Reusable Auth**: Uses `//libs/go/htmxauth` library for authentication

## Configuration

### Required Environment Variables

```bash
# Auth Mode (default: none)
AUTH_MODE=none              # Options: "none" or "oidc"

# Session Secret (always required)
SECRET_KEY=your-random-secret-key
```

### OIDC Mode (Additional Required Variables)

```bash
AUTH_MODE=oidc

# OIDC Configuration
OIDC_ISSUER=https://keycloak.example.com/realms/myrealm
OIDC_CLIENT_ID=management-ui
OIDC_CLIENT_SECRET=your-client-secret
OIDC_REDIRECT_URI=https://manage.manman.local/auth/callback
```

### Optional Variables

```bash
# Server
HOST=0.0.0.0
PORT=8000

# Experience API
EXPERIENCE_API_URL=http://experience-api-dev-service:8000
```

## Quick Start

### Development (No-Auth Mode)

```bash
# Set minimal configuration
export AUTH_MODE=none
export SECRET_KEY=dev-secret

# Build and run
bazel run //manman/management-ui:management_ui

# Access at http://localhost:8000
# No login required - automatically uses dev user
```

### Production (OIDC Mode)

```bash
# Set full OIDC configuration
export AUTH_MODE=oidc
export SECRET_KEY=$(openssl rand -base64 32)
export OIDC_ISSUER=https://your-keycloak/realms/your-realm
export OIDC_CLIENT_ID=management-ui
export OIDC_CLIENT_SECRET=your-secret
export OIDC_REDIRECT_URI=https://manage.manman.local/auth/callback

# Build and run
bazel run //manman/management-ui:management_ui
```

## Authentication Modes

### No-Auth Mode (Development)

**Use for**: Local development, integration tests, demos

**Behavior**:
- No login required
- All users get a default dev user identity
- User info: `sub="dev-user"`, `name="Development User"`
- `/auth/login` and `/auth/logout` redirect to home

⚠️ **Never use in production!**

**Configuration**:
```bash
AUTH_MODE=none
SECRET_KEY=any-value
```

### OIDC Mode (Production)

**Use for**: Production deployments

**Behavior**:
- Full OIDC authentication flow
- Users must log in via Keycloak
- Session-based authentication
- Secure token handling

**Configuration**:
```bash
AUTH_MODE=oidc
OIDC_ISSUER=https://keycloak.example.com/realms/myrealm
OIDC_CLIENT_ID=management-ui
OIDC_CLIENT_SECRET=your-secret
OIDC_REDIRECT_URI=https://manage.manman.local/auth/callback
SECRET_KEY=$(openssl rand -base64 32)
```

## Keycloak Setup (OIDC Mode Only)

### 1. Create Client

1. Navigate to your Keycloak realm
2. **Clients** → **Create**
3. Configure:
   - **Client ID**: `management-ui`
   - **Client Protocol**: `openid-connect`
   - **Access Type**: `confidential`
   - **Valid Redirect URIs**: 
     - `https://manage.manman.local/auth/callback`
     - `http://localhost:8000/auth/callback` (dev)
   - **Web Origins**: `https://manage.manman.local`

### 2. Required Scopes

Standard OIDC scopes:
- `openid` - Required
- `profile` - User profile information
- `email` - User email address

### 3. Get Credentials

1. **Credentials** tab
2. Copy **Client Secret**
3. Set in `OIDC_CLIENT_SECRET`

## Local Development

### Build

```bash
# Build the application
bazel build //manman/management-ui:management_ui

# Run locally
bazel run //manman/management-ui:management_ui
```

### Environment Setup

```bash
export OIDC_ISSUER=https://your-keycloak/realms/your-realm
export OIDC_CLIENT_ID=management-ui
export OIDC_CLIENT_SECRET=your-secret
export OIDC_REDIRECT_URI=http://localhost:8000/auth/callback
export SECRET_KEY=dev-secret-key
export EXPERIENCE_API_URL=http://localhost:8000

bazel run //manman/management-ui:management_ui
```

### Testing

```bash
# Run tests
bazel test //manman/management-ui:management_ui_test

# Verbose output
bazel test //manman/management-ui:management_ui_test --test_output=all
```

## Kubernetes Deployment

Automatically included in `manman-host-services` Helm chart.

### Kubernetes Secret

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: management-ui-secrets
  namespace: manman
type: Opaque
stringData:
  OIDC_ISSUER: "https://keycloak.example.com/realms/myrealm"
  OIDC_CLIENT_ID: "management-ui"
  OIDC_CLIENT_SECRET: "your-client-secret"
  OIDC_REDIRECT_URI: "https://manage.manman.local/auth/callback"
  SECRET_KEY: "generate-secure-key"
```

### Ingress

- **Host**: `manage.manman.local`
- **TLS Secret**: `manman-tls`
- **Type**: `external-api`

## Security

### Authentication Flow

1. User visits homepage (`/`)
2. Redirected to `/auth/login` if not authenticated
3. OIDC flow initiated with Keycloak
4. User authenticates with Keycloak
5. Keycloak redirects to `/auth/callback` with auth code
6. App exchanges code for tokens
7. Session created, user redirected

### Session Management

- Server-side sessions via `gorilla/sessions`
- HTTP-only cookies
- 24-hour session lifetime
- CSRF protection via OAuth2 state parameter

### Route Protection

```go
// Protected route
mux.HandleFunc("/", app.requireAuth(app.handleHome))

// Access user in handler
user := getUserFromContext(r.Context())
```

## API Endpoints

### Public
- `GET /health` - Health check
- `GET /auth/login` - Initiate OIDC login
- `GET /auth/callback` - OIDC callback
- `GET /auth/logout` - Logout

### Protected (Auth Required)
- `GET /` - Home page
- `GET /api/worker-status/{user_id}` - HTMX worker status
- `GET /api/servers/{user_id}` - HTMX server list

## HTMX Features

Dynamic updates without JavaScript:

- **Worker Status**: Auto-refresh every 10s
- **Server List**: Auto-refresh every 15s
- **Loading Indicators**: Visual feedback
- **Declarative**: All via HTML attributes

Example:
```html
<span hx-get="/api/worker-status/123" 
      hx-trigger="load, every 10s"
      hx-indicator=".refresh-indicator">
    Content
</span>
```

## Dependencies

Go dependencies managed in `MODULE.bazel`:

```starlark
bazel_dep(name = "com_github_coreos_go_oidc_v3", version = "3.9.0")
bazel_dep(name = "com_github_gorilla_sessions", version = "1.2.1")
bazel_dep(name = "org_golang_x_oauth2", version = "0.16.0")
```

## Experience API Integration

### Current Status

The Experience API Go client generation is configured in `//generated/go/manman:experience_api` but not yet implemented in the handlers. 

### TODO

Update `api_client.go` to use the generated client:

```go
import (
    "github.com/whale-net/everything/generated/go/manman/experience_api"
)

func (app *App) getActiveWorkerID(ctx context.Context, userID string) (string, error) {
    client := experience_api.NewAPIClient(&experience_api.Configuration{
        BasePath: app.config.ExperienceAPIURL,
    })
    
    resp, _, err := client.DefaultApi.GetActiveWorkerIdApiV1ActiveWorkerIdUserIdGet(ctx, userID)
    if err != nil {
        return "", err
    }
    
    return resp.WorkerId, nil
}
```

## Troubleshooting

### OIDC Errors

**Problem**: Authentication fails

**Solutions**:
1. Verify `OIDC_ISSUER` is correct
2. Check `OIDC_REDIRECT_URI` matches Keycloak config
3. Validate client secret
4. Check Keycloak logs

### Build Errors

**Problem**: Missing Go dependencies

**Solution**:
```bash
# Update Go dependencies
bazel run @rules_go//go mod tidy
```

### Session Issues

**Problem**: Users logged out unexpectedly

**Solutions**:
1. Ensure `SECRET_KEY` is consistent
2. Check session cookie settings
3. Verify HTTPS in production

## Future Enhancements

- [ ] Implement Experience API client integration
- [ ] Add server control actions (start/stop)
- [ ] Display detailed server metrics
- [ ] Add user management interface
- [ ] Implement WebSocket for real-time updates
- [ ] Add audit logging
- [ ] Support multi-worker environments
- [ ] Dashboard with visualizations

## Project Structure

```
management-ui/
├── BUILD.bazel       # Bazel build config
├── README.md         # This file
├── main.go           # Application entry point
├── session.go        # Session management
├── handlers.go       # HTTP handlers
├── api_client.go     # Experience API client
├── templates.go      # HTML templates
├── main_test.go      # Tests
└── templates/        # (Future: external templates)
```

## Development Tips

### Adding New Pages

1. Add route in `setupRoutes()`
2. Create handler function
3. Add template in `templates.go`
4. Use `requireAuth` middleware for protected routes

### HTMX Patterns

For dynamic updates:
```go
func (app *App) handleDynamicContent(w http.ResponseWriter, r *http.Request) {
    // Return HTML fragment
    fmt.Fprintf(w, `<div>%s</div>`, content)
}
```

In template:
```html
<div hx-get="/api/dynamic" hx-trigger="every 5s">
    Initial content
</div>
```

## License

Part of the Everything monorepo.
