# The escalation/intervention/console axis (M5): stuck, escalated, and acted on (root plan issue #2851, conformance issue #2877)

M5 (issues #2867-#2876) is a single self-contained axis hanging off the
work axis (M4): a task that cannot make progress on its own — thrashing
between lanes, sitting past its attempt cap, or judged stuck by a human —
becomes visible and actionable **from krill alone**, with no hand-edited
row. This section is the whole-milestone view, the same role
`ARCHITECTURE/28-work-axis-m4.md` plays for M4;
`ARCHITECTURE/30-operator-release-and-manual-escalate.md` goes deeper on
FR8/FR9 specifically. `krill/conformance/escalation_console_integration_test.go`
(issue #2877) is the one place all of it is proven connected end to end,
plus the NFR1-NFR6 audit named below — see its own doc comment and
`krill/TOC.md`'s entry for how to run it.

## Component map addendum

M5 adds no new binary and no new component box to the top-level map in
`ARCHITECTURE.md` — it widens `api` (new routes), `krill/store` (new
tables, new `TaskStore` methods), and `mcp` (a **third** MCP mount,
`/mcp/ops`, alongside `specMountPath`/`designMountPath`):

```
        mcp
   ┌─────────────┬──────────────┬─────────────┐
   │  /mcp/spec  │ /mcp/design  │  /mcp/ops   │
   │ (every      │ (every       │ (Persona-   │
   │  resolved   │  resolved    │  SwarmOper- │
   │  persona)   │  persona,    │  ator only, │
   │             │  per-tool    │  the mount  │
   │             │  allow-list) │  itself is  │
   │             │              │  the gate)  │
   └─────────────┴──────────────┴─────────────┘
```

One migration, `016_escalation_axis` (issue #2868), carries the whole
axis — `task_escalation_event`, `task_intervention_event`,
`task_note_lifecycle_event`, plus additive columns on `task`
(`thrash_count`, `current_escalation_id`, `cancelled_at`) and `task_note`
(`current_status`) — for the identical concurrency reason
`015_work_axis` gives in `ARCHITECTURE/28-work-axis-m4.md`: M5's ten
tasks land independently and often in parallel, and reserving one
migration number up front avoids a golang-migrate version collision.

## The escalation state machine: three reasons, one state, one active event per task

A task is either `active` or `escalated` (`store.TaskState`) — never a
richer state machine than that. `escalated` is derived, always, from
`task.current_escalation_id IS NOT NULL`; it is never a separate stored
flag that could drift from that column. Exactly one reason ever produces
it:

- **`thrash-cap`** (FR1, FR2) — `CompleteTask` (`task_complete.go`)
  increments `task.thrash_count` on every *failing* verdict, total, never
  consecutive-only, and never resets it on an intervening pass. The
  moment that count reaches `store.DefaultThrashCap`, the same
  transaction that would otherwise revert the lane instead holds the task
  at its current lane and calls `recordEscalationTx`.
- **`attempt-cap`** (FR3) — `ReclaimExpired`, `AbandonClaim`, and
  `ReleaseLease` all count a lapse/abandon/release against the exact same
  `store.DefaultAttemptCap` M4 already enforced at claim time (`ClaimTask`'s
  own `ErrAttemptCapExhausted` check); once an increment reaches the cap,
  the same call records the escalation in the same transaction, so the
  cap is never crossed with no escalation event explaining it.
- **`manual`** (FR9) — `EscalateTask` records this reason with
  `counter_value`/`cap_value` both `NULL` — a human's judgment call has no
  triggering counter to report.

`recordEscalationTx` (`task_escalation.go`, issue #2868) is the one
shared write path all three reasons call — never a per-reason copy of the
same INSERT — and it enforces FR9's one-event rule structurally: it
refuses (`ErrTaskEscalated`) to escalate a task that already has one
active, under the same row lock every other work-axis verb takes. A task
therefore never carries two concurrently-active escalation events, which
is exactly what keeps FR6's "reset the counter whose cap triggered *the*
escalation" unambiguous — see the next section's cross-reference to
`30-operator-release-and-manual-escalate.md` for the one exception this
rule interacts with (`escalate` against a task already at
`DefaultAttemptCap`).

An escalation is resolved by exactly one of two verbs, never by rewriting
the `task_escalation_event` row itself (NFR2, NFR5 — see this migration's
own "no resolution column, on purpose" comment):

- **`requeue`** (FR6, issue #2876) resets exactly the counter named by
  the resolved escalation's own reason (`thrash-cap` → `thrash_count = 0`,
  `attempt-cap` → `attempt_count = 0`, `manual` → neither, **unless**
  `EscalateTask`'s own force-close already pushed `attempt_count` to the
  cap — see below), clears `current_escalation_id`, and appends a
  `task_intervention_event` naming the escalation it resolved.
- **`cancel`** (FR7, issue #2873) moves the task to a dead-lettered
  terminal state instead (`cancelled_at`), leaving the resolved-or-not
  escalation's own history exactly as it was — cancel works identically
  whether or not the task happens to be escalated.

## Claimability: the predicate lives in the index, not only in Go

FR2 states the escalated-regardless-of-claim-state rule must hold "even
under concurrency" — so `016_escalation_axis`'s `task_claimable_idx`
carries all **three** predicates as one partial index, not two
independently-checked conditions:

```sql
CREATE INDEX task_claimable_idx ON task(scope_id)
    WHERE current_claim_id IS NULL AND current_escalation_id IS NULL AND cancelled_at IS NULL;
```

`ClaimTask` (`task_claim.go`) mirrors this exact predicate shape in its
own `SELECT ... FOR UPDATE`, checking `cancelled_at` and
`current_escalation_id` ahead of, and independently from, the
claim/lease claimable check — a task escalated through a normal
`complete` has `current_claim_id IS NULL` exactly like any ordinary
unclaimed task, so the escalation and cancellation predicates are
genuinely additional conditions, never implied by the claim predicate
alone. The index and the Go check are defense in depth for the same one
rule, never two different rules that could disagree.

## `/mcp/ops`: a third mount, not a per-call check on an existing one (issue #2867)

FR6-FR9 all name their actor as "a Swarm Operator" — root plan #2851's
own design note leaves *how* that restriction is enforced open ("mount
routing vs. per-call auth check... at the altitude M1 already placed
it"). M5 resolves it as a **third MCP mount**, `/mcp/ops`
(`krill/mcp/server/transport.go`'s `opsMountPath`), alongside
`specMountPath`/`designMountPath` — never a per-tool allow-list layered
onto an existing mount. `registry.go`'s `RegisterOpsRead`/`RegisterOpsWrite`
fix the allowed persona to `PersonaSwarmOperator` with no allow-list
parameter to vary, because the mount itself already answers "who may call
anything registered here" — the same way `specMountPath`'s complete
absence of `RegisterWrite` calls is what keeps that mount read-only.
"No write/read tool crossover on a mount" stays a per-mount,
grep-verifiable invariant, never a per-tool one to audit individually.

Both M5's operator verbs (`release`, `escalate`, `requeue`, `cancel` —
write) and M5's console queries (`list_claimed_tasks`,
`list_escalated_tasks`, `list_cancelled_tasks`, `list_open_notes` — read)
register on this one mount. This restriction is **MCP-only**: the
matching HTTP endpoints (`krill/api/routes.go`) keep using the existing
session-only `gate(...)` write gate (`api/handlers/gate.go`), which has no
persona concept at all — a later task must not add a second, divergent
persona check on the HTTP side; `/mcp/ops` is the one place the Swarm
Operator restriction lives.

## The console paging contract (NFR6)

Every M5 console query (`ListClaimedTasks`, `ListCancelledTasks`,
`ListEscalatedTasks`, `ListOpenNotes`) shares one paging/token contract,
built once in `krill/store/paging.go` (issue #2869) rather than each
query rolling its own:

- **Keyset (cursor) paging, never `OFFSET`.** Each query defines its own
  deterministic total order (e.g. `ListCancelledTasks`'
  `created_at DESC, task.id DESC`) and encodes the last row's own sort
  key plus id as the resume position — a page boundary this way stays
  stable under concurrent writes, unlike an `OFFSET` that a concurrent
  insert/delete can shift out from under a caller mid-walk.
- **Bounded, with a caller-adjustable size.** `PageParams.PageSize`,
  resolved by `ResolvePageSize`: absent/zero → `DefaultConsolePageSize`
  (25); above `MaxConsolePageSize` (100) → **clamped down**, never
  rejected — a Swarm Operator asking for "everything" gets the largest
  page this API hands back in one round trip, not an error to retry with
  a smaller number.
- **Scope-qualified continuation tokens (LB1, NFR1).**
  `EncodeContinuationToken` embeds the issuing scope id inside the opaque
  token; `DecodeContinuationToken` rejects a token whose embedded scope id
  differs from the scope the resumed query is running against
  (`ErrTokenScopeMismatch`) **before** ever handing back its cursor value.
  A token issued under one scope can never become a path across a scope
  boundary into another, even by accident — this is exactly what
  `escalation_console_integration_test.go`'s Step 11 exercises against
  all three working console queries (see "Known defect" below for the
  fourth).
- **A page's `NextToken` is `""` exactly when no rows remain** — every
  query fetches one row beyond the requested page size and trims it off,
  so "is there a next page" is answered by that extra row's presence,
  never by a second `COUNT(*)` round trip.

## Two design-ambiguity resolutions (root plan #2851's design notes)

**`escalate` against an already-escalated task is rejected, not a silent
no-op.** Documented in full in
`ARCHITECTURE/30-operator-release-and-manual-escalate.md`'s "Design
choice" section — summarized here for this file's own cross-reference: a
no-op would let an operator believe their `escalate` call is what put the
task in front of the console when an earlier, possibly
differently-reasoned event actually did; `EscalateTask` instead reuses
`ErrTaskEscalated` (the same error `ClaimTask` already reports for the
identical underlying condition) so "the one escalation event this task
has" stays a question with exactly one accurate answer.

**The widened `task_attempt.outcome` values are `released` and
`force-closed`, and only those two.** M4 shipped `claimed`, `lapsed`,
`abandoned`, `completed`; migration 016 widens the CHECK to add exactly
two more, one per new attempt-counting path this milestone introduces:

- `released` — `ReleaseLease` (FR8, Assumption 2: a manual release counts
  as an attempt against the same cap `ClaimTask`/`ReclaimExpired`/
  `AbandonClaim` already enforce, so an operator cannot dodge the cap by
  releasing instead of letting the lease lapse).
- `force-closed` — `EscalateTask`'s own force-close of a live claim (FR9,
  Assumption 7: the force-close itself counts as an attempt, or an
  escalate-then-requeue round trip would reach `release`'s exact end state
  without ever touching the counter `release` protects).

`requeue` and `cancel` deliberately get **no** new outcome value:
`requeue` is explicitly not an attempt (FR6, Assumption 7 — it never
inserts a `task_attempt` row or touches `attempt_count` at all), and
`cancel` is a terminal state with no "how did the claim end" question left
to answer (its own force-close, if any, is the intervention event itself,
not a run-attempt outcome). Migration 016's own comment on this CHECK
states it plainly: "this is the exact set later M5 tasks must not widen
again" — a seventh value would need a new migration and a new load-bearing
decision, never an in-place edit to this CHECK.

## Known defect: `ListClaimedTasks` (FR4) is an unimplemented stub

`store.TaskStore.ListClaimedTasks` (`krill/store/task_console.go`) was
scaffolded in issue #2869 alongside `paging.go` and the console
HTTP/MCP wiring, but that issue closed with the store method itself left
as a permanent `return Page[ClaimedTaskRow]{}, ErrNotImplemented` — unlike
its three siblings (`ListCancelledTasks`/`ListEscalatedTasks`/
`ListOpenNotes`, issues #2873/#2875/#2874), which are fully implemented.
`GET /console/claimed` and the `list_claimed_tasks` MCP tool are both
wired end-to-end in production and both fail on every call, in every
scope. `escalation_console_integration_test.go`'s Step 2 documents this
explicitly (asserting the current `ErrNotImplemented` behaviour, not the
intended one) rather than silently passing FR4 or failing the whole
conformance walk; the fix itself is tracked as a follow-up,
issue #2916, per this task's own "do not fix a defect the walk
uncovers" scope.
