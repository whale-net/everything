# htmxauth - Reusable HTMX Authentication Library

A Go library providing flexible authentication for HTMX-based applications with support for both development (no-auth) and production (OIDC) modes.

## Features

- **Dual Auth Modes**: Switch between no-auth (development) and OIDC (production)
- **OIDC Integration**: Full OpenID Connect support via Keycloak or other providers
- **Session Management**: Secure cookie-based sessions with `gorilla/sessions`
- **Middleware Pattern**: Easy integration with standard `http.Handler`
- **Context-based User Access**: Retrieve user info from request context
- **CSRF Protection**: OAuth2 state parameter validation

## Installation

```starlark
# In your BUILD.bazel
go_library(
    name = "myapp",
    deps = [
        "//libs/go/htmxauth",
    ],
)
```

## Quick Start

### No-Auth Mode (Development)

```go
package main

import (
    "context"
    "net/http"
    
    "github.com/whale-net/everything/libs/go/htmxauth"
)

func main() {
    // Configure no-auth mode
    config := htmxauth.Config{
        Mode:          htmxauth.AuthModeNone,
        SessionSecret: "dev-secret",
    }
    
    auth, _ := htmxauth.NewAuthenticator(context.Background(), config)
    
    // Setup routes
    mux := http.NewServeMux()
    mux.HandleFunc("/auth/login", auth.HandleLogin)
    mux.HandleFunc("/auth/logout", auth.HandleLogout)
    mux.HandleFunc("/", auth.RequireAuthFunc(homeHandler))
    
    http.ListenAndServe(":8000", mux)
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
    user := htmxauth.GetUser(r.Context())
    // user.Sub == "dev-user"
    // user.PreferredUsername == "developer"
}
```

### OIDC Mode (Production)

```go
config := htmxauth.Config{
    Mode:             htmxauth.AuthModeOIDC,
    SessionSecret:    os.Getenv("SECRET_KEY"),
    OIDCIssuer:       os.Getenv("OIDC_ISSUER"),
    OIDCClientID:     os.Getenv("OIDC_CLIENT_ID"),
    OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
    OIDCRedirectURL:  os.Getenv("OIDC_REDIRECT_URI"),
}

auth, err := htmxauth.NewAuthenticator(context.Background(), config)
if err != nil {
    log.Fatal(err)
}

// Setup routes
mux := http.NewServeMux()
mux.HandleFunc("/auth/login", auth.HandleLogin)
mux.HandleFunc("/auth/callback", auth.HandleCallback)
mux.HandleFunc("/auth/logout", auth.HandleLogout)

// Protected routes
mux.HandleFunc("/", auth.RequireAuthFunc(homeHandler))
```

## Configuration

### Config Struct

```go
type Config struct {
    // Auth mode: "none" or "oidc"
    Mode AuthMode
    
    // Session configuration
    SessionSecret string // Required
    SessionName   string // Optional, defaults to "htmx_session"
    
    // OIDC configuration (required if Mode == AuthModeOIDC)
    OIDCIssuer       string
    OIDCClientID     string
    OIDCClientSecret string
    OIDCRedirectURL  string
    OIDCScopes       []string // Optional, defaults to ["openid", "profile", "email"]
}
```

### Environment Variables Pattern

```bash
# Auth mode
AUTH_MODE=none              # or "oidc"

# Session
SECRET_KEY=your-secret-key

# OIDC (only for oidc mode)
OIDC_ISSUER=https://keycloak.example.com/realms/myrealm
OIDC_CLIENT_ID=my-app
OIDC_CLIENT_SECRET=secret
OIDC_REDIRECT_URI=https://myapp.example.com/auth/callback
```

## API Reference

### Functions

#### `NewAuthenticator(ctx context.Context, config Config) (*Authenticator, error)`

Creates a new authenticator instance. Initializes OIDC provider if in OIDC mode.

#### `RequireAuth(next http.Handler) http.Handler`

Middleware that requires authentication. In no-auth mode, provides a default dev user. In OIDC mode, redirects to login if not authenticated.

#### `RequireAuthFunc(next http.HandlerFunc) http.HandlerFunc`

Convenience wrapper for RequireAuth that works with HandlerFunc.

#### `GetUser(ctx context.Context) *UserInfo`

