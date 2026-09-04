# mcpauth — MCP OAuth2 authorization-server front end + credential lifecycle store

A Go library providing an OAuth2 authorization-server front end for MCP
(Model Context Protocol) servers — RFC 9728/8414 discovery metadata, RFC
7591 dynamic client registration (this doc), and (via #1642)
authorization-code + PKCE `/authorize`/`/token` endpoints — together with
the credential lifecycle (mint / verify / revoke / list) for the opaque
bearer credential that front end issues.

## What this is (and isn't)

mcpauth runs its own OAuth2 authorization server; it does not, however,
verify a caller's identity against an *external* identity provider
(Keycloak, Google OIDC, or any other obtain-step IdP) itself. By the time
its `/authorize` endpoint runs, the consuming domain's own sign-in flow has
already established who the caller is (a session cookie, a trusted proxy
header, ...); `CallerResolver` (see `resolver.go`) just reads that
already-established identity back off the request. The credential this
library then mints is a long-lived, database-backed, individually
revocable bearer token an MCP client presents on every subsequent call.
See `mcpauth.go`'s package doc for the full "what this is not" boundary
(no external-IdP verification, no access/refresh-token lifecycle).

This package has **zero domain-specific types or imports** (NFR2) —
`Identity` is a plain `string`. The worked precedent this behavior was
lifted from is `audience_score_system/store/credential.go`
(`CredentialStore` over `mcp_credential`, migration 005); read that file for
the exact behavioral bar this package reproduces.

## Schema ownership — the consuming domain owns the migration

This library does **not** own, embed, or run any migration — the consuming
domain's own migration tooling must create the table before the first call
to `NewCredentialStore` (unlike `libs/go/htmxauth`'s `ui_sessions`, whose
one shared shape the library now bundles and a domain merges in via
`migrate.WithSource` — mcpauth's schema varies per domain, so there is no
single shape to bundle). It does still mirror `DBSessionManager`'s
boot-time preflight behavior (see `db_session.go`'s `probeSessionTable`):
it

- names an **unqualified** table (`StoreConfig.TableName`, default
  `mcp_credential`) so every query — including the boot-time preflight
  probe — resolves through the same `search_path` the runtime uses;
- probes that table at construction time and fails loudly, naming the
  table, if it is missing.

### Schema contract

A consuming migration must create exactly this shape. The identity column's
name and type are configurable (`StoreConfig.IdentityColumn`,
`StoreConfig.IdentityCast`); everything else must match.

```sql
-- Generic consumer (identity is an opaque string, e.g. a service-account name):
CREATE TABLE mcp_credential (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    identity     TEXT        NOT NULL,
    token_hash   TEXT        NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX mcp_credential_token_hash ON mcp_credential(token_hash) WHERE revoked_at IS NULL;
CREATE INDEX mcp_credential_identity ON mcp_credential(identity);
```

```sql
-- ASS-shaped consumer (identity is a Person UUID foreign key):
CREATE TABLE mcp_credential (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    person_id    UUID        NOT NULL REFERENCES person(id),
    token_hash   TEXT        NOT NULL UNIQUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ
);
CREATE INDEX mcp_credential_token_hash ON mcp_credential(token_hash) WHERE revoked_at IS NULL;
CREATE INDEX mcp_credential_person_id ON mcp_credential(person_id);
```

The ASS consumer sets `StoreConfig{TableName: "mcp_credential", IdentityColumn: "person_id"}`.

**Not SCD2.** The lifecycle here is mint-then-revoke — a one-way
`revoked_at` soft close, not a dimension whose value changes over time and
needs history. Do **not** add `valid_from`/`valid_to` to this table; if a
future reviewer suggests it per the repo's SCD2 convention (see
`AGENTS.md`), point them at this paragraph — SCD2 answers "what was this
row's value at time T", which is not a question this table needs to answer
(a credential has exactly one terminal state transition: live → revoked).

## Identity column and casting

`Verify`/`Revoke`/`List` take a plain Go `string` identity, but a consuming
domain's identity column may be a non-text PostgreSQL type (ASS's
`person_id` is `UUID`). `StoreConfig.IdentityCast` lets that domain tell
`mcpauth` to emit `<IdentityColumn> = $N::<IdentityCast>` instead of
`<IdentityColumn> = $N` in generated SQL.

