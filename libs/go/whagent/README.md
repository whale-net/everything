# whagent — the whagent-net persona claim and idempotency-key contract

The one published contract a domain-owned MCP server satisfies to be
usable by a whagent-net session (FR12), and the credential whagent-net
presents on every tool call (FR10). This is a standalone library — it has
**no dependency on `whagent_net/`** — so it can land, and be adopted by a
consuming domain (`audience_score_system` for M1), independently of the
rest of whagent-net's M1 work.

## What this is (and isn't)

- **One verifiable credential (LB3).** whagent-net mints a short-lived JWT
  (the `Claim`), signed with its own asymmetric key, and is the sole trust
  root for agent actions. A domain server **never** accepts a bare
  Keycloak token or an unsigned claim in its place (NFR4) — there is no
  per-domain Keycloak token-exchange configuration to set up.
- **Not an OAuth2 authorization server.** Unlike `libs/go/mcpauth`, this
  package does not run `/authorize`/`/token`/`/register` endpoints, and
  does not manage a long-lived, revocable, database-backed credential.
  `Signer`/`Verifier` mint and check a short-lived (minutes, not hours)
  bearer JWT instead — there is no revocation list, because there is
  nothing long-lived to revoke.
- **Not a replacement for a domain's existing auth.** `Middleware` and
  `HTTPMiddleware` are mounted **alongside**, never in place of, whatever
  web-session (or other) caller-resolution flow a domain server already
  has — see "Mounting the verifying middleware" below.

## The `Claim` (LB3, FR10)

Exactly these fields, and no profile attributes — no email, no display
name, no other profile data. A domain auto-provisioning a user record from
this claim (see "Identity mapping" below) necessarily provisions an
identity-key-only record.

| Claim field | JWT claim | Meaning |
|---|---|---|
| `Claim.Subject` (embedded `jwt.Claims`) | `sub` | The on-behalf-of subject's `sub`. For M1 this is always the human operator who started the session — no delegated-caller path exists yet. |
| `Claim.SubjectIssuer` | `sub_iss` | The on-behalf-of subject's `iss` (LB2's shape verbatim). |
| `Claim.Actor` | `act` | The acting whagent-net agent: `Actor.Subject` (the acting subject) plus `Actor.AgentID` (the whagent-net `agent_id`, NFR6). |
| `Claim.WhagentSessionID` | `whagent_session_id` | The whagent-net session this call belongs to. |
| `Claim.Audience` (embedded) | `aud` | The target domain server this Claim was minted for. |
| `Claim.Issuer` (embedded) | `iss` | whagent-net's own issuer — never a domain's, never a Keycloak realm's. |
| `Claim.IssuedAt` (embedded) | `iat` | Standard. |
| `Claim.Expiry` (embedded) | `exp` | Short-lived: minutes, not hours (`DefaultTTL`). |
| `Claim.ID` (embedded) | `jti` | Standard. |

**Do not add a convenience profile field to `Claim` "because it would be
useful."** FR10 forbids it; that would be a contract change, not a nicety.

## Minting (`Signer`)

```go
signer, err := whagent.New(privateKey, "whagent-net", "2026-09-key-1")
token, err := signer.Mint(ctx, whagent.MintRequest{
    Subject:       humanSub,
    SubjectIssuer: humanIssuer,
    Actor:         whagent.Actor{Subject: agentServiceSub, AgentID: "research-agent"},
    SessionID:     sessionID,
    Audience:      "https://ass-mcp.example.com",
})
```

