# Operator `release` and manual `escalate` (FR8, FR9, issue #2872)

`release` (FR8, `krill/store/task_release.go`) and `escalate` (FR9,
`krill/store/task_escalate.go`) are shipped together because they share the
exact same three invariants — force-close a live claim, count an attempt,
and decide what happens when that attempt crosses `DefaultAttemptCap`
(`krill/store/task_claim.go`) — with `cancel` (`task_cancel.go`, FR7)
before them. All three call `forceCloseClaimTx`/`recordEscalationTx`
(`task_escalation.go`, issue #2868) rather than each forking its own copy.

## `release`: a manual release counts as an attempt

Root plan issue #2851's Assumption 2 is explicit: a Swarm Operator must not
be able to dodge `DefaultAttemptCap` by releasing a claim manually instead
of letting its lease lapse. `ReleaseLease` therefore inserts a `released`
`task_attempt` row and increments `task.attempt_count` in the same
transaction as the force-close — identical accounting to `ReclaimExpired`'s
`lapsed` row and `AbandonClaim`'s `abandoned` row. When that increment
reaches the cap, the attempt-cap escalation (FR3, issue #2871) is recorded
in this same transaction, never deferred to a later claim.

`ReleaseLease` refuses an unclaimed task (`ErrTaskNotClaimed`) — unlike
`forceCloseClaimTx`'s own no-op posture (a no-op when there is nothing to
close, the shape `cancel`/`escalate` want), release's whole point is to
force-close a *live* lease, so calling it against a task with no open claim
is a caller error, not a no-op.

## `escalate`: the force-close counts as an attempt, but the exception is one event, not two

`EscalateTask` records the `manual` escalation event first (`reason =
'manual'`, `counter_value`/`cap_value` both `NULL` — a manual escalation
has no triggering counter), then force-closes any open claim
(`release_reason = 'escalate'`) and counts that force-close as an attempt
(`force-closed` `task_attempt` row, `attempt_count`+1) — otherwise an
escalate-then-requeue round trip would reach `release`'s exact end state
(force-close a live claim, return the task to claimable at the same lane)
without ever touching the counter `release` protects.

FR9 states one exception to FR8's "cap crossed ⇒ attempt-cap escalation
recorded in the same transaction" rule: when this force-close is itself the
attempt that reaches `DefaultAttemptCap`, no second escalation event is
recorded. The code enforces this structurally, not with an extra branch —
the `manual` event from step 1 already occupies
`task.current_escalation_id` for the rest of the transaction, so a second
call to `recordEscalationTx` in the same transaction would itself fail with
`ErrTaskEscalated`; `EscalateTask` simply never attempts that second call.
A task therefore never carries two concurrently-active escalation events,
keeping FR6's "reset the counter whose cap triggered *the* escalation"
unambiguous. `requeue` (FR6, #2876) handles the resulting "manual, at the
cap" case by resetting the run-attempt counter on its own.

## Design choice: escalating an already-escalated task is rejected, not a no-op

Root plan issue #2851's design notes leave this open; FR9 itself does not
say what happens. `EscalateTask` rejects it, reusing `ErrTaskEscalated`
rather than minting a new sentinel or treating the call as a no-op —
`recordEscalationTx`'s own doc comment (`task_escalation.go`, issue #2868)
already named this exact condition ("recordEscalationTx's named rejection
when a task that already has one active escalation is escalated again
(FR9's one-event rule)") before this task ever called it, so reusing it
keeps one error meaning one condition rather than two errors for the
identical one.

A no-op was considered and rejected: it would let an operator believe their
`escalate` call is what put the task in front of the console, when in fact
an earlier — possibly differently-reasoned — event did, silently
discarding the caller's own rationale (`task_intervention_event.reason`)
with no way to look up "for either previous event." A loud rejection keeps
"the one escalation event this task has" a question with exactly one
accurate answer, and lets the caller re-fetch the task payload
(`GET /tasks/{id}`, `work.Payload.EscalationReason`) to see which reason is
already active. Both verbs also refuse an already-cancelled task
(`ErrTaskCancelled`) — cancel's dead-letter state is terminal for both
(`task_cancel.go`'s own FR7 doc comment).
