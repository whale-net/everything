# MCP server

`mcp` is built on the official `github.com/modelcontextprotocol/go-sdk`
(`mcp` package), currently vendored at `v1.7.0`. This is the first Go MCP
server in the repo, so there is no in-repo Go precedent to follow or
diverge from — the choice is purely against the field of Go MCP SDKs
available upstream, and the official SDK (maintained jointly by Anthropic
and Google under the `modelcontextprotocol` GitHub org, the same org that
publishes the MCP spec itself) is the safest default: spec-tracking is
its whole job, whereas third-party Go SDKs risk drifting or going
unmaintained.

The Python/FastMCP precedent used elsewhere in this repo (`serial-mcp`,
`agentsync-mcp`, `tilt-mcp`) is **explicitly not applicable** here — see
"Language: Go throughout" above. FastMCP is a Python framework; ASS's
`mcp` binary is Go, sharing `//libs/go/temporal` and `//libs/go/db` with
`web` and `worker`, so a Python MCP framework was never a candidate.
Reusing the Python precedent would have meant either forking `mcp` into a
different language than its siblings (breaking the shared-toolchain
rationale) or hand-rolling the MCP protocol layer from scratch instead of
using an existing, spec-authoritative SDK — the official Go SDK is
strictly better than both.

`google.golang.org/api` (vendored at `v0.296.0`) supplies the two YouTube
API clients `mcp` and `worker` need: `youtube/v3` (YouTube Data API v3 —
schedule/video metadata) and `youtubeanalytics/v2` (YouTube Analytics API
v2 — published-video metrics, C9). Both reuse the already-vendored
`golang.org/x/oauth2` (`v0.36.0`, unchanged by this task) for the
Channel-level OAuth token flow (C2). See
`//audience_score_system/deps` for the compile-only smoke target proving
all three packages resolve under Bazel.

