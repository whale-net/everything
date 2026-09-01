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

Everything else is not yet designed — later schema tasks land research
notes, verdicts per LB3's FK chain, schedule drafts/committed entries,
synced schedule/metrics, and pending matches.
