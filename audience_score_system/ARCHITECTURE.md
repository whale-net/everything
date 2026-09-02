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

## Component map

| Component | Binary | `release_app` identity | Responsibility |
|---|---|---|---|
| `migrate` | `audience_score_system/migrate` | `migration` (job) | Applies golang-migrate SQL migrations to Postgres. Runs once, ahead of the other three, as a Helm job hook (see `libs/go/migrate/README.md`). |
| `web` | `audience_score_system/web` (scaffold only, #1570) | `web` (external-api) | The **only** UI surface. Limited to C1/C2/C3/C8 (see "NFR3 interface allocation" below). |
| `mcp` | `audience_score_system/mcp` (not yet built) | `mcp` (external-api) | Every other capability (C4-C7, C9, C10): research notes, viability verdicts, schedule sync reads, schedule draft proposals, pacing policy, outcome-match confirm/reject, and all browsing. Exposed as MCP tools to any MCP-capable agent client. |
| `worker` | `audience_score_system/worker` (not yet built) | `worker` (worker) | Per-Channel Temporal scheduled workflow: syncs YouTube schedule (C6) and published-video metrics (C9) on a ~15-30 minute cadence (NFR4). Skips a cycle for a disconnected/needs-reauth Channel without erroring the workflow. |
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

`//libs/go/temporal` (the shared Go bootstrap `worker` will use) has no
equivalent to `friendly_computing_machine`'s hand-rolled
`AbstractScheduleWorkflow` (`temporal/base.py`) — the only in-repo
*scheduled*-workflow precedent, and it's Python/FCM-specific, not a shared
library. This is a gap `worker` inherits from `product/01-current-state.md`
and knowingly accepts for M1: the per-Channel sync schedule (NFR4, ~15-30
minute interval) will be built directly against the Temporal Go SDK's
native `ScheduleClient`, not a repo-shared helper. Worth revisiting if a
second Go scheduled-workflow consumer shows up — at that point the
duplication is worth generalizing into `//libs/go/temporal`.

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