`New` requires an asymmetric `crypto.Signer` (Ed25519, ECDSA, or RSA —
`Mint` picks the matching JWS algorithm, e.g. `EdDSA`/`ES256`/`RS256`,
from the key's own type) — **asymmetric signing only**, so a `Verifier`
never needs, and is never given, the signing key. `Mint` defaults
`MintRequest.TTL` to `DefaultTTL` when left zero, and rejects any TTL
longer than `MaxTTL` (LB3 — minutes, not hours). `Signer.JWKS()` returns
the public JWKS document the minting service serves so a `Verifier`
constructed against a JWKS URL can fetch and cache it.

## Verifying (`Verifier`)

```go
verifier, err := whagent.NewVerifier(ctx, "https://whagent-net.example.com/.well-known/jwks.json", "whagent-net")
claim, err := verifier.Verify(ctx, token, "https://ass-mcp.example.com")
```

`Verify` rejects, with a distinct sentinel error a caller can `errors.Is`
against, each of:

- `ErrInvalidSignature` — bad signature, **including a token signed by
  anything other than the configured whagent-net key** (NFR4) — a valid
  Keycloak-signed token is rejected exactly the same way as a forged one.
- `ErrInvalidAudience` — `aud` absent or not the expected audience.
- `ErrExpired` — `exp` has passed.
- `ErrMissingClaim` — any required field (the table above) is absent.
- `ErrUnknownIssuer` — `iss` is not the `Verifier`'s configured
  whagent-net issuer.

For tests, construct a `Verifier` against a static public key instead of a
JWKS URL — `whagent.NewVerifierFromKey(pub, issuer)` — so a test does not
need to stand up a JWKS endpoint.

## Mounting the verifying middleware (FR12(a))

A consuming domain mounts **two** halves, both new, both alongside any
existing auth the domain already has:

```go
// HTTP layer -- extracts the bearer credential, verifies it, and
// stashes the result for the MCP-protocol layer below. Standalone: does
// not require libs/go/mcpauth to be anywhere in this chain.
httpHandler := whagent.HTTPMiddleware(verifier, myAudience)(mcpHandler)

// MCP-protocol layer -- reads the verified Claim and places it on ctx.
// Add this alongside (never in place of) any existing
// mcp.Server.AddReceivingMiddleware call, e.g. ASS's own PersonMiddleware.
srv.AddReceivingMiddleware(whagent.Middleware(verifier, myAudience))
```

Inside a tool handler, read the verified claim with:

```go
claim := whagent.ClaimFromContext(ctx)
```

This mirrors the shape `audience_score_system/mcp/server/auth.go`'s
`PersonMiddleware` already uses (`mcp.Server.AddReceivingMiddleware`), and
is built on the same `sdkauth.RequireBearerToken` bearer-extraction
machinery `libs/go/mcpauth.RequireBearerToken` already uses — but with its
own `sdkauth.TokenVerifier` calling `Verifier.Verify`, so it is a genuinely
standalone authentication path, not one built on top of `mcpauth`.

## Identity mapping — `(iss, sub)` to local user record

A consuming domain resolves the verified Claim's on-behalf-of pair --
`(Claim.SubjectIssuer, Claim.Subject)` -- to its own user record,
**auto-provisioning** that record the first time a given `(iss, sub)` pair
is seen rather than requiring a pre-existing linked account. That record
is necessarily identity-key-only (FR10): a domain must acquire any profile
data (name, email, ...) through its own means, never from this claim.

`audience_score_system` does exactly this for M1: a new whagent-claim
lookup keyed on `(iss, sub)`, alongside its existing
`UpsertByGoogleSubject`, both resolving to the same `Person` type.

## Idempotency (LB4, FR11)

- **Argument name.** `IdempotencyKeyArgument` (`"idempotency_key"`) is the
  top-level string tool argument every mutating tool must accept -- this
  matches what `audience_score_system` already ships.
- **Derivation.** `DeriveIdempotencyKey(sessionID, turn, callIndex)` is
  deterministic and, by construction, stable across retries of the same
  call: a retry re-sends the same `turn`/`callIndex` within the same
  session, so it re-derives to the exact same key. **Never regenerate this
  key on retry, and never derive it any other way.**
- **Guard scope.** `(tool, resolved identity, key)` -- "resolved identity"
  is the domain's own user record (e.g. ASS's `person_id`), regardless of
  which of the domain's parallel authentication paths (this package's
  Claim-verifying path, or any pre-existing path) produced it. A domain
  keeps **one** idempotency store across all of its authentication paths,
  not one per path.
- **Routing.** `IdempotencyKeyed` is the interface a write tool's decoded
  input implements to expose its caller-supplied key -- a domain's tool
  registry type-asserts against it to decide whether to route a call
  through its idempotency guard. This formalizes the shape
  `audience_score_system/mcp/server/idempotency.go` already defines.

## Status

Implementation phase (issue #2110) complete: `Signer.Mint`/`Signer.JWKS`,
`Verifier.Verify`, and both `Middleware`/`HTTPMiddleware` halves are fully
implemented (asymmetric signing via go-jose, keyed on the private key's
own type -- Ed25519/ECDSA/RSA -- with `kid` set from `Signer.New`'s
`keyID`; verification via `oidc.KeySet.VerifySignature` so JWKS `kid`
rotation and caching come from the already-vendored `go-oidc` library
rather than reimplemented here). `bazel build //libs/go/whagent/...`
passes. Pending the Testing phase: the pure-Go unit test suite described
below.

## Testing

Pending the Testing phase. Expect pure-Go unit tests (no database, no
network) covering: `Signer.Mint`/`Verifier.Verify` round-tripping,
`Verify`'s five distinct rejection cases (including the NFR4
Keycloak-signed-token rejection), `Middleware`/`HTTPMiddleware` wiring
against fakes, and `DeriveIdempotencyKey`'s determinism/retry-stability.

## License

Part of the Everything monorepo.