`//audience_score_system/youtube` (`Client`, #1573) is the sole point of
contact with those two vendored clients — quota handling, error
classification (`ErrRevoked`/`ErrQuotaExceeded`/`ErrTransient`/
`ErrPermanent`), and revoked-credential detection (FR4) live here once, so
`mcp` and `worker` never import `google.golang.org/api/...` directly. The
only accepted exception is `web/channel`'s own inline
`channels.list?mine=true` resolver (#1571), which `youtube.Client`'s
`ChannelInfo` may absorb later. `youtube/fake` is an in-memory `Client`
double consumers use in tests with no network call.

### MCP server: caller authentication

**Decision (landed in #1575's Scaffold/Implementation phases, migrated
onto the shared `//libs/go/mcpauth` library by #1643):** an MCP client
authenticates as a Person with a bearer credential that `mcp`'s auth stack
resolves to `person.id` server-side. The credential is a high-entropy
random token; only its SHA-256 hash is ever persisted, in `mcp_credential`
(migration 006, backed by `libs/go/mcpauth.CredentialStore` — see
`libs/go/mcpauth/README.md`'s schema contract) — the raw token is shown to
the Person exactly once, at mint time, and is never recoverable from the
database afterward. `mcp_credential` was originally created by migration
005 against ASS's own bespoke `store.CredentialStore` (#1575); migration
006 drops and recreates it against `mcpauth`'s generic contract while
preserving ASS's own referential integrity (`person_id` stays a real
foreign key to `person(id)`, and both of 005's indexes are kept verbatim)
— `mcpauth` itself treats identity as an opaque string
(`StoreConfig.IdentityColumn = "person_id"`,
`StoreConfig.IdentityCast = "uuid"` tell it how to bind/cast against this
column), so that genericity never became a reason ASS lost its FK
(FR13/NFR5).

- **(a) Obtained:** minted via `mcpauth`'s own OAuth2 authorization-code +
  PKCE `/token` endpoint, mounted on `web` (issue #1646). An MCP client
  drives the standard RFC 9728/8414/7591 discovery-to-registration chain
  against `web`, then `/authorize` resolves the caller through
  `web/auth.Authenticator.MCPCallerResolver()` — reusing the Person's
  existing C1 sign-in session (`SessionManager.PersonID`, the same read
  `RequireSignedIn` performs) rather than any new credential-collection UI
  or a fresh IdP round trip. An unresolved caller is redirected to `/login`
  with the exact original `/authorize` request preserved via ASS's
  existing `?next=` convention (`mcpauth.ProviderConfig.SignInReturnParam`,
  defaulted to `"next"`) and returns to `/authorize` after Google sign-in.
  `mcpauth.CredentialStore.Mint`'s only production caller is `/token`'s
  handler, invoked once the authorization code is redeemed. A self-serve
  mint/revoke/list UI page on `web` is separate scope (#1591) — not
  needed for a caller that IS an MCP client, since the client itself
  drives the OAuth2 flow.
- **(b) Revoked:** `mcpauth.CredentialStore.Revoke` closes a credential by
  setting `revoked_at`; a revoked credential's hash no longer resolves in
  `Verify`, so any MCP call bearing it is rejected on the next request
  without needing to invalidate anything client-side. Revocation is
  idempotent (NFR2) — revoking an already-revoked or nonexistent
  credential is not an error.
- **(c) NFR3 rationale:** minting a credential is sign-in machinery — it
  bootstraps an already-authenticated Person's access to `mcp`, the same
  way `web/auth`'s OAuth callback bootstraps access to `web`. It performs
  none of C4-C10's actions itself (no research notes, verdicts, schedule
  drafts, pacing, outcome confirmation, or browsing happen at mint time),
  so it does not grow a new capability surface on `web` and NFR3 stands.

Mechanically, resolution happens in two layers (see
`audience_score_system/mcp/server/`):

1. **HTTP layer** (`transport.go`): `mcpauth.RequireBearerToken` wraps the
   streamable HTTP handler, calling `mcpauth.TokenVerifier` under the hood
   to hash the raw token and resolve it via
   `mcpauth.CredentialStore.Verify`, producing an `auth.TokenInfo` whose
   `UserID` is the resolved Person's ID (rendered as a string). Credentials
   do not expire on a timer (they live until revoked), so
   `mcpauth.RequireBearerToken` always forces `AllowMissingExpiration:
   true` internally rather than requiring a per-token `exp` claim.
2. **MCP-protocol layer** (`server.go`/`auth.go`): `PersonMiddleware`, wired
   via `mcp.Server.AddReceivingMiddleware`, reads that `TokenInfo` off each
   request's `RequestExtra`, parses `UserID` back into a `uuid.UUID`,
   resolves the full `store.Person` via `store.PersonStore`, and places it
   on the handler's `context.Context` (`PersonFromContext`, `context.go`).
   A request with no resolved `TokenInfo`, an unparseable `UserID`, or a
   `UserID` that doesn't resolve to a real Person, is rejected here — the
   tool handler is never entered. This step is unchanged by the #1643
   migration — `mcpauth` only replaces the credential storage/verification
   layer, not how a resolved identity becomes a Person.

`mcpauth.CredentialStore.Verify`, `Mint`, `Revoke`, and `List` are real
SQL-backed implementations against `mcp_credential`, constructed in
`mcp/main.go` via `mcpauth.NewCredentialStore` — its preflight probe means
a missing migration 006 fails `mcp` at boot instead of at first call.
`Verify` also stamps `last_used_at` in the same round trip, so it doubles
as the "last seen" signal for a future credential-management view (see
issue #1591's scope note).

**Split across two binaries (issue #1646).** `mcpauth`'s OAuth2
authorization-code + PKCE `/authorize` endpoint needs the caller's ASS web
session cookie, which only `web` has; the OAuth2 protected resource an MCP
client ultimately calls is `mcp`. So:

- `web` hosts the full OAuth2 authorization server: `/authorize`, `/token`,
  `/register`, and `/.well-known/oauth-authorization-server`
  (`mcpauth.Provider`, `web/main.go`'s `run()`, mounted outside
  `RequireSignedIn` — `mcpauth`'s own `Resolver`/`SignInURL` do the gating
  for `/authorize`, and `/token`/`/register` are called directly by the MCP
  client with no session cookie at all, so wrapping either in
  `RequireSignedIn` would break them).
- `mcp` hosts only the protected-resource half: `/.well-known/oauth-protected-resource`
  (`mcpauth.NewProtectedResourceMetadataHandler`, `mcp/server/transport.go`'s
  `NewHTTPHandler`) plus the `WWW-Authenticate: Bearer resource_metadata="..."`
  challenge a missing/invalid bearer token gets
  (`mcpauth.ProtectedResourceMetadataURL`, passed as
  `sdkauth.RequireBearerTokenOptions.ResourceMetadataURL`). `mcp` never
  mounts `/authorize` or `/token` — it has no session cookie to resolve a
  caller from, and has no business doing so.
- Both well-known/discovery paths are registered at each binary's mux
  root, never under a prefix — MCP clients probe fixed well-known
  locations (RFC 9728 §3, RFC 8414 §3).
- `web` and `mcp` share one Postgres and nothing else (no cross-service
  call, no shared in-process state): a credential minted by `web`'s
  `/token` is immediately verifiable by `mcp`'s
  `mcpauth.CredentialStore.Verify` against the same `mcp_credential`
  table, and an authorization code or dynamically registered client
  `/authorize`/`/register` create on one `web` replica is resolvable by
  `/token` on a different `web` replica — this is why ASS MUST construct
  `mcpauth.NewPostgresClientRegistry` and `mcpauth.NewPostgresAuthCodeStore`
  (migration 007, `mcp_oauth_client`/`mcp_auth_code`) rather than
  `mcpauth`'s single-replica in-memory defaults (NFR5's schema-ownership
  split: `mcpauth` ships no migrations of its own, ASS's own migration
  tooling owns 006 and 007 against `mcpauth`'s documented schema
  contracts).
- Discovery chain an MCP client actually drives, end to end: unauthenticated
  call to `mcp` → 401 naming `mcp`'s own `resource_metadata` URL → GET that
  URL (`mcp`) → follow its `authorization_servers[0]` (`web`'s issuer,
  `ASS_OAUTH_REDIRECT_BASE_URL`) → GET `web`'s
  `/.well-known/oauth-authorization-server` → `POST /register` → `GET
  /authorize` (real session cookie) → `POST /token` → bearer credential,
  now usable against `mcp`. No step in this chain is ASS- or
  client-specific (NFR4) — see
  `mcp/server/oauth_bootstrap_integration_test.go` for the test that drives
  this exact sequence across two independently constructed `web`-shaped and
  `mcp`-shaped server instances sharing one database.

### MCP server: whagent-net authentication path (issue #2116, FR12)

**Decision:** a second, fully independent caller-authentication path,
mounted **alongside** — never built on top of, and never replacing — the
`mcp_credential` path above. A whagent-net session presents a short-lived
JWT signed by whagent-net's own key (`//libs/go/whagent`'s published
contract, LB3/NFR4); `mcp` verifies it against whagent-net's JWKS and
resolves its verified `(iss, sub)` on-behalf-of claim to an ASS `person_id`,
auto-provisioning a `Person` the first time a given pair is seen. Both
paths coexist on the exact same `mcp.Server`/mux — a Keycloak token, an
ASS `mcp_credential`, or an unsigned claim can never satisfy the other
path's verifier (NFR4).

**The contract gap this path had to work around.** `//libs/go/whagent`
publishes a `Middleware`/`HTTPMiddleware` pair meant to be mounted
directly, but `Middleware` unconditionally rejects any call carrying no
verified `Claim`, and `HTTPMiddleware` is the only sanctioned way to
populate the private extra-key `Middleware` reads back off the request —
there is no exported way to make either a no-op pass-through for a call
that instead came in via `mcp_credential`. Mounting them verbatim in
series alongside the existing chain would reject every `mcp_credential`
call outright, exactly the "built on top of" failure mode FR12(a)
forbids. `mcp/server/whagent_auth.go` instead:

1. Routes each request's bearer token to exactly one of the two paths at
   the HTTP layer (`DualAuthHTTPHandler`), keyed on token SHAPE: a
   whagent Claim is always a three-segment, two-dot JWT compact
   serialization; an ASS `mcp_credential` is always a 64-character hex
   string with no dots — the two encodings never overlap, so this split
   is exact, not probabilistic. Each branch is its own independent
   `sdkauth.RequireBearerToken` instance.
2. Calls `whagent.Verifier.Verify` directly (an equally-exported,
   equally-documented entry point on the same published contract) rather
   than `whagent.HTTPMiddleware`, building this file's own
   `sdkauth.TokenInfo`/`Extra` marker instead of whagent's private one.
3. `WhagentPersonMiddleware` (the MCP-protocol half, mounted so it runs
   BEFORE `auth.go`'s `PersonMiddleware` — see `server.New`/`mcp/main.go`'s
   construction order) reads that marker: present, it resolves `(iss, sub)`
   to a `Person` (`store.PersonIdentityStore.FindOrCreateByIssSub`,
   auto-provisioning on first sight) and places it on `ctx`; absent, it
   calls `next` unchanged — `next` IS `PersonMiddleware`, which
   authenticates the call exactly as it always has. The one coexistence
   seam this composition needs is a single early check in
   `PersonMiddleware` itself: if a `Person` is already on `ctx`, skip its
   own `mcp_credential`-shaped resolution and call its own `next` directly,
   rather than reinterpreting a whagent-routed call's `TokenInfo` as an ASS
   credential and rejecting it. This check is a no-op for every call that
   predates #2116 (nothing else ever placed a `Person` on `ctx` before
   `PersonMiddleware` ran), so the existing path's own behavior is
   unchanged for `mcp_credential` callers.

This composition seam — not any change to `//libs/go/whagent`'s own
contract — is what made the two paths coexist on the SDK's linear
receiving-middleware chain. Worth revisiting on `//libs/go/whagent` itself
if a second consuming domain hits the identical problem: an exported "is
this call whagent-authenticated" predicate on `mcp.Request` would let a
consumer skip the ASS-side `PersonMiddleware` amendment entirely.

**`(iss, sub)` → `Person`, with auto-provisioning (FR12(b)).** Migration
020's `person_oidc_identity` table is keyed on the **pair** `(iss, sub)` —
never on `sub` alone, and never re-keyed off `person.google_subject` — so
the same external subject under two different issuers resolves to two
distinct Persons, and a future second issuer (e.g. Google itself, M3) is
one more row-set rather than a schema re-key. `PersonIdentityStore.
FindOrCreateByIssSub` (`store/person_identity.go`) auto-provisions an
identity-key-only `Person` (`google_subject`/`email`/`display_name` all
NULL — the whagent Claim carries none of FR10's profile attributes) the
first time a pair is seen; migration 020 drops `person.google_subject`'s
`NOT NULL` constraint (still `UNIQUE`) specifically to allow this, since it
was ASS's only identity key until this task and a whagent-net-provisioned
Person may never sign into `web`. Because the find-or-create spans two
tables (`person`, `person_oidc_identity` — deliberately kept separate, see
migration 020's SQL comment), it cannot be a single `ON CONFLICT` statement
like `UpsertByGoogleSubject`'s; instead it opens a transaction, speculatively
inserts both rows, and — if the identity-link insert loses a concurrent
race for the exact same pair (`person_oidc_identity_iss_sub`'s unique
index) — rolls back the whole transaction (discarding the speculative
`Person` row) and re-resolves to the winner, so a concurrent first-sight
race still creates exactly one `Person`.

**FR11: one idempotency store, not one per auth path.** `store.
Idempotency.Do`'s `(tool, personID, key)` guard (migration 002) is
untouched by this task — both paths place their resolved `Person` on `ctx`
via the exact same `withPerson`, so `registry.go`'s `RegisterWrite` never
has to know which path authenticated a call. If the same human's calls
via both paths ever resolve to the same `person_id`, that one guard
already applies "at most once" correctly; see `mcp/server/
whagent_auth_integration_test.go`'s FR11 test for how this is proven
without a pre-linking flow (M1 has none, FR12(b)) — it auto-provisions a
Person via the whagent identity store, then mints an `mcp_credential` for
that exact `person_id`, and calls the same write tool with the same
`idempotency_key` once via each path.

### MCP server: Channel-scoping and idempotency middleware

Both wired into `mcp/server/registry.go`'s `RegisterRead`/`RegisterWrite`
so a product tool author gets them automatically rather than having to
remember to call them:

- **Channel-scoping (NFR5):** a tool's input type opts in by implementing
  `ChannelScoped` (`channelscope.go`, one `ChannelScopeID() uuid.UUID`
  method). `RegisterRead`/`RegisterWrite` type-assert each call's decoded
  input against that interface at call time and, when it matches, run
  `RequireChannelRole` against `store.CanRead`/`store.CanWrite` before the
  handler runs — a caller with no live `channel_person` row for that
  Channel gets a permission error and the handler is never entered. A tool
  whose input carries no `channel_id` (`whoami` and `list_channels`, #1631 --
  a tool that reports the caller's own identity/access rather than a
  specific Channel's data has nothing to scope) simply doesn't
  implement the interface and is left unscoped; this is deliberate, not an
  oversight — NFR5 only applies to Channel-scoped data.
- **Idempotency (NFR2/LB4):** `RegisterWrite` splits a write tool into a
  `WriteMutate` step (the side-effecting write, returning the UUID of the
  entity it created or affected) and a `WriteRender` step (builds the
  tool's structured response from that ref). This split exists because
  `store.Idempotency.Do` (already real, migration 002/#1569) only ever
  persists/returns a UUID (`mcp_idempotency.result_ref`) — there is
  nowhere in that ledger to cache an arbitrary tool response, so replaying
  a call means re-deriving the response from the ref via `WriteRender`,
  not replaying a cached response body; `WriteRender` runs on every call,
  first run and every replay alike, so a write tool's response always
  reflects current DB state. A tool's input opts into key-based replay by
  implementing `IdempotencyKeyed` (`idempotency.go`, one
  `IdempotencyKey() string` method) and returning a nonempty key;
  `RegisterWrite` then computes `request_fingerprint` as a stable hash of
  the tool name plus the input's JSON encoding and runs `WriteMutate`
  under `store.Idempotency.Do`'s guard. A tool with no key (or whose input
  doesn't implement `IdempotencyKeyed`) runs `WriteMutate` directly every
  call and must be safe via natural-key upsert instead — per this task's
  Implementation notes, every write tool states which of the two
  mechanisms it uses.

### MCP server: statelessness (LB4)

`mcp` holds no server-side per-session state beyond Postgres. `server.New`
builds one `*mcp.Server` from a `*store.Store` and nothing else; the
streamable HTTP handler's `getServer` callback (`transport.go`) always
returns that same instance, never constructing per-request state, and no
package in `audience_score_system/mcp/...` keeps an in-memory map keyed by
session or conversation ID. Every cross-cutting concern this task owns —
caller identity, Channel-scope authorization, and the idempotency ledger —
resolves through a Postgres read/write on every call, so a second,
independently-constructed server instance sharing the same database
produces identical results to the first. Enforce this in review: a
handler or middleware that introduces an in-memory cache/map keyed by
caller or session violates LB4 even if it "only" affects performance.

### MCP server: observability

`mcp/main.go` configures `//libs/go/logging` the same way `web` and
`worker` do (`ServiceName: "audience-score-system-mcp"`, OTLP logs +
tracing enabled) and wraps its HTTP handler in `otelhttp.NewHandler` --
but every MCP tool call multiplexes over that one HTTP endpoint as
JSON-RPC, so an HTTP-level span alone never shows which tool ran, for
which caller, or whether it succeeded. `mcp/server/observability.go`'s
`instrumentToolCall` closes that gap: `RegisterRead`/`RegisterWrite`
(`registry.go`) route every registered tool's full call -- including the
unauthenticated/permission-denied paths they check before the product
handler runs, not just the handler itself -- through it, so a tool author
gets a `mcp.tool/<name>` trace span and a structured success/failure log
line (tool, resolved caller, duration) the same way they already get
Channel-scope authorization and idempotency: by going through the
registry, not by remembering to add it themselves. Rejections that never
reach the registry -- `PersonMiddleware`'s caller-identity checks and
`TokenVerifier`'s bearer-token verification (both `auth.go`) -- log
directly at `Warn` against the same package-level logger, never including
the raw token or its hash.

