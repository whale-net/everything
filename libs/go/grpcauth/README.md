# grpcauth

Go library for gRPC authentication/authorization in the manmanv2 platform. Provides server-side JWT interceptors and client-side credential helpers, with a dev mode that requires no Keycloak.

## Auth Modes

| Mode | Server behavior | Client behavior |
|------|-----------------|-----------------|
| `none` (default) | Injects fake `Claims{Subject: "dev-user", Roles: ["admin"]}` — no token required | Sends no credentials |
| `oidc` | Validates `Authorization: Bearer <token>` via local JWKS; returns `codes.Unauthenticated` on failure | Fetches/refreshes token automatically |

Set `GRPC_AUTH_MODE` consistently across all components. A mismatch (e.g. server=oidc, client=none) causes `codes.Unauthenticated` on every call.

**Setting up the Keycloak side — see [KEYCLOAK.md](KEYCLOAK.md).** Step-by-step
realm/client/role configuration, the reference pattern for service-to-service
auth in this repo, and the two gotchas that break every first attempt (roles must
be *realm* roles; you must add an audience mapper).

## Usage

### Server — add interceptors

```go
unaryInt, streamInt, err := grpcauth.NewServerInterceptors(ctx, grpcauth.ServerConfig{
    Mode:      grpcauth.AuthMode(os.Getenv("GRPC_AUTH_MODE")), // "none" or "oidc"
    IssuerURL: os.Getenv("GRPC_OIDC_ISSUER"),                  // required for oidc
    ClientID:  os.Getenv("GRPC_OIDC_CLIENT_ID"),               // expected audience
})
if err != nil {
    log.Fatalf("grpcauth: %v", err)
}

server := grpc.NewServer(
    grpc.ChainUnaryInterceptor(unaryInt),
    grpc.ChainStreamInterceptor(streamInt),
)
```

Reading claims inside a handler:
```go
claims, ok := grpcauth.ClaimsFromContext(ctx)
if ok {
    log.Printf("request from %s", claims.Subject)
}
```

If your service uses service-prefixed role names (e.g. `app-registry-builder`
rather than plain `admin`), set `ServerConfig.DevRoles` to the full list of
roles your handlers check — otherwise `AuthModeNone`'s fake claims (which
default to `Roles: ["admin"]`) satisfy none of them, and local/Tilt
development breaks silently. See `tools/app_registry/server/main.go` for a
worked example.

Testing handlers directly (bypassing the interceptor) against injected
claims:
```go
ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{
    Subject: "test-user",
    Roles:   []string{"app-registry-builder"},
})
```

### Client — service account (Host, Log-Processor → API)

Machine-to-machine: fetches a client credentials token once and auto-refreshes it.

```go
authOpt, err := grpcauth.NewServiceAccountDialOption(grpcauth.ClientConfig{
    Mode:                     grpcauth.AuthMode(os.Getenv("GRPC_AUTH_MODE")),
    TokenURL:                 os.Getenv("GRPC_AUTH_TOKEN_URL"),
    ClientID:                 os.Getenv("GRPC_AUTH_CLIENT_ID"),
    ClientSecret:             os.Getenv("GRPC_AUTH_CLIENT_SECRET"),
    RequireTransportSecurity: false, // internal cluster; set true if using TLS
})
if err != nil {
    log.Fatalf("grpcauth: %v", err)
}

conn, err := grpc.NewClient(addr,
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    authOpt,
)
```

### Client — per-request user token (UI → API / UI → Log-Processor)

Reads the user's access token from context on each call.

```go
// At startup — create the dial option once
userAuthOpt := grpcauth.NewUserTokenDialOption(grpcauth.AuthMode(os.Getenv("GRPC_AUTH_MODE")))

conn, err := grpc.NewClient(addr,
    grpc.WithTransportCredentials(insecure.NewCredentials()),
    userAuthOpt,
)

// Per HTTP request — inject the token into the context before the gRPC call
ctx = grpcauth.WithUserToken(r.Context(), accessToken)
resp, err := client.SomeRPC(ctx, req)
```

## Environment Variables

### Server side

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_OIDC_ISSUER` | `""` | Keycloak realm URL (required for `oidc`) |
| `GRPC_OIDC_CLIENT_ID` | `""` | Expected audience in the JWT (required for `oidc`) |

### Client side (service account)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |
| `GRPC_AUTH_TOKEN_URL` | `""` | Keycloak token endpoint (required for `oidc`) |
| `GRPC_AUTH_CLIENT_ID` | `""` | Service account client ID |
| `GRPC_AUTH_CLIENT_SECRET` | `""` | Service account client secret |

### Client side (user token forwarding, UI only)

| Variable | Default | Description |
|----------|---------|-------------|
| `GRPC_AUTH_MODE` | `none` | `none` or `oidc` |

## BUILD.bazel

```bazel
deps = [
    "//libs/go/grpcauth",
    ...
]
```

## Types

```go
type AuthMode string
const (
    AuthModeNone AuthMode = "none"
    AuthModeOIDC AuthMode = "oidc"
)

type Claims struct {
    Subject  string
    Roles    []string
    Audience []string
}

type ServerConfig struct {
    Mode      AuthMode
    IssuerURL string
    ClientID  string
    DevRoles  []string // AuthModeNone only; defaults to ["admin"]
}

type ClientConfig struct {
    Mode                     AuthMode
    TokenURL                 string
    ClientID                 string
    ClientSecret             string
    RequireTransportSecurity bool
}
```