**Resolved: is the cast actually required?** No — verified directly against
a real Postgres (`postgres:16-alpine`, via a throwaway pgxpool spike
mirroring `//libs/go/dbtest`'s container setup) that pgx v5's default
(extended/prepared-statement) query protocol:

- encodes a Go `string` parameter against a `uuid` column with **no**
  explicit cast, for `INSERT`, `SELECT ... WHERE`, and
  `UPDATE ... RETURNING`;
- scans a `uuid` column into a Go `string` destination with **no** explicit
  cast.

All four cases returned correct results with no error. `IdentityCast`
therefore stays in `StoreConfig` as an **optional** escape hatch — for
identity columns whose type pgx cannot infer from query context alone (a
custom domain/enum type, for example), and as cheap insurance against a
future pgx or Postgres version silently regressing the implicit-cast
behavior — rather than as a requirement every UUID-keyed consumer must set.
ASS's task in this plan may still set it explicitly for clarity; that is a
style choice, not a functional requirement.

## Identifier validation

`TableName`, `IdentityColumn`, and `IdentityCast` are interpolated directly
into generated SQL — they cannot be bound query parameters — so
`NewCredentialStore` validates each one against a strict identifier regex
(`^[a-z_][a-z0-9_]*$`) before building any query, rejecting anything else
with a clear construction-time error. This is a hard requirement (SQL
injection surface), not a style nicety.

## OAuth2 client registry — memory vs. Postgres, and its schema contract

`Provider`'s RFC 7591 dynamic client registration (`POST /register`, see
`clients.go`) is backed by a pluggable `ClientRegistry`, exactly parallel
to `CredentialStore` above:

- `NewMemoryClientRegistry()` — an in-process map. This is
  `ProviderConfig.Clients`'s **default** because it is enough for a
  single-replica server.
- `NewPostgresClientRegistry(ctx, ClientRegistryConfig)` — backed by
  Postgres, safe across replicas.

**A multi-replica deployment MUST use `NewPostgresClientRegistry`, not the
memory default.** Each process's memory registry is its own isolated map:
a client that dynamically registers against replica A is completely
unknown to replica B, so the next request that lands on B (a load
balancer gives no affinity guarantee) fails as if the client had never
registered at all. There is no way to make the memory registry safe for
more than one replica — this is not a tuning knob, it is a hard
single-replica constraint.

### Schema contract

Exactly like `mcp_credential` above, `mcpauth` does **not** own, embed, or
run this migration (NFR5) — the consuming domain's own migration tooling
must create this table before the first call to
`NewPostgresClientRegistry`, in the same domain-owned-migration style as
`mcp_credential`:

