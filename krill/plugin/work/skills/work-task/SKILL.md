---
name: work-task
description: Work one krill Task directly in this session — claim it, do the work, complete it. Use whenever the user asks to do, pick up, continue, or fix something that is (or should be) a krill Task — a task id, a milestone/milepebble's open task, a krill-hosted domain's backlog item — or when about to change code in a krill-hosted domain (krill, whagent_net, friendly_computing_machine) with no task claimed. The krill Task lane only moves through claim_task/complete_task, so work done without them leaves the task unclaimed and its lane stale. For a whole milestone run unattended, use /krill-work:implement instead.
---

# work-task

Single-task path for an ordinary session with no orchestrator: the session
itself does what a `worker`/`validator` subagent would, so the krill `Task`
records who held it and where it ended up. Mechanics are in
`krill/plugin/shared/CONVENTIONS.md` § "Working a task outside the swimlane
loop".

## Usage

```
/krill-work:work-task <task-id>
/krill-work:work-task <milestone-or-milepebble-id>   # pick the next ready task
/krill-work:work-task "<what you're about to do>"     # no task exists yet
```

## Steps

1. **Session.** `init_session {}` → keep `session_id`. Mint a fresh one if a
   call rejects it as unknown or expired.
2. **Find the task.**
   - Task id given: `get_task {id}`.
   - Milestone/milepebble id given: `list_tasks {milestone_id}`, then
     `get_task` on each candidate. Take the oldest whose dependencies are all
     `Done`, with no live claim and `current_lane` ≠ `Done`.
   - Free-text only: `get_scope {}` → `list_products {scope_id}`; if the
     touched domain is krill-hosted, look for a matching open task via
     `list_tasks` over its in-flight milestone/milepebble. If none exists,
     say so and let the user choose between `/krill-work:plan` and a
     `create_task` under the right milestone — don't start the work
     untracked. A domain that isn't krill-hosted has nothing to claim;
     proceed normally.
3. **Claim before editing anything.** `claim_task {krill_session_id,
   task_id}`; save `current_claim.claim_id`. If the claim is refused
   (unresolved dependency, live claim held elsewhere, attempt cap), report
   the exact error and stop — don't do the work anyway.
4. **Do the lane's work** per `task.current_lane` and `task.body`, following
   `agents/worker.md` (Scaffold / Implementation / Testing) or
   `agents/validator.md` (Validation, read-only). `heartbeat_task
   {krill_session_id, task_id, claim_id}` is best-effort between long steps;
   a lapsed lease only matters if another session claims the task.
5. **Close the claim.** `complete_task {krill_session_id, task_id,
   claim_id, verdict: "pass" | "fail", summary}` when the lane's work is
   finished — krill moves the lane itself. If blocked with no verdict to
   give, `abandon_task {…, reason}` instead. Never leave a claim to expire.
6. **More lanes?** `get_task` again. If it advanced and the next lane is
   yours to do, loop from step 3 (a new claim per lane).
7. **Report** the task id, final lane, and anything out of scope you filed
   with `record_note {kind: "scope-note"}`.

Put `krill task: <task-id>` on its own line at the end of each commit message
for this task. Pushing and PRs follow the repo's normal flow.
