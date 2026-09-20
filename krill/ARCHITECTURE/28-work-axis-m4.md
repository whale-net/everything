# The work axis (M4): task, claim, lease, attempt, note (root plan issue #2717, conformance issue #2728)

M4 (issues #2719-#2727) is a single self-contained axis — a task, its
claim, and everything an Agent touches while it holds one — hanging off
the delivery axis (M3) rather than the spec axis. This section is the
whole-milestone view; "The work axis: task creation" and "The task
payload document" above go deeper on FR1 and FR4/FR10 specifically.
`krill/conformance/work_axis_integration_test.go` (issue #2728) is the
one place all of it is proven connected end to end, plus a mechanical
audit of every NFR named below — see its own doc comment and
`krill/TOC.md`'s entry for how to run it.

**One migration, six tables (issue #2719).** `015_work_axis` is the one
migration number reserved for `task`, `task_dependency`, `task_claim`,
`task_lease_event`, `task_attempt`, and `task_note` together, rather than
one migration per task as M1-M3 mostly did. The reason is concurrency, not
convenience: M4's nine tasks (#2719-#2727) land independently on trunk,
often in parallel, and golang-migrate numbers migrations sequentially —
two tasks each proposing their own next-numbered migration risks a gap or
collision the moment they merge close together. Reserving one number for
the whole axis up front (mirroring M1's own up-front numbering note in
`schema.go`'s package doc, scaled from one task to a whole milestone) means
every task after the first states "no new migration" and only adds
store/handler/MCP/test code against tables that already exist.

**Three row-lifecycle shapes, not two.** NFR2 rules out SCD2 for this
axis, but "not SCD2" still leaves more than one legitimate shape, and the
migration's own comments name which of the three each table is:

- **Append-only-plus-claimed** — `task` alone. Written once at
  `CreateTask`; thereafter updated in place only for the fields a live
  claim mutates (`current_claim_id`, `lease_expires_at`, `current_lane`,
  `attempt_count`). This mirrors `tools/app_registry`'s `writeback_outbox`
  (cited in `krill/product/01-current-state.md`) — one row per unit of
  work, claimed and released in place — rather than either SCD2 or a
  plain immutable row. Every claim, heartbeat, and attempt `task` itself
  has ever seen lives as history on a sibling table below; `task` only
  ever answers "what is true right now".
- **Append-only with one narrow closure exception** — `task_claim` alone.
  Every column but one is written once at INSERT and never touched again;
  the one exception is claim closure (`released_at`/`release_reason`),
  set exactly once when a claim ends (FR7 reclaim, FR8 complete, FR9
  abandon) and never re-set or cleared afterward.
- **Plain append-only** — `task_dependency`, `task_lease_event`,
  `task_attempt`, `task_note`. No column on any of these four is ever
  updated after INSERT; each is a pure history log a `task` or
  `task_claim` row's own "current value" columns are derived from
  (`task.attempt_count` from `task_attempt`, `task.lease_expires_at` from
  the latest `task_lease_event`, mirroring `milestone_status_event`'s
  identical "history on a sibling table, current value on the parent"
  split from migration 012).

**The claim payload is enrichment, never a second projection (LB7,
NFR4).** `GET /tasks/{id}` (FR4/FR10), `claim` (FR3/FR5), `complete`
(FR8), and `abandon` (FR9) all return the exact same `work.Payload`
document — never a bespoke per-verb response shape. `work.Assembler.
Assemble` builds it by calling `slice.Querier.GetMilestoneDeliversSlice`
for the spec-axis half (`Payload.Slice`, `slice.Document` embedded
verbatim) and `store.TaskStore`'s own reads for the work-axis half
(lane state, dependencies, current claim, notes) — it never issues its
own `SELECT` against `feature`, `requirement`, `load_bearing_decision`,
or `entity_milestone`. A change to the spec-axis document's shape is
therefore a change this one enrichment layer picks up automatically,
never a second, independently maintained copy that can drift from it —
see "The task payload document" above for `Assemble`'s own read sequence,
and this file's NFR4 conformance test (`work_axis_integration_test.go`)
for the byte-equality regression check that guards it.

**Lane routing lives in the task's own `lane_sequence`, never in the
completing Agent's request (FR8).** `store.NextLane` (`task_complete.go`)
is a pure function of two inputs only — the task's own `lane_sequence`
(FR1) and its `current_lane` — with no third, caller-supplied destination
input to read even if a client tried to supply one:
`CompleteTaskParams` carries no lane/destination field of any kind, a
compile-time guarantee stronger than a runtime rejection. A passing
verdict advances to the next lane in sequence (or `Done`, if `current` is
already the sequence's last element); a failing verdict reverts to the
lane immediately preceding `current` (or leaves it unchanged, if
`current` is already the sequence's first element). Because a task's
`lane_sequence` is data set once at `CreateTask` and never a global
constant, two tasks with different sequences (a docs-only task skipping
`Scaffold`, say) get correct, independent routing from the exact same
function.

**The attempt cap and lease duration are provisional Go constants, not
config (FR7, out of scope note in root plan #2717).**
`DefaultAttemptCap` (3) and `DefaultLeaseDuration` (15 minutes,
`task_claim.go`) are the one place each value is defined — `ClaimTask`,
`ReclaimExpired` (FR7's lease-expiry sweep), and `AbandonClaim` (FR9) all
read the same constant rather than each carrying its own number or a
per-task/per-scope override. A task whose `attempt_count` reaches the cap
is refused re-service (`ErrAttemptCapExhausted`) and left claimed by no
one, with no escalation queue, human-attention surface, or operator
console in this milestone — root plan #2717 names M5's C26 as the owner
of the real cap value, the escalation destination, and the human-facing
surface for that terminal state; this milestone only ships the invariant
that a cap exists and reclaim refuses past it.

