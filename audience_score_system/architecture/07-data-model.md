# Data model

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
matches, plus the `mcp_idempotency` ledger (NFR2/LB4). Migration 015
(`015_verdict_source.up.sql`, M4.1 FR5/NFR4, issue #1898) adds
`viability_verdict.source` (`agent` or `human`, `NOT NULL DEFAULT
'agent'`) -- an authorship marker distinguishing a verdict written via
MCP's `save_viability_verdict` from one written via the web save-verdict
form (#1901), on the record itself; the `DEFAULT` deterministically
backfills every pre-existing row to `agent` in the same statement that
adds the column (NFR4).

Migration 008 (`008_strategy.up.sql`, issue #1637) lands `strategy` and
`strategy_verdict`. A Strategy is built directly from one or more
`viability_verdict` rows via `strategy_verdict` -- not from Ideas:
`idea_id` is derived through `viability_verdict.idea_id` rather than
stored on the join row, the same LB3 pattern `video_script.verdict_id`
uses one layer downstream, applied to the join itself. The relationship
is many-to-many in both directions -- a Strategy is typically grounded in
several verdicts (often several Ideas), and the same verdict may ground
more than one Strategy -- which is the point: `save_strategy` records
exactly which analysis justified the grouping, not just "whichever idea
is currently viable."

**Cadence and `generate_schedule_plan` removed (FR47, issue #1833):**
migration 008 originally gave `strategy` a `cadence` (weekly/biweekly/
monthly, optional preferred weekday), and `generate_schedule_plan`
(`mcp/tools/strategy.go`) read it -- together with the Channel-wide
`pacing_policy` (FR17) -- to propose next-slot schedule entries,
reconciling the two via a `pacingTracker` so a plan's proposals agreed
with what `save_schedule_draft` would flag at commit time (FR18's
`cadence_exceeded`). M2.1 retires this pacing/calendar surface outright
(FR41/FR47): migration 011 drops `strategy.cadence` and
`generate_schedule_plan` is deleted, not retargeted. This is a deliberate
"removed, not deferred" outcome -- not a placeholder for a successor
field or tool. A Strategy today is purely a grouping of viable verdicts
under a title and an active flag; `preferred_weekday` is unaffected (no
FR removes it), and `strategy_id` on `video_script` still resolves a
Strategy for context grouping (FR36, LB3). A Strategy-driven cadence/
auto-proposal capability, if ever wanted, is new capability work for a
later milestone, not a re-add of the dropped column.

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

**FR17 authority (issue #1579, historical):** FR17 (per-Channel pacing
policy) and the `set_pacing_policy` tool this note explained the authority
gate for are retired outright by the video-script-model milestone (FR41,
issue #1832) -- `mcp/tools/schedule_draft.go` no longer exists. No
successor capability carries a pacing-policy authority question forward.

**Outcome matching: confidence threshold (issue #1581, FR21-FR23; re-anchored
onto `video_script` by FR43/FR44, milestone video-script-model, issue
#1829):** `worker/sync.Activities.SyncOutcomes` scores every newly-published
`synced_video` against the Channel's `greenlit`, still-unmatched
`video_script` rows (`worker/sync.Match`, `matching.go`) and combines
title similarity and publish-date proximity into a single `confidence` in
`[0,1]`:

- **Title similarity (weight 0.7):** the Jaccard index (intersection over
  union) of the video's title and the candidate `video_script`'s own
  title, each normalized (lowercased, punctuation stripped, English
  stopwords dropped) into a word set -- 1.0 for identical normalized word
  sets (including pure case/punctuation differences), 0.0 for no shared
  words.
- **Publish-date proximity (weight 0.3):** 1.0 for the video's
  `published_at` landing exactly on the candidate's `target_publish_date`,
  decaying linearly to 0.0 at a 14-day separation (either direction) and
  staying 0.0 beyond it. A `video_script` with no `target_publish_date`
  (FR36 makes it optional) scores this term as exactly 0 -- deliberately
  NOT renormalized to title-only, so an undated script's combined score is
  capped at `0.7*titleSim <= 0.7`, below `MatchConfidenceThreshold`
  (0.8) by construction, whatever the title match (FR43's load-bearing
  invariant): an undated script can never auto-link, only ever land
  `pending` for a human (FR44 frames manual resolution as the primary
  path for an undated script).

**`worker/sync.MatchConfidenceThreshold = 0.8`** is the value at or above
which SyncOutcomes auto-links (`video_schedule_match.state = 'auto'`,
FR22); below it (including "no plausible candidate at all", scored 0) the
match is queued `pending` for a human via `resolve_pending_match`
(FR23) -- never guessed. Title is weighted more heavily than date because a
video's actual publish date can legitimately slip by days from a script's
target date without it being a different upload, whereas two
differently-titled videos landing on the same day are a real ambiguity far
more often than a false negative; the combined score only clears 0.8 when
BOTH the title match is strong and the dates are close, which is the
"confident enough to skip human review" bar FR22 requires. `0.8` is a
starting value, not a permanent one -- it lives in exactly one place
(`worker/sync/matching.go`'s `MatchConfidenceThreshold` constant) specifically
so retuning it against real match outcomes later is a one-line change, no
call site touches the literal. See `matching.go`'s doc comments and
`matching_test.go` (issue #1581's Testing phase, extended for FR43 by
#1829) for the boundary cases this value was checked against.

A video already carrying a SETTLED `video_schedule_match` row -- auto,
confirmed, or rejected in any case, or pending with a real
`video_script_id` -- is skipped by SyncOutcomes on every later cycle
(`MatchStore.HasMatch`) -- matching never re-links or duplicates. A
`rejected` match's video stays unmatched by default; nothing in M1
automatically re-queues it (that would require an explicit future re-queue
tool, not built here).

**Bug fix (issue #1652): the no-candidate placeholder is not settled.** A
`pending` row with `video_script_id IS NULL` means no `greenlit`
`video_script` existed as a candidate at all when the video was first
scored -- most commonly a backdated/historical video synced before its
matching `video_script` was ever greenlit. `HasMatch` deliberately
reports false for this row (unlike every other state), so the video is
re-scored on every later `SyncOutcomes` cycle until either a real candidate
appears or a human explicitly rejects it via `resolve_pending_match`.
`MatchStore.Record` upserts on `video_schedule_match_synced_video_id_live`
(migration 002's partial unique index) so a later re-score updates that same
placeholder row in place instead of colliding with the unique index or
leaving a stale duplicate; the `DO UPDATE ... WHERE` clause is scoped so a
conflicting row that already carries a real `video_script_id` is left
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
this same "Data model" section (`strategy`/`strategy_verdict`, and its
FR47/#1833 cadence removal). Migration 011
(`011_drop_strategy_cadence.up.sql`, issue #1833) is the FR47 follow-up
covered there.

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
