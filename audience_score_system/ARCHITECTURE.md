# Audience Score System — Architecture

Design record for `//audience_score_system`. Read [`README.md`](README.md)
first for what the system is and how to run it locally, and
[`PRODUCT.md`](PRODUCT.md) for the vision and load-bearing decisions this
architecture implements.

## Language: Go throughout

ASS is **Go throughout** — `web`, `mcp`, and `worker` all reuse
`//libs/go/temporal`, `//libs/go/db`, and `//libs/go/migrate`, the same as
`migrate` (this scaffold). This is a deliberate override of the pattern
`product/01-current-state.md` surveyed: every existing MCP server in this
repo (`serial-mcp`, `agentsync-mcp`, `tilt-mcp`) is Python + FastMCP. ASS's
MCP server is the first Go MCP server in this repo — accepted as a known
gap for M1 (no in-repo Go MCP framework precedent to follow), not a
blocker. The tradeoff: one shared language/toolchain across all four
binaries and reuse of the hardened Go Postgres/Temporal libraries, at the
cost of writing the MCP protocol layer without a same-language precedent.

## MCP server

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

## Component map

| Component | Binary | `release_app` identity | Responsibility |
|---|---|---|---|
| `migrate` | `audience_score_system/migrate` | `migration` (job) | Applies golang-migrate SQL migrations to Postgres. Runs once, ahead of the other three, as a Helm job hook (see `libs/go/migrate/README.md`). |
| `web` | `audience_score_system/web` (C1 sign-in #1570, C2 Channel-connect #1571, C3 analyst invite #1572, C8 schedule approve/un-approve/edit UI #1580) | `web` (external-api) | The **only** UI surface. Its three UI-only OAuth-consent surfaces are C1/C2/C3 (see "NFR3 interface allocation" below); its C8 schedule page is a UI front end onto the same `store.ScheduleStore` that `mcp`'s schedule-draft tools also write to. |
| `mcp` | `audience_score_system/mcp` (#1575, #1577-#1582, #1631, #1648, #1650) | `mcp` (external-api) | Every other capability (C4-C7, C8, C9, C10): Channel access discovery (`list_channels`, #1631 -- resolves which Channels the caller holds a role on, and that role, without dropping to the web UI), research notes, viability verdicts, schedule sync reads, schedule draft proposals/commit/un-commit/edit, pacing policy, outcome-match confirm/reject, all browsing, and (#1650) forcing an out-of-band `ChannelSyncWorkflow` run via `trigger_channel_sync`. Exposed as MCP tools to any MCP-capable agent client. |
| `worker` | `audience_score_system/worker` (#1574, #1576, #1581) | `worker` (worker) | Per-Channel Temporal scheduled workflow: syncs YouTube schedule (C6) and published-video metrics (C9) on a ~1-24 hour cadence (NFR4, default 24h). Skips a cycle for a disconnected/needs-reauth Channel without erroring the workflow. `mcp`'s `trigger_channel_sync` tool (#1650) can force an out-of-band run of the same workflow without waiting for this cadence. |
| Postgres | — | — | System of record for all four components, accessed via `//libs/go/db` (`PG_DATABASE_URL`). No separate cache/read-model store in M1. |

All four share the `audience-score-system` `release_app` domain, so images
are `audience-score-system-migration`, `-web`, `-mcp`, `-worker`.

## OAuth grants

ASS uses TWO deliberately separate Google OAuth2 grants, never one combined
scope request:

| Grant | Scopes | Establishes | Stored in | Package |
|---|---|---|---|---|
| **C1: Google sign-in/sign-up** | `openid email profile` | A Person's identity (keyed on the Google `sub` claim, #1570) | `web_session` (migration 003) | `web/auth` |
| **C2: YouTube Channel-connect** | `yt-analytics.readonly` + `youtube.readonly` (see `ENV.md` "OAuth scopes", NFR1/LB1) | A Channel's authorization for this app to call the YouTube Data/Analytics APIs on its behalf (#1571) | `channel_credential` (migration 004) | `web/channel` + `tokens` |

**Why two grants, not one:** C1 answers "who is this human" and needs
nothing beyond identity claims -- requesting YouTube scopes at sign-in
would force every Person (including a pure Analyst who never connects a
Channel) through YouTube's consent screen for permissions they may never
use. C2 answers "may this app act on this specific YouTube Channel's
behalf" and is requested only when a Creator actually connects a Channel
(FR3) or reconnects one (FR4). Keeping them separate also means a scope
change to one grant (e.g. LB1's forward-looking Analytics scope) never
forces re-consent of the other.

**Reconnect authorization (FR4, NFR5, FR32):** only a Person holding a
live `role=creator` or `role=co_creator` `channel_person` row on a
Channel may (re)connect it -- Founder and Co-Creator hold symmetric
authority here (FR32), checked via `store.CanReconnect`, the same
sanctioned authz entry point every other M1/M2 permission check uses (see
"Data model" below). This is enforced by
`web/channel.Handler.HandleReconnect`, never inferred from who initiated
the original connect.

**Token storage split:** C1's refresh token lives in `web_session`
(managed by `web/auth.SessionManager`, one row per signed-in session); C2's
access/refresh token lives in `channel_credential` (managed by
`tokens.Store`, SCD2 per `AGENTS.md` -- one open row per Channel, a
reconnect closes the old row and opens a new one so token history is
auditable). Both encrypt at rest with AES-256-GCM under the same
`ASS_TOKEN_ENCRYPTION_KEY`-derived key, but are otherwise independent
stores -- a Person's session surviving does not imply their Channel's
YouTube credential is still valid, and vice versa.

**Needs-reauth lifecycle (FR4):** a Channel is `connected` or
`needs_reauth` (`channel.connection_state`). `tokens.Store.TokenSource`'s
refresh path distinguishes a revoked grant (`invalid_grant` from Google) --
which calls `tokens.Store.MarkNeedsReauth`, flipping `connection_state` to
`needs_reauth` -- from a transient network/5xx failure, which must NOT
trip needs-reauth. A `needs_reauth` Channel retains every previously
synced row (`synced_video`/`video_metrics`/`schedule_entry` are never
deleted) and the worker (#1574) skips its sync cycle for that Channel
without erroring the workflow, until a Creator reconnects (FR4) and
`connection_state` returns to `connected` with no other manual step.

**Schedule creation at connect time, not just at worker startup (FR14/NFR4,
issue #1614):** `web/channel.Handler.HandleCallback` calls
`sync.ScheduleManager.EnsureSchedule` itself, immediately after a Channel
reaches `connection_state = connected` (both the fresh-connect and
reconnect branches) -- `web` constructs its own Temporal client and
`sync.ScheduleManager`, following `worker/main.go`'s exact construction
pattern, rather than introducing a new cross-binary signaling mechanism.
This closes the gap where a Channel connected while `worker` was already
running would otherwise wait for `worker`'s next process restart (its
`Reconcile` only runs at startup) before getting a live schedule.
`EnsureSchedule` is safe to call from two independent places -- `web` at
connect time and `worker` at startup `Reconcile` -- because it is
idempotent (deterministic `sync.ScheduleID(channelID)`): whichever call
lands first creates the schedule, the other gets Temporal's
already-exists response, which `EnsureSchedule` already treats as
success. The call is best-effort and non-fatal from `web`'s HTTP request
path: the Channel is already correctly `connected` in Postgres by the
time it runs, so a transient Temporal failure here logs a warning and
still redirects, degrading to "worker's next startup Reconcile will pick
it up" rather than turning an otherwise-successful connect into a 500.

**Interval-consistency caveat:** `EnsureSchedule`'s `ScheduleSpec` bakes in
whichever `Interval` its *first* caller passes at schedule-creation time
and is never updated by a later `EnsureSchedule` call for the same
Channel (only created-or-no-op). Because `web` and `worker` are now both
callers, they must load `ASS_SYNC_INTERVAL` with the identical default
and the identical `sync.ValidateSyncInterval` band-check (see `ENV.md`
"Temporal") -- a misconfigured `web` diverging from `worker` here could
silently create a Channel's schedule at the wrong cadence, since whichever
binary's `EnsureSchedule` call happens to land first wins.

**NFR3 check for this change:** NFR3 (below) restricts `web` to three
*UI-only* surfaces (C1/C2/C3) -- calling `EnsureSchedule` from
`HandleCallback` adds no new HTTP route, no new MCP tool, and no new
capability visible to a user or agent; it is backend plumbing inside the
already-allocated C2 (Channel-connect) surface, exactly analogous to
`web` already writing `channel_credential` and `connection_state` as part
of that same flow. NFR3 stands unmodified. (This predates issue #1648's
NFR3 amendment, which moved C8 off the `web`-only list entirely -- the
citation here is updated to match, but the `EnsureSchedule` reasoning
itself is unaffected.)

## NFR3 interface allocation

The web UI is **limited to exactly three UI-only surfaces** — everything
else is MCP-exposed too, per the product brief's MCP-agent-first interface
decision:

- **C1** — OAuth signup/login (Google OAuth consent → Person record).
- **C2** — Channel connect (YouTube OAuth consent → Channel + `role=creator`
  join row, LB2).
- **C3** — Analyst invite/accept (invite code generation and
  accept/decline).

These three are OAuth-consent flows tied to a browser redirect and cannot
be MCP tools by construction (there is no meaningful "call this MCP tool
to complete a Google consent screen"). Every other capability — C4
(research notes), C5 (viability verdicts), C6 (schedule sync reads), C7
(schedule drafting, pacing policy), **C8 (schedule-draft
commit/un-commit/edit)**, C9 (outcome comparison, pending-match
confirm/reject), C10 (browsing) — is exposed as `mcp` tools, whether or not
`web` also renders a UI for it.

**NFR3 amendment (issue #1648): C8 is no longer a `web`-only surface.** M1
originally kept all of C8 (approve, un-approve, edit) `web`-only, on the
theory that committing a schedule was a deliberate human action best gated
behind a UI click. In practice this made the FR16→FR19→FR22/FR23 pipeline
(draft → commit → auto/pending-match → resolve) structurally unreachable
from an MCP-only client: `mcp`'s outcome matcher and `resolve_pending_match`
only ever consider *committed* entries, and nothing in `mcp` could ever
produce one. `mcp/tools/schedule_draft.go` now exposes the full set --
`commit_schedule_draft`, `uncommit_schedule_draft`, `update_schedule_draft`
-- each calling the exact same `store.ScheduleStore` method and
`store.CanApprove` (Creator-tier: Founder or Co-Creator, symmetrically per
FR32) check `web`'s approve/unapprove/edit handlers already use, so the
authority boundary is unchanged: an Analyst
credential is rejected on either surface. `web`'s schedule page is
unaffected and keeps rendering the same approve/un-approve/edit
affordances -- the two surfaces are now two independent, equally-capable
front ends onto the same `store.ScheduleStore`, not a primary (`web`) and a
read-only shadow (`mcp`).

**NFR3 amendment (M2, issue #1728): C11/C12/C13 are dual-surface, except
Channel-connect.** M2 adds three capabilities on top of M1's allocation
above:

- **C11 (multi-Channel management):** Channel-connect (FR25) stays
  `web`-only, for the identical OAuth-consent reason C2 always was -- there
  is no more "call this MCP tool to complete a Google consent screen" for
  a second Channel than there was for the first. The Channel list/switcher
  page (FR26, `GET /channels`) is `web`-only by the FR text itself (no MCP
  tool is named for it) -- distinct from `list_channels` (issue #1631,
  predating M2), an MCP tool that already answered a similar "which
  Channels can I see" question for an MCP-only client; M2 repoints
  `list_channels` onto the same `store.AccessStore.
  ChannelsWithRoleForPerson` query FR26's page uses (issue #1719), so the
  two now agree by construction, without FR26 itself requiring a new MCP
  tool.
- **C12 (cross-Channel aggregate, FR27/FR28):** dual-surface by FR27's own
  text -- `GET /my-work` (`web/main.go`'s `handleMyWork`) and `get_my_work`
  (`mcp/tools/my_work.go`) both call `store.MyWorkStore.
  SummariesForPerson` directly, re-deriving the caller's currently-open
  roles on every call (FR28) -- neither surface caches or requires a
  reconnect for a just-revoked Channel to disappear from the very next
  call.
- **C13 (three-tier authority, FR30/FR31/FR33/FR35):** invite Co-Creator,
  promote, remove, and the audit trail are each dual-surface -- `web/
  access.Handlers` (`GET/POST /channels/{id}/access...`) and `mcp/tools/
  access.go`'s `invite_co_creator`/`promote_to_co_creator`/
  `remove_channel_person` plus `mcp/tools/access_audit.go`'s
  `get_channel_access` call the identical `store.InviteStore`/
  `store.RoleStore`/`store.AccessStore` methods and the identical
  `store.CanInvite`/`CanRemove`/`CanViewAudit` authorization checks as
  their web counterparts -- the same "two independent, equally-capable
  front ends" relationship C8's amendment above established, not a
  primary/shadow pair.

The existing NFR3 list (C1/C2/C3 web-only; everything else MCP-exposed
too) and the C8 amendment above are both unchanged by this addition --
this is an appended clarification of three new capabilities' allocation,
not a rewrite of the ones already there.

## Temporal: no scheduled-workflow helper yet

`//libs/go/temporal` (the shared Go bootstrap `worker` uses for its
client/worker construction) has no equivalent to
`friendly_computing_machine`'s hand-rolled `AbstractScheduleWorkflow`
(`temporal/base.py`) — the only in-repo *scheduled*-workflow precedent, and
it's Python/FCM-specific, not a shared library. This is a gap `worker`
inherits from `product/01-current-state.md` and knowingly accepts for M1:
issue #1574 built the per-Channel sync schedule (NFR4, ~1-24 hour
interval, default 24h) directly against the Temporal Go SDK's native `ScheduleClient`
(`audience_score_system/worker/sync.ScheduleManager`, wrapping
`client.Client.ScheduleClient()`), not a repo-shared helper. `ScheduleManager`
is a small, four-method interface (`EnsureSchedule`/`RemoveSchedule`/
`Reconcile`/`TriggerNow`, the last added by issue #1650 to back the
`trigger_channel_sync` MCP tool) with nothing app-registry-specific about
its shape — worth
promoting into `//libs/go/temporal` if a second Go scheduled-workflow
consumer shows up; at that point the duplication is worth generalizing.
Until then it stays local to `worker/sync`, since a one-off abstraction
extracted from a single caller tends to guess wrong about what a second
caller will actually need.

## Data model

Migration 001 (`migrate/schema/migrations/001_identity.up.sql`, issue
#1568) lands the identity core: `person`, `channel`, the `channel_person`
join table (LB2, SCD2 per AGENTS.md), and `channel_invite`. `channel` has
no `owner_id` column -- ownership and every other role live only in
`channel_person`, read through `//audience_score_system/store`'s
`CanApprove`/`CanInvite`/`CanReconnect`/`CanRead`/`CanWrite`/`CanRemove`/
`CanViewAudit` (NFR5/NFR6), the only sanctioned authorization entry
points. `CanRemove` and `CanViewAudit` are M2 additions (migration 009,
below) but the mechanism is not new: every one of the seven functions
resolves purely from `RoleStore.RolesFor`'s currently-held roles for a
`(channel_id, person_id)` pair -- one role-lookup mechanism, extended for
a third tier and a removal matrix, never a second, parallel
authorization path (NFR6). `CanRemove`'s removal matrix (FR33) is the one
authorization decision in this package that depends on TWO Persons' roles
at once (the actor's and the target's), not one -- Founder may remove
Co-Creator or Analyst; Co-Creator may remove Analyst only; nothing ever
removes a Founder, including a Founder removing themselves (self-removal
falls out of the matrix's own cells, not a special case). `CanRemove`
returns `false, nil` both when the matrix disallows a removal and when
the target already holds no open role at all (FR33's idempotent no-op) --
callers that must tell the two apart make one extra `RolesFor` call on
the target, exactly as `store/authz.go`'s `CanRemove` doc comment and
`web/access.HandleRemove`/`mcp/tools/access.go`'s `remove_channel_person`
both do.

Migration 002 (`002_research_schedule_outcome.up.sql`, issue #1569) lands
the LB3 record chain: idea, research notes, viability verdicts (append-only
version log, not SCD2 -- see `AGENTS.md`'s SCD2 event-log exclusion),
pacing policy, schedule entries, synced videos/metrics, and pending
matches, plus the `mcp_idempotency` ledger (NFR2/LB4).

Migration 008 (`008_strategy.up.sql`, issue #1637) lands `strategy` and
`strategy_verdict`: a cadence (weekly/biweekly/monthly, optional preferred
weekday) sitting between viability verdicts and scheduling -- independent
of, and finer-grained than, the Channel-wide `pacing_policy` (FR17). A
Strategy is built directly from one or more `viability_verdict` rows via
`strategy_verdict` -- not from Ideas: `idea_id` is derived through
`viability_verdict.idea_id` rather than stored on the join row, the same
LB3 pattern `schedule_entry.verdict_id` uses one layer downstream, applied
to the join itself. The relationship is many-to-many in both directions --
a Strategy is typically grounded in several verdicts (often several
Ideas), and the same verdict may ground more than one Strategy -- which is
the point: `save_strategy` records exactly which analysis justified the
cadence, not just "whichever idea is currently viable."

**No persisted "Plan" table (issue #1637):** the issue's proposed
`generate_schedule_from_strategies` surface is implemented as
`generate_schedule_plan` (`mcp/tools/strategy.go`), a read-only tool that
derives next-slot proposals fresh on every call from active Strategies +
the current schedule (LB4: nothing about a plan is cached in Postgres).
Committing a proposal reuses the existing `save_schedule_draft` tool
rather than a second write path, so FR16's viable-verdict gate and FR18's
non-blocking flags stay defined in exactly one place. Revisit only if a
product need emerges to browse/audit past *generated* plans as their own
record, distinct from the `schedule_entry` rows a caller chose to commit
from them -- at that point a `plan`/`plan_proposal` pair is a new
migration, not a retrofit of this one.

**`synced_video` retention on disappearance (issue #1576):** C6's schedule
sync (`worker/sync.Activities.SyncSchedule`) upserts every video YouTube's
`ListSchedule` response returns for a cycle, keyed on `(channel_id,
youtube_video_id)`, but never deletes a row for a video that has dropped
out of the response. No `disappeared_at` column was added for this: a
disappeared video's row is simply left untouched, so its `last_synced_at`
stops advancing while every still-present video's `last_synced_at` keeps
moving forward -- a caller can already tell "not reconfirmed this cycle"
from a stale `last_synced_at` relative to the Channel's other rows,
without a second signal to keep consistent. This also keeps
`synced_video.id` permanently stable, which `video_schedule_match`
(#1581) depends on via FK. Revisit only if a positive "confirmed gone"
signal turns out to be needed for FR18 collision detection or a UI
surface -- at that point add the column via a new migration rather than
overloading `last_synced_at`.

**FR17 authority (issue #1579):** FR17 reads "A Creator can define and
update a per-Channel pacing policy" -- read narrowly, that would gate
`set_pacing_policy` on `store.CanApprove` (Creator-only), matching FR19's
explicit "An Analyst cannot approve a draft." FR17 carries no matching
explicit exclusion, though, unlike FR19 -- and the Analyst already holds
write authority over every other drafting-adjacent surface (research
notes, verdicts, `save_schedule_draft` itself) with no approval power of
its own. Gating pacing policy -- a planning input, not an approval action
-- more restrictively than the drafting tools it informs would be an
inconsistent authority model with no stated product reason. `mcp/tools/
schedule_draft.go`'s `set_pacing_policy` therefore reads FR17's "a Creator
can" as "at least a Creator can" and is gated on `store.CanWrite`
(Creator and Analyst both), the same as `save_schedule_draft`,
`save_research_note`, and `save_viability_verdict`. Revisit only if
product feedback says otherwise -- switching the gate to `store.CanApprove`
is a one-line change in `registerSetPacingPolicy`.

**Outcome matching: confidence threshold (issue #1581, FR21-FR23):**
`worker/sync.Activities.SyncOutcomes` scores every newly-published
`synced_video` against the Channel's committed, still-unmatched
`schedule_entry` rows (`worker/sync.Match`, `matching.go`) and combines
title similarity and publish-date proximity into a single `confidence` in
`[0,1]`:

- **Title similarity (weight 0.7):** the Jaccard index (intersection over
  union) of the video's title and the candidate's bound idea title, each
  normalized (lowercased, punctuation stripped, English stopwords
  dropped) into a word set -- 1.0 for identical normalized word sets
  (including pure case/punctuation differences), 0.0 for no shared words.
- **Publish-date proximity (weight 0.3):** 1.0 for the video's
  `published_at` landing exactly on the candidate's `proposed_publish_at`,
  decaying linearly to 0.0 at a 14-day separation (either direction) and
  staying 0.0 beyond it.

**`worker/sync.MatchConfidenceThreshold = 0.8`** is the value at or above
which SyncOutcomes auto-links (`video_schedule_match.state = 'auto'`,
FR22); below it (including "no plausible candidate at all", scored 0) the
match is queued `pending` for a human via `resolve_pending_match`
(FR23) -- never guessed. Title is weighted more heavily than date because a
Channel's pacing policy can legitimately slip a video's actual publish date
by days without it being a different upload (FR18), whereas two
differently-titled videos landing on the same day are a real ambiguity far
more often than a false negative; the combined score only clears 0.8 when
BOTH the title match is strong and the dates are close, which is the
"confident enough to skip human review" bar FR22 requires. `0.8` is a
starting value, not a permanent one -- it lives in exactly one place
(`worker/sync/matching.go`'s `MatchConfidenceThreshold` constant) specifically
so retuning it against real match outcomes later is a one-line change, no
call site touches the literal. See `matching.go`'s doc comments and
`matching_test.go` (issue #1581's Testing phase) for the boundary cases
this value was checked against.

A video already carrying a SETTLED `video_schedule_match` row -- auto,
confirmed, or rejected in any case, or pending with a real
`schedule_entry_id` -- is skipped by SyncOutcomes on every later cycle
(`MatchStore.HasMatch`) -- matching never re-links or duplicates. A
`rejected` match's video stays unmatched by default; nothing in M1
automatically re-queues it (that would require an explicit future re-queue
tool, not built here).

**Bug fix (issue #1652): the no-candidate placeholder is not settled.** A
`pending` row with `schedule_entry_id IS NULL` means no committed
`schedule_entry` existed as a candidate at all when the video was first
scored -- most commonly a backdated/historical video synced before its
matching `schedule_entry` was ever committed. `HasMatch` deliberately
reports false for this row (unlike every other state), so the video is
re-scored on every later `SyncOutcomes` cycle until either a real candidate
appears or a human explicitly rejects it via `resolve_pending_match`.
`MatchStore.Record` upserts on `video_schedule_match_synced_video_id_live`
(migration 002's partial unique index) so a later re-score updates that same
placeholder row in place instead of colliding with the unique index or
leaving a stale duplicate; the `DO UPDATE ... WHERE` clause is scoped so a
conflicting row that already carries a real `schedule_entry_id` is left
untouched.

Migration 003 (`003_web_session.up.sql`, issue #1570) lands `web_session`
-- C1's Google sign-in session store (see "OAuth grants" above).

Migration 004 (`004_channel_credentials.up.sql`, issue #1571) lands
`channel_credential` -- C2's per-Channel YouTube OAuth token store (see
"OAuth grants" above), SCD2 per `AGENTS.md`: exactly one live row per
Channel (`UNIQUE ... WHERE valid_to IS NULL`), a reconnect closes the old
row and opens a new one.

Migrations 005-007 land `mcp_credential` (original bespoke shape, then
migrated onto `mcpauth`'s contract) and `mcpauth`'s OAuth2 client-registry/
auth-code tables -- see "MCP server: caller authentication" above; no
`channel_person`/`channel_invite` change.

Migration 008 (`008_strategy.up.sql`, issue #1637) is covered above, in
this same "Data model" section (`strategy`/`strategy_verdict`, the
Strategy-driven `generate_schedule_plan` read tool).

**Migration 009 (`009_co_creator_tier.up.sql`, issue #1713, M2's C13)**
lands the third authority tier and its supporting attribution/uniqueness
machinery, additive-only throughout (NFR6): no existing `channel_person`
or `channel_invite` row is `UPDATE`d or `DELETE`d, `creator` keeps its
exact M1 meaning (Founder) unchanged with no backfill, and no row is
ever backfilled to `co_creator`.

- **Third tier as one more CHECK value (NFR6/NFR7):** `channel_person`'s
  auto-generated `channel_person_role_check` constraint (Postgres names
  an unnamed inline CHECK `<table>_<column>_check`) is dropped and
  replaced with `CHECK (role IN ('creator', 'co_creator', 'analyst'))`.
  Nothing else about the column, the table, or `store.Role`'s Go type
  changes shape for this -- `RoleCoCreator` is a new `store.Role`
  constant (`store/models.go`) and nothing compares roles by rank or
  order anywhere in the codebase (`containsRole`/`hasRole` in
  `store/authz.go` are pure set-membership checks). **NFR7's guarantee
  is exactly this shape:** a future fourth tier is one more CHECK value
  in a migration plus one more Go constant, never a data migration that
  would lose existing role history -- nothing in this schema or in
  `store`'s authorization functions assumes exactly three tiers can ever
  exist.
- **Grant/revoke attribution (FR34):** `channel_person` gains
  `granted_by_person_id` and `revoked_by_person_id`, both nullable (no
  actor is invented for pre-M2 rows). Per `AGENTS.md`'s SCD2
  close-and-open convention, `granted_by_person_id` is written once at a
  row's `INSERT` (`store/role.go`'s `addRoleTx`, the only two write paths
  that ever touch `channel_person` being `addRoleTx` and `RoleStore.
  RemoveRole`) and `revoked_by_person_id` is written once, together with
  `valid_to`, at the closing `UPDATE`. **Known gap (issue #1787, out of
  scope for #1728):** a promotion's implicit revoke-half -- `addRoleTx`
  closing a Person's old Analyst row in the same call that opens their
  new Co-Creator row -- does not thread an actor through to that closing
  `UPDATE`, so that one `revoked` audit event always renders with no
  actor (`"unknown"`, never fabricated). This is real, current, and
  accepted for M2; `//audience_score_system/citest`'s M2 end-to-end test
  asserts it explicitly as expected behavior rather than treating it as
  a surprise.
- **Founder-uniqueness DB backstop (NFR10):** a partial unique index,
  `channel_person_channel_id_founder_current` on `(channel_id) WHERE
  role = 'creator' AND valid_to IS NULL`, enforces FR29's "exactly one
  Founder per Channel" at the database level -- belt-and-suspenders
  alongside the fact that no FR path other than Channel-connect (FR25)
  ever grants `creator`.
- **Per-`(channel_id, role)` live-invite uniqueness (NFR11):**
  `channel_invite` gains a `role` column (`co_creator` or `analyst` --
  `creator` is never a valid invite role, since no invite path ever
  grants Founder), defaulted to `'analyst'` so M1's pre-existing rows
  read as exactly what they always were (Analyst invites). The live-invite
  uniqueness index is rescoped from `channel_invite_channel_id_live` on
  `(channel_id)` to `channel_invite_channel_id_role_live` on `(channel_id,
  role)`, so a live Co-Creator invite and a live Analyst invite coexist on
  one Channel (FR30) instead of one live invite total per Channel.
- **`v_channel_person_audit` (FR35):** a `UNION ALL` view -- one row per
  grant *event* (every `channel_person` row, at `valid_from`) unioned with
  one row per revoke *event* (every CLOSED `channel_person` row, at
  `valid_to`) -- per `AGENTS.md`'s SCD2 "Views" convention: the join from
  `channel_person` to `person` (subject, and separately the granter/
  revoker) lives here once, not re-derived per call site.
  `store.AccessStore.AuditTrail` selects from this view directly rather
  than hand-rolling the same union in Go. Ordering (most-recent-first, per
  FR35) is the caller's: `store.AccessStore.AuditTrail` sorts
  `occurred_at DESC`.
