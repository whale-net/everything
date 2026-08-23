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

## DB-Backed Sessions

By default, sessions are stored in an encrypted gorilla/sessions cookie. This works well for pure HTTP auth, but has a limitation when the UI also makes gRPC calls on behalf of the user: the access token stored in the cookie expires (typically 5–15 min), while the session cookie itself remains valid (24 h). After token expiry, gRPC calls fail while the UI still appears logged in.

### DB session manager

`DBSessionManager` stores session state in PostgreSQL and automatically refreshes the access token when it is close to expiry:

- Only a session ID cookie is sent to the browser (no token in cookie).
- Refresh tokens are AES-256-GCM encrypted at rest. The encryption key is derived from `SECRET_KEY` via SHA-256.
- Concurrent requests trigger a single refresh — `SELECT FOR UPDATE` prevents double-refresh races.
- Stale sessions are cleaned up hourly.

### Setup

1. Apply migration `032_ui_sessions` to create the `ui_sessions` table.
2. Set `PG_DATABASE_URL`.
3. Use `NewAuthenticatorWithDB` instead of `NewAuthenticator`:

```go
pool, err := db.NewPool(ctx, "")  // reads PG_DATABASE_URL
if err != nil {
    log.Fatal(err)
}

dbSessions, err := htmxauth.NewDBSessionManager(ctx, pool, os.Getenv("SECRET_KEY"), "manmanv2-ui")
if err != nil {
    log.Fatal(err)
}

auth, err := htmxauth.NewAuthenticatorWithDB(ctx, htmxauth.Config{
    Mode:             htmxauth.AuthModeOIDC,
    SessionSecret:    os.Getenv("SECRET_KEY"),
    OIDCIssuer:       os.Getenv("OIDC_ISSUER"),
    OIDCClientID:     os.Getenv("OIDC_CLIENT_ID"),
    OIDCClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
    OIDCRedirectURL:  os.Getenv("OIDC_REDIRECT_URI"),
}, dbSessions)
```

### GetAccessToken

`GetAccessToken` retrieves the user's access token from the session. In DB mode it auto-refreshes if expiry is within 2 minutes. In dev mode (`AuthModeNone`) it returns the string `"dev-token"`.

```go
token, err := auth.GetAccessToken(r)
if err != nil {
    // user not authenticated, or cookie mode with expired token
}
```

Use this to forward the token to gRPC calls via `grpcauth.WithUserToken`.

---

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

## Convergence spike (#998, FR9)

Findings from the FR9 convergence spike (issue #1013), which investigated
three pinned divergence points between `manmanv2/ui` and
`tools/app_registry/ui`. Outcome: **partial convergence (FR9b)** — one point
converged cleanly and was hoisted here; the other two do not converge into
shared code, for reasons recorded below. No follow-up task issues were filed
per the spike's scope.

### Point 1 — `withAccessToken` (converged, FR9a)

Confirmed byte-for-byte identical in both apps. Hoisted as
`Authenticator.WithAccessToken` in this package (`auth.go`); both apps'
`main.go` now call `app.auth.WithAccessToken(...)` and no longer define a
local copy. This is a pure code move — behavior (redirect to
`/auth/login?next=...`, `HX-Redirect` + 401 for HTMX partial requests,
`grpcauth.WithUserToken` context injection) is unchanged (NFR3). Unit tests
covering the pass-through, plain-redirect, and HX-Redirect paths live in
`auth_test.go`.

This added a `libs/go/grpcauth` dependency to `libs/go/htmxauth` (no cycle:
`grpcauth` does not import `htmxauth`).

### Point 2 — session-store bootstrap policy (decision: app-owned, not converged)

manmanv2's UI falls back to cookie-only sessions when `DATABASE_URL` is
unset; app-registry's UI hard-fails at boot instead. **Decision: this stays
app-owned policy, not a shared `htmxauth.Config` option.**

Reasoning:

- The two behaviors reflect genuinely different, deliberate operational
  contracts, not an accidental drift. app-registry's UI hard-fails *by
  design* (see the comment in `tools/app_registry/ui/main.go`'s `NewApp`,
  tied to FR-58) because it must never silently degrade to a session store
  that can't refresh access tokens. manmanv2's cookie fallback is an
  intentional lightweight-deployment affordance.
- The actual duplicated logic is a single `if config.DatabaseURL == ""`
  branch per app (a handful of lines) — not enough surface to justify a new
  `htmxauth.Config` field, a `BootstrapPolicy` enum, or plumbing pool
  construction through the library. The building blocks the branch chooses
  between (`NewAuthenticator` for cookie sessions, `db.NewPool` +
  `NewDBSessionManager` + `NewAuthenticatorWithDB` for DB sessions) are
  already shared; only the choice of which to call is app-specific, and that
  choice is exactly the kind of policy an app should own.
- Forcing this into a shared config option would add an abstraction layer
  (a policy enum threaded through a constructor, plus a way to hand the
  library a pool or a pool-constructor callback) to remove less code than
  the abstraction itself would add.

If a third app ever needs this same choice, revisit — but two data points
with a one-line-each divergence, both already using shared building blocks,
is not enough justification for a new config knob today.

### Point 3 — identity shaping behind the top-right rendering divergence (no backend divergence found)

Investigated: manmanv2's top-right renders `data.User.Name`
(`manmanv2/ui/components/layout.templ`); app-registry's renders
`user.PreferredUsername` (`tools/app_registry/ui/components/layout.templ`).

Finding: **there is no backend data-shaping divergence to converge.** Both
apps populate `htmxauth.UserInfo` identically — same struct, same fields
(`Sub`, `PreferredUsername`, `Name`, `Email`, `Roles`), same population path
through `SessionManager.SetUserInfo`/`GetUserInfo` (cookie-backed) and
`DBSessionManager.SetUserInfo`/`GetUserInfo` (DB-backed) in this package. The
divergence is entirely in which field each app's `.templ` template chooses
to display — a rendering choice, not a backend shaping difference. Nothing
in `libs/go/htmxauth` needed to change for this point; whether the two
templates should standardize on the same field is FR8's rendering-layer
scope (see #1010/#1011), not this issue's.

### NFR6 gate

This spike changed `libs/go/htmxauth` (point 1), so the mandatory
integration-test gate applies. Run and evidence posted on issue #1013:

```sh
bazel test //libs/go/htmxauth:htmxauth_integration_test --test_output=all
```

## License

Part of the Everything monorepo.
