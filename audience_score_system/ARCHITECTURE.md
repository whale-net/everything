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

**Decision (resolved for #1575's Scaffold phase, before any of this task's
Implementation work landed):** an MCP client authenticates as a Person
with a bearer credential that `mcp`'s auth middleware
(`audience_score_system/mcp/server/auth.go`) resolves to `person.id`
server-side. The credential is a high-entropy random token; only its
SHA-256 hash is ever persisted, in `mcp_credential` (migration 005,
`audience_score_system/store/credential.go`'s `CredentialStore`) — the raw
token is shown to the Person exactly once, at mint time, and is never
recoverable from the database afterward.

- **(a) Obtained:** minted by an authenticated endpoint on `web`, reusing
  the Person's existing C1 sign-in session (`web/auth`'s
  `RequireSignedIn`) rather than any new credential-collection UI. That
  endpoint is not yet built — this task lands the schema and store
  interface (`CredentialStore.Mint`) it will call; adding the endpoint
  itself is filed as a scope note (no other M1 task owns it).
- **(b) Revoked:** `CredentialStore.Revoke` closes a credential by setting
  `revoked_at`; a revoked credential's hash no longer resolves in
  `VerifyTokenHash`, so any MCP call bearing it is rejected on the next
  request without needing to invalidate anything client-side. Revocation
  is idempotent (NFR2) — revoking an already-revoked or nonexistent
  credential is not an error.
- **(c) NFR3 rationale:** minting a credential is sign-in machinery — it
  bootstraps an already-authenticated Person's access to `mcp`, the same
  way `web/auth`'s OAuth callback bootstraps access to `web`. It performs
  none of C4-C10's actions itself (no research notes, verdicts, schedule
  drafts, pacing, outcome confirmation, or browsing happen at mint time),
  so it does not grow a new capability surface on `web` and NFR3 stands.

Mechanically, resolution happens in two layers (see
`audience_score_system/mcp/server/`):

1. **HTTP layer** (`transport.go`): `auth.RequireBearerToken` (from the
   vendored SDK's `auth` package) wraps the streamable HTTP handler,
   calling `TokenVerifier` (`auth.go`) to hash the raw token and resolve it
   via `CredentialStore.VerifyTokenHash`, producing an `auth.TokenInfo`
   whose `UserID` is the resolved Person's ID. Credentials do not expire on
   a timer (they live until revoked), so `AllowMissingExpiration: true` is
   set rather than requiring a per-token `exp` claim.
2. **MCP-protocol layer** (`server.go`/`auth.go`): `PersonMiddleware`, wired
   via `mcp.Server.AddReceivingMiddleware`, reads that `TokenInfo` off each
   request's `RequestExtra`, resolves the full `store.Person`, and places
   it on the handler's `context.Context` (`PersonFromContext`,
   `context.go`). A request with no resolved `TokenInfo`, or a `UserID`
   that doesn't resolve to a real Person, is rejected here — the tool
   handler is never entered.