Retrieves the authenticated user from the request context.

#### `HandleLogin(w http.ResponseWriter, r *http.Request)`

Handles login requests. In OIDC mode, initiates OAuth2 flow. In no-auth mode, redirects to home.

#### `HandleCallback(w http.ResponseWriter, r *http.Request)`

Handles OIDC callback after successful authentication. Only available in OIDC mode.

#### `HandleLogout(w http.ResponseWriter, r *http.Request)`

Handles logout requests. Clears session in OIDC mode, redirects to home in no-auth mode.

### Types

#### `UserInfo`

```go
type UserInfo struct {
    Sub               string                 // Subject (user ID)
    PreferredUsername string                 // Username
    Name              string                 // Display name
    Email             string                 // Email address
    RawClaims         map[string]interface{} // All OIDC claims
}
```

In no-auth mode, defaults to:
- Sub: `"dev-user"`
- PreferredUsername: `"developer"`
- Name: `"Development User"`
- Email: `"dev@localhost"`

## Usage Patterns

### Protecting Routes

```go
// Single route
mux.HandleFunc("/admin", auth.RequireAuthFunc(adminHandler))

// Multiple routes with a sub-router
adminMux := http.NewServeMux()
adminMux.HandleFunc("/users", usersHandler)
adminMux.HandleFunc("/settings", settingsHandler)
mux.Handle("/admin/", http.StripPrefix("/admin", auth.RequireAuth(adminMux)))
```

### Accessing User Info

```go
func handler(w http.ResponseWriter, r *http.Request) {
    user := htmxauth.GetUser(r.Context())
    if user == nil {
        http.Error(w, "Unauthorized", http.StatusUnauthorized)
        return
    }
    
    fmt.Fprintf(w, "Hello, %s!", user.Name)
}
```

### HTMX Integration

```html
<div class="user-info">
    <span>{{.User.Name}}</span>
    <a href="/auth/logout" class="btn">Logout</a>
</div>

<div hx-get="/api/data" hx-trigger="load">
    <!-- HTMX will automatically include session cookie -->
</div>
```

## Testing

### Unit Tests

```go
func TestHandler(t *testing.T) {
    config := htmxauth.Config{
        Mode:          htmxauth.AuthModeNone,
        SessionSecret: "test",
    }
    auth, _ := htmxauth.NewAuthenticator(nil, config)
    
    handler := auth.RequireAuth(http.HandlerFunc(yourHandler))
    
    req := httptest.NewRequest("GET", "/", nil)
    w := httptest.NewRecorder()
    handler.ServeHTTP(w, req)
    
    assert.Equal(t, http.StatusOK, w.Code)
}
```

### Integration Tests

Use no-auth mode for integration tests to avoid OIDC setup:

```go
func TestMain(m *testing.M) {
    os.Setenv("AUTH_MODE", "none")
    os.Setenv("SECRET_KEY", "test-secret")
    os.Exit(m.Run())
}
```

## Keycloak Setup

### Client Configuration

1. **Clients** → **Create**
2. Configure:
   - Client ID: Your app name
   - Client Protocol: `openid-connect`
   - Access Type: `confidential`
   - Valid Redirect URIs: `https://yourapp.com/auth/callback`
   - Web Origins: `https://yourapp.com`

3. **Credentials** tab → Copy Client Secret

### Required Scopes

Default scopes (automatically requested):
- `openid` - Required for OIDC
- `profile` - User profile info
- `email` - User email

## Security Considerations

### Production Checklist

- [ ] Use OIDC mode in production
- [ ] Set `Secure: true` in session options (requires HTTPS)
- [ ] Use strong `SessionSecret` (32+ random bytes)
- [ ] Set appropriate session `MaxAge`
- [ ] Enable HTTPS/TLS
- [ ] Validate redirect URLs
- [ ] Monitor session activity
- [ ] Rotate secrets periodically

### No-Auth Mode Warning

⚠️ **Never use no-auth mode in production!** It bypasses all authentication and provides a default user to all requests. Only use for:
- Local development
- Integration tests
- Demos/prototypes

## Examples

See the management-ui app for a complete example:
- `//manman/management-ui` - Full HTMX application using htmxauth

## License

Part of the Everything monorepo.