```sql
CREATE TABLE mcp_oauth_client (
    client_id  TEXT        PRIMARY KEY,
    metadata   JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

`ClientRegistryConfig.TableName` defaults to `mcp_oauth_client`; like
`StoreConfig.TableName`, it names an **unqualified** table so the
boot-time preflight probe resolves through the same `search_path` the
runtime uses.

### Registration is open

`POST /register` is intentionally **unauthenticated** — this is what RFC
7591 dynamic client registration requires and what every MCP client (Claude
Desktop, GitHub Copilot, opencode) expects to be able to call before it has
any credential at all (see the Context section this issue was scoped from).
Consequently, a client registration by itself confers **no access**: it
only mints a `client_id` (and validates/echoes the client's redirect URIs)
so the client can go on to run `/authorize` — access still requires a
signed-in caller completing that flow (#1642). Do not treat "has a
registered `client_id`" as any kind of authorization signal.

`/register` validates every submitted `redirect_uri` is either `https`,
`http` on loopback (native/CLI MCP clients that run a local callback
listener, e.g. `http://127.0.0.1:PORT/callback`), or a private-use custom
scheme (native app deep links, e.g. `com.example.app:/callback`); it
rejects `javascript:`/`data:`/`vbscript:` outright, since those could turn
`/authorize`'s eventual redirect into script execution rather than a
navigation. On success it mints a random `client_id`, issues no
`client_secret` (mcpauth only registers public PKCE clients — see the
authorization-server metadata's `token_endpoint_auth_methods_supported:
["none"]`), and returns `201` with an RFC 7591
`ClientRegistrationResponse`. Bad input gets `400` with an RFC 7591 error
body (`{"error": "invalid_redirect_uri" | "invalid_client_metadata"}`) —
never a raw Go error string.

## OAuth2 authorization-code + PKCE flow — memory vs. Postgres, and its schema contract

`Provider`'s `GET /authorize` (`authorize.go`) and `POST /token`
(`token.go`) implement the authorization-code + PKCE flow (RFC 6749 +
PKCE `S256` only — no `plain`, no device-code, no client-credentials).
Between those two requests, a pending authorization code is held in a
pluggable `AuthCodeStore`, exactly parallel to `ClientRegistry` above:

- `NewMemoryAuthCodeStore()` — an in-process map. This is
  `ProviderConfig.AuthCodes`'s **default** because it is enough for a
  single-replica server.
- `NewPostgresAuthCodeStore(ctx, AuthCodeStoreConfig)` — backed by
  Postgres, safe across replicas.

**A multi-replica deployment MUST use `NewPostgresAuthCodeStore`, not the
memory default.** `/authorize` and `/token` can land on different
replicas — the same warning as the OAuth2 client registry above, and for
the same reason: each process's memory store is its own isolated map.

Like the bearer credential (`mcp_credential`), an authorization code is
persisted only as a SHA-256 hash (NFR1) — a leaked table read must not
yield a redeemable code.

`ProviderConfig.AuthCodeTTL` controls how long a minted code stays
redeemable before `/token` rejects it as expired. Defaults to 60 seconds,
per OAuth 2.1 guidance that codes be short-lived.

### Schema contract

Exactly like `mcp_credential` and `mcp_oauth_client` above, `mcpauth` does
**not** own, embed, or run this migration (NFR5) — the consuming domain's
own migration tooling must create this table before the first call to
`NewPostgresAuthCodeStore`:

```sql
CREATE TABLE mcp_auth_code (
    code_hash             TEXT        PRIMARY KEY,
    client_id             TEXT        NOT NULL,
    redirect_uri          TEXT        NOT NULL,
    identity              TEXT        NOT NULL,
    code_challenge        TEXT        NOT NULL,
    code_challenge_method TEXT        NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX mcp_auth_code_expires_at ON mcp_auth_code(expires_at);
```

`AuthCodeStoreConfig.TableName` defaults to `mcp_auth_code`; like
`StoreConfig.TableName`, it names an **unqualified** table so the
boot-time preflight probe resolves through the same `search_path` the
runtime uses.

## Caller resolution and the sign-in redirect

`ProviderConfig.Resolver` (a `CallerResolver`, `resolver.go`) is how
`/authorize` learns who the caller already is — see that interface's doc
comment for the hard rule: it must only read an identity the consuming
domain's own sign-in flow already established (a session cookie, a trusted
proxy header, etc.), never perform a fresh IdP round trip itself.

When `Resolver.ResolveCaller` reports `ok == false`, `/authorize` redirects
to `ProviderConfig.SignInURL` (if set — otherwise it 401s) with the exact
original `/authorize` request, query string intact, carried in a query
parameter named by `ProviderConfig.SignInReturnParam` (defaults to
`"next"` — see that field's doc for why this defaults to ASS's own
`?next=` convention rather than an mcpauth-invented name). The consuming
domain's sign-in flow is expected to redirect back to that URL once the
caller is signed in, and `/authorize` re-runs from the top — this is a
plain redirect round trip, not a callback URL mcpauth calls itself.

## Split authorization-server / resource-server binaries

Some consumers run their OAuth2 authorization server and their MCP
resource server as two separate processes — e.g. because only one process
holds the caller's session cookie `Resolver` needs (see
`audience_score_system/web` vs. `audience_score_system/mcp`, issue #1646).
For that shape, the resource-server binary must NOT construct a full
`Provider`: `Provider` requires a `CallerResolver` and `CredentialStore` it
may not have, and `Provider.Mount` would also wire `/authorize`, `/token`,
and `/register`, which a pure resource server must never serve (it has no
session to resolve a caller from).

Use these instead, on the resource server's own mux:

```go
mux.Handle(mcpauth.ProtectedResourceMetadataPath, mcpauth.NewProtectedResourceMetadataHandler(mcpauth.ProtectedResourceMetadataConfig{
    Resource:            "https://mcp.example.com",      // this resource server's own URL
    AuthorizationServer: "https://auth.example.com",     // the OTHER process's Provider.Issuer
    ResourceName:        "Example MCP",
}))

requireBearer := mcpauth.RequireBearerToken(credentials, &sdkauth.RequireBearerTokenOptions{
    ResourceMetadataURL: mcpauth.ProtectedResourceMetadataURL("https://mcp.example.com"),
})
```

`credentials` is the same `CredentialStore` (backed by the same
`mcp_credential` table) the authorization-server process's `Provider`
mints against via its own `ProviderConfig.Credentials` — the two processes
share this table (and, for a multi-replica authorization server, the
Postgres-backed `ClientRegistry`/`AuthCodeStore` above) but nothing else;
see `audience_score_system/ARCHITECTURE.md` "MCP server: caller
authentication" → "Split across two binaries" for a worked example
end to end, including the exact discovery chain an MCP client drives
across both processes.

## Usage

```go
package main

import (
    "context"
    "log"

    "github.com/whale-net/everything/libs/go/mcpauth"
    "github.com/jackc/pgx/v5/pgxpool"
)

func main() {
    ctx := context.Background()
    pool, err := pgxpool.New(ctx, "" /* reads PG_DATABASE_URL via //libs/go/db, or pass explicitly */)
    if err != nil {
        log.Fatal(err)
    }

    // Generic consumer.
    store, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{Pool: pool})
    if err != nil {
        log.Fatalf("mcpauth: %v — apply your domain's mcp_credential migration first", err)
    }

    rawToken, cred, err := store.Mint(ctx, "my-service-account")
    if err != nil {
        log.Fatal(err)
    }
    // Show rawToken to the operator now — it is never recoverable again.
    log.Printf("minted credential %s for %s", cred.ID, cred.Identity)

    identity, cred, err := store.Verify(ctx, rawToken)
    if err != nil {
        log.Fatal("invalid or revoked credential")
    }
    log.Printf("verified as %s (last used %v)", identity, cred.LastUsedAt)

    if err := store.Revoke(ctx, cred.ID, identity); err != nil {
        log.Fatal(err)
    }
}
```

ASS-shaped consumer (Person UUID identity):

```go
store, err := mcpauth.NewCredentialStore(ctx, mcpauth.StoreConfig{
    Pool:           pool,
    IdentityColumn: "person_id",
    IdentityCast:   "uuid",
})
```

## Setup

1. Apply your domain's own migration creating the schema above (see
   "Schema contract"). This library does not ship or run migrations
   (NFR5) — see `audience_score_system/migrate/schema/migrations/006_mcpauth_credential.up.sql`
   for a real domain-owned migration satisfying this contract; mirror that
   pattern for `mcp_credential`. (This is deliberately unlike
   `libs/go/htmxauth`'s `ui_sessions`, which one shared, byte-identical
   table shape let the library bundle and merge in via
   `migrate.WithSource` instead — mcpauth's schema varies per domain
   (identity column name/type/FK), so there is no single shape to bundle.)
2. Construct a `*pgxpool.Pool` (see `//libs/go/db`).
3. Call `mcpauth.NewCredentialStore` — it preflights the configured table
   and returns an error naming it if the migration has not been applied.

## Testing

Pure-Go unit tests cover, no database required, run via
`bazel test //libs/go/mcpauth/...`:

- `credential_test.go` — token generation, hashing parity with ASS's
  current `hashToken`, `StoreConfig` defaults, and identifier validation.
- `provider_test.go` — `NewProvider` validation (`Issuer`/`Resource` must
  be absolute URLs, required fields), `validateAbsoluteURL`/
  `isLoopbackHost`.
- `metadata_test.go` — protected-resource and authorization-server
  metadata's exact JSON shape (against an `httptest`-mounted `*Provider`),
  CORS + `OPTIONS` handling, the metadata/route drift guard (every
  advertised endpoint URL actually routes, not 404), and the full
  protected-resource → authorization-server → `/register` bootstrap chain
  in one test (the NFR4 sequence every MCP client runs).
- `clients_test.go` — `NewMemoryClientRegistry`'s register/get round trip
  and cross-instance isolation, `validateRedirectURI`'s allow/reject
  cases, and `/register`'s full happy-path and RFC 7591 rejection-body
  behavior (asserting no raw Go error text ever reaches the response).

Integration tests against a real Postgres (`//go:build integration`, using
`//libs/go/dbtest`) cover what pure-Go tests cannot:

- `credential_integration_test.go` — both a generic `identity TEXT` table
  and an ASS-shaped `person_id UUID` table, proving the full
  mint/verify/revoke/list lifecycle and the preflight failure path against
  real SQL.
- `clients_integration_test.go` — the `mcp_oauth_client` preflight
  failure/success path, the guarantee `NewMemoryClientRegistry` cannot
  give at all (a client registered via one `NewPostgresClientRegistry`
  instance ("replica A") is retrievable via a second, separately
  constructed instance sharing the same database ("replica B")), and a
  concurrent-registration race test (many goroutines calling `Register`
  against one registry at once, asserting every `client_id` is distinct,
  every row persists, and none is lost to the primary-key-constrained
  `INSERT`).

Run explicitly (requires a working Docker daemon):

```sh
bazel test //libs/go/mcpauth:mcpauth_integration_test --test_output=all
```

## License

Part of the Everything monorepo.