`CredentialStore.VerifyTokenHash`, `Mint`, `Revoke`, and `ListForPerson`
(issue #1575's Implementation phase) are real SQL-backed implementations
against `mcp_credential` — `VerifyTokenHash` also stamps `last_used_at` in
the same round trip, so it doubles as the "last seen" signal for a future
credential-management view (see issue #1591's scope note).

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
  whose input carries no `channel_id` (only `whoami` today) simply doesn't
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

## Component map

| Component | Binary | `release_app` identity | Responsibility |
|---|---|---|---|
| `migrate` | `audience_score_system/migrate` | `migration` (job) | Applies golang-migrate SQL migrations to Postgres. Runs once, ahead of the other three, as a Helm job hook (see `libs/go/migrate/README.md`). |
| `web` | `audience_score_system/web` (scaffold only, #1570) | `web` (external-api) | The **only** UI surface. Limited to C1/C2/C3/C8 (see "NFR3 interface allocation" below). |
| `mcp` | `audience_score_system/mcp` (scaffold only, #1575) | `mcp` (external-api) | Every other capability (C4-C7, C9, C10): research notes, viability verdicts, schedule sync reads, schedule draft proposals, pacing policy, outcome-match confirm/reject, and all browsing. Exposed as MCP tools to any MCP-capable agent client. |
| `worker` | `audience_score_system/worker` (scaffold only, #1574) | `worker` (worker) | Per-Channel Temporal scheduled workflow: syncs YouTube schedule (C6) and published-video metrics (C9) on a ~15-30 minute cadence (NFR4). Skips a cycle for a disconnected/needs-reauth Channel without erroring the workflow. |
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

**Reconnect authorization (FR4, NFR5):** only a Person holding a live
`role=creator` `channel_person` row on a Channel may (re)connect it --
checked via `store.CanReconnect`, the same sanctioned authz entry point
every other M1 permission check uses (see "Data model" below). This is
enforced by `web/channel.Handler.HandleReconnect`, never inferred from
who initiated the original connect.

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

## NFR3 interface allocation

The web UI is **limited to exactly four surfaces** — everything else is
MCP-only, per the product brief's MCP-agent-first interface decision:

- **C1** — OAuth signup/login (Google OAuth consent → Person record).
- **C2** — Channel connect (YouTube OAuth consent → Channel + `role=creator`
  join row, LB2).
- **C3** — Analyst invite/accept (invite code generation and
  accept/decline).
- **C8** — Schedule-draft approval/revocation (Creator-only approve,
  un-approve, edit, re-approve up until publish).

Every other capability — C4 (research notes), C5 (viability verdicts), C6
(schedule sync reads), C7 (schedule drafting, pacing policy), C9 (outcome
comparison, pending-match confirm/reject), C10 (browsing) — is exposed only
through `mcp` tools. `web` must never grow a UI surface for these; if a
future milestone needs one, that's a scope change to NFR3, not an
implementation detail to slip in under an existing task.

## Temporal: no scheduled-workflow helper yet

`//libs/go/temporal` (the shared Go bootstrap `worker` uses for its
client/worker construction) has no equivalent to
`friendly_computing_machine`'s hand-rolled `AbstractScheduleWorkflow`
(`temporal/base.py`) — the only in-repo *scheduled*-workflow precedent, and
it's Python/FCM-specific, not a shared library. This is a gap `worker`
inherits from `product/01-current-state.md` and knowingly accepts for M1:
issue #1574 built the per-Channel sync schedule (NFR4, ~15-30 minute
interval) directly against the Temporal Go SDK's native `ScheduleClient`
(`audience_score_system/worker/sync.ScheduleManager`, wrapping
`client.Client.ScheduleClient()`), not a repo-shared helper. `ScheduleManager`
is a small, three-method interface (`EnsureSchedule`/`RemoveSchedule`/
`Reconcile`) with nothing app-registry-specific about its shape — worth
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
`CanApprove`/`CanInvite`/`CanReconnect`/`CanRead`/`CanWrite` (NFR5), the
only sanctioned authorization entry points.

Migration 002 (`002_research_schedule_outcome.up.sql`, issue #1569) lands
the LB3 record chain: idea, research notes, viability verdicts (append-only
version log, not SCD2 -- see `AGENTS.md`'s SCD2 event-log exclusion),
pacing policy, schedule entries, synced videos/metrics, and pending
matches, plus the `mcp_idempotency` ledger (NFR2/LB4).

Migration 003 (`003_web_session.up.sql`, issue #1570) lands `web_session`
-- C1's Google sign-in session store (see "OAuth grants" above).

Migration 004 (`004_channel_credentials.up.sql`, issue #1571) lands
`channel_credential` -- C2's per-Channel YouTube OAuth token store (see
"OAuth grants" above), SCD2 per `AGENTS.md`: exactly one live row per
Channel (`UNIQUE ... WHERE valid_to IS NULL`), a reconnect closes the old
row and opens a new one.
