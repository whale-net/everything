# OAuth grants

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
synced row (`synced_video`/`video_metrics`/`video_script` are never
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
lands first creates the schedule, and any later call for the same Channel
reconciles the existing schedule to match rather than erroring. The call
is best-effort and non-fatal from `web`'s HTTP request path: the Channel
is already correctly `connected` in Postgres by the time it runs, so a
transient Temporal failure here logs a warning and still redirects,
degrading to "worker's next startup Reconcile will pick it up" rather
than turning an otherwise-successful connect into a 500.

**Interval-consistency caveat (updated by issue #1742):** `EnsureSchedule`
now builds its desired `client.ScheduleOptions` and hands them to
`temporallib.UpsertSchedule` (`//libs/go/temporal`, promoted out of this
package by #1742 -- see "Temporal: schedule upsert helper" below), which
tries `Create` and, on Temporal's already-exists response, patches the
existing schedule's `Spec`/`Action`/`Overlap` via `ScheduleHandle.Update`
instead of leaving them at whatever a prior caller set. So a Channel's
schedule interval is no longer permanently pinned to whichever
`EnsureSchedule` call created it first -- a later call (e.g. `worker`
restarting after `ASS_SYNC_INTERVAL` changes, or `web` handling a
reconnect) reconciles it to the current `Interval`. `web` and `worker`
must still load `ASS_SYNC_INTERVAL` with the identical default and the
identical `sync.ValidateSyncInterval` band-check (see `ENV.md`
"Temporal"): a persistent mismatch no longer just loses the race
permanently to whichever binary connects first -- it makes the effective
interval flip-flop between whichever binary's `EnsureSchedule` call ran
most recently.

**NFR3 check for this change:** NFR3 (below) restricted `web`, as of this
note's writing, to three *UI-only* surfaces (C1/C2/C3) -- calling
`EnsureSchedule` from `HandleCallback` adds no new HTTP route, no new MCP
tool, and no new capability visible to a user or agent; it is backend
plumbing inside the already-allocated C2 (Channel-connect) surface, exactly
analogous to `web` already writing `channel_credential` and
`connection_state` as part of that same flow. NFR3 stands unmodified for
C2 specifically. (This predates issue #1648's NFR3 amendment, which moved
C8 off the `web`-only list entirely, and predates the batch covered by
issue #2039's amendments below, which grew the `web`-only count from three
to six (C1/C2/C3 plus C18's edit slice, C20, C21) -- the citations here are
updated to match, but the `EnsureSchedule` reasoning itself, scoped only to
C2, is unaffected by either.)

