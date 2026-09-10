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

### Client — delegated grant (per-user, non-interactive later)

A one-time browser authorization-code + PKCE consent flow (`offline_access`)
that persists a real Keycloak refresh token server-side, keyed to
`(subject, grant)`, and exposes it as a non-interactive accessor callable
with no user and no browser present — e.g. from a Temporal Activity at
schedule-fire time. See [KEYCLOAK.md § 11](KEYCLOAK.md#11-delegated-grants-offline_access-browser-consent-for-a-client-that-acts-later)
for the Keycloak-side client setup this requires (confidential client, own
redirect URI per consuming domain, `offline_access` scope, rotation
enabled).

**Wiring it up:**

```go
grantSource, err := grpcauth.NewDelegatedGrantSource(ctx, grpcauth.DelegatedGrantConfig{
    Issuer:        os.Getenv("GRPC_OIDC_ISSUER"),          // realm issuer; discovers endpoints
    ClientID:      os.Getenv("GRANT_CLIENT_ID"),            // this domain's own Keycloak client
    ClientSecret:  os.Getenv("GRANT_CLIENT_SECRET"),
    RedirectURI:   "https://myapp.example.com/oauth/callback",
    Store:         store,                                   // see "Storage" below
    EncryptionKey: encryptionKey,                            // see "Encryption key" below
})
```

The consent leg — driven from your own web handler, not owned by `grpcauth`:

```go
// GET /oauth/authorize?subject=<user>&grant=<schedule-id>
authURL, pending, err := grantSource.BeginAuthorization(ctx, subject, grant)
// Persist `pending` against the browser session (cookie, server-side
// session store) and redirect the browser to authURL.

// GET /oauth/callback?state=...&code=...
err = grantSource.CompleteAuthorization(ctx, pending, r.URL.Query().Get("state"), r.URL.Query().Get("code"))
// On success, (subject, grant) now has a usable, persisted grant. Re-running
// this for a grant already in needs_reauth is the re-authorization path
// (FR11) — it reuses the same (subject, grant) key rather than minting a
// new grant identity.
```

The non-interactive accessor — the one a Temporal Activity calls at fire
time, with no browser and no user present:

```go
token, err := grantSource.TokenSource(subject, grant).Token(ctx)
```

See "A Temporal Activity calling this" below for the full pattern, including
the `Status` check that should happen before firing.

**Storage — the `grpcauth/pgstore` reference implementation.** Core
`//libs/go/grpcauth` deliberately carries no pgx/Postgres dependency, so
existing consumers of the package (`leaflab`, `manmanv2`,
`tools/app_registry`, `whagent_net`, and the rest of `grpcauth`'s ~21
existing consumer targets) are unaffected by this credential source. A
consumer that wants the pgx-backed reference `Store` depends on the separate
`//libs/go/grpcauth/pgstore` `go_library` target instead:

```bazel
deps = [
    "//libs/go/grpcauth",
    "//libs/go/grpcauth/pgstore",
    ...
]
```

```go
store, err := pgstore.NewGrantStore(ctx, pgstore.StoreConfig{
    Pool:          pool,                 // *pgxpool.Pool
    EncryptionKey: encryptionKey,        // required — see "Encryption key" below
    Revoker:       grantSource,          // optional: best-effort RFC 7009 remote revoke (FR13)
    // TableName, SubjectColumn, GrantColumn, MaterialColumn, StatusColumn
    // all default to grpcauth_delegated_grant / subject / grant_key /
    // token_material / status; override if your migration names them
    // differently.
})
```

**The SQL migration is domain-owned — no migration ships with this
library**, exactly like `libs/go/mcpauth`'s precedent
(`audience_score_system/migrate/schema/migrations/006_mcpauth_credential.up.sql`
plays the same role for `mcpauth`). Your migration must create a table
shaped like this (column/table names are configurable via `StoreConfig`;
the shape must match):

```sql
CREATE TABLE grpcauth_delegated_grant (
    subject        TEXT        NOT NULL,
    grant_key      TEXT        NOT NULL,
    token_material BYTEA       NOT NULL,
    status         TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (subject, grant_key)
);
```

This is a plain current-state table, not SCD2 (AGENTS.md § SCD2) — a
grant's history is its status transitions (`GrantStatus`), not versioned
rows.

**Encryption key.** `StoreConfig.EncryptionKey` (and, if you construct one
directly, `DelegatedGrantConfig.EncryptionKey`) is a 32-byte AES-256-GCM key
— the same shape as `audience_score_system`'s
`ASS_TOKEN_ENCRYPTION_KEY`-derived key (`audience_score_system/ENV.md`):
your domain supplies a secret via its own environment variable and derives
exactly `grpcauth.GrantKeySize` (32) raw bytes from it before passing it in
— `grpcauth` never reads an environment variable itself and never picks the
derivation for you. **NFR2 boundary, stated explicitly:** encryption is
**library-enforced** in the shipped `pgstore` reference implementation — it
calls the shared encrypt/decrypt helper on every `Persist` and every read of
token material, so your code never handles plaintext refresh-token bytes.
If you write your own alternative `Store` instead of using `pgstore`,
enforcing encryption-at-rest is entirely your implementation's own
responsibility — the `Store` interface itself has no way to compel it.

**A Temporal Activity calling this** — check `Status` before firing, fetch
the token only if the grant is usable, and let the caller's own
`RetryPolicy` handle transient failures (the accessor performs no internal
retry or backoff):

```go
func FireDelegatedRunActivity(ctx context.Context, subject, grant string) error {
    // FR10: query state without attempting a token fetch. Pausing or
    // skipping the actual Temporal Schedule based on this is this
    // consuming domain's own responsibility -- this primitive only
    // supplies the signal.
    status, err := grantSource.Status(ctx, subject, grant)
    if err != nil {
        return fmt.Errorf("check grant status: %w", err)
    }
    if status == grpcauth.GrantStatusNeedsReauth || status == grpcauth.GrantStatusRevoked {
        return fmt.Errorf("grant %s/%s is %s: pause/skip this schedule until re-authorized", subject, grant, status)
    }

    // Call the accessor once per firing and reuse the token for every
    // downstream call this run makes -- not once per downstream call.
    token, err := grantSource.TokenSource(subject, grant).Token(ctx)
    if err != nil {
        if grpcauth.IsTransient(err) {
            // FR9: network/5xx blip, grant is fine -- return the error and
            // let the Activity's RetryPolicy retry. Do not retry in here.
            return err
        }
        if errors.Is(err, grpcauth.ErrGrantNeedsReauth) || errors.Is(err, grpcauth.ErrGrantRevoked) {
            // FR8: the grant became unusable between the Status check above
            // and this call -- stop and surface it, do not retry.
            return fmt.Errorf("grant unusable, pause/skip: %w", err)
        }
        return fmt.Errorf("unexpected delegated-grant error: %w", err)
    }

    return callDownstreamAPIsAsGrantor(ctx, token)
}
```

**Interim admin-revoke procedure (FR6).** Until a consuming domain builds
its own admin API/UI, an operator with backend access to that domain's own
schedule/grant table (which already maps owner → grant) and to the
`grpcauth/pgstore`-backed database can invoke `store.Revoke(ctx, subject,
grant)` directly — e.g. from a short internal script or admin CLI, **not** a
UI this library ships — to stop one grant without touching Keycloak or the
rest of that user's access. Two gaps are accepted here, deliberately, rather
than left silent:

- **No admin-facing authentication/authorization surface.** Nothing here
  decides *who* is allowed to call `Revoke` for a subject other than
  themselves — that check belongs to whatever caller invokes the `Store` (a
  future admin API/UI, once one exists).
- **No actor attribution.** The `Store` records only that a grant
  transitioned to `revoked`, not who called `Revoke`. Self-service revoke
  has an unambiguous actor (the subject itself); an admin-invoked revoke
  does not. A future admin UI built on this must add its own audit log
  around the call — `grpcauth` does not retrofit one here.

**Call-frequency expectation.** Call the accessor once per schedule firing
and reuse the resulting access token for that run's downstream calls, not
once per downstream call (see the sample above). With rotation enabled,
calling more often than necessary means unnecessary `Persist`/rotation
churn and unnecessary surface for a concurrent-refresh race.

**No "list grants for a subject" surface — intentionally.** `Store` is
keyed lookup only (`Status(subject, grant)`, `Revoke(subject, grant)`,
etc.). A consuming domain's own schedule table already maps owner → grant,
so enumerating a subject's grants — for self-service or admin use — is that
domain's job, not `grpcauth`'s.

**`needs_reauth` detection is reactive.** It surfaces only on the next fire
or `Status` check, not the instant the underlying condition changes — a
revoked grant or a Keycloak-disabled user can lag "confirmed stopped" from
this primitive's point of view by up to one schedule interval. An operator
or consuming domain must not assume a Keycloak-side disablement or a revoke
takes effect instantly *as observed here*; the underlying Keycloak/local
state itself changes immediately, only the observation lags.

**The `needs_reauth` folding callout.** Folding a self-revoked grant and a
Keycloak-disabled user into one `needs_reauth` state is a deliberate
simplification. A future consuming-domain notification/admin UI built on
top of `Status` must **not** assume re-consent (repeating the browser
authorization flow) is always the fix — a disabled user is not
grantor-fixable by re-authorizing; only a self-revoked grant is.

**Forward-looking consent-screen guidance.** A future consuming domain's own
consent screen (not delivered by this library) should disclose to the
grantor, in plain language, that the grant is **indefinite** and
**inherits the grantor's full current role set**, not a scoped-down subset —
Keycloak's own `offline_access` consent screen does not say either of these
on its own. Recorded here since this library's docs are the only place the
underlying mechanism is explained.

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
