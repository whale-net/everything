---
name: work-task
description: Work one existing krill Task directly in this session — claim it, do the work, complete it. Use only when the user names a krill Task (a task id) or a milestone/milepebble whose open task to pick up. The krill Task lane only moves through claim_task/complete_task, so a task's work done without them leaves it unclaimed and its lane stale. Not for ad-hoc edits that have no krill Task (docs, prompts, one-off fixes) — just do those normally. For a whole milestone run unattended, use /krill-work:implement instead.
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
```

## Steps

1. **Session.** `init_session {}` → keep `session_id`. Mint a fresh one if a
   call rejects it as unknown or expired.
2. **Find the task.**
   - Task id given: `get_task {id}`.
   - Milestone/milepebble id given: `list_tasks {milestone_id}`, then
     `get_task` on each candidate. Take the oldest whose dependencies are all
     `Done`, with no live claim and `current_lane` ≠ `Done`.
   - No id given: ask for one. This skill doesn't create tasks or track
     ad-hoc work; a request with no krill Task isn't its job.
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
