---
name: "krill-implement"
description: "Runs the swimlane execution phase of a krill-work plan (fork of project-manager's implement) — orchestrates worker and validator personas in parallel batches, via per-task branches created in dedicated worktrees, over ready krill Tasks from planner's manifest until every task reaches Done, then hands each batch's push/PR integration and continuous trunk-merge off to a mergepush subagent. Requires the task manifest the `plan` workflow produced (Milestone path) or the Project board it created (no-Milestone path)."
---


# implement

The orchestrator/branch/worktree/batch/mergepush machinery (worktree
creation, `<plan-branches>` tracking, batching, the mergepush hand-off, the
report format) matches `tools/project-manager/workflows/implement`; task
discovery and dispatch differ, below.

## Usage

```
the `implement` workflow <milestone-id>     # Milestone path — needs the task manifest plan produced
the `implement` workflow 123                # no-Milestone GitHub fallback: the tracking issue number
the `implement` workflow <milestone-id> --max-subagents 2
```

## Steps (Milestone path)

1. **Discovery**: use the task manifest the `plan` workflow/`planner`
   reported (every task id, title, starting lane, and dependency edges) if
   you have it. If you don't, call `list_tasks {milestone_id}`
   (CONVENTIONS.md "Work axis") for the task ids, then `get_task {id}` on
   each for its dependency edges — `list_tasks` itself doesn't carry
   dependencies, only id/title/`current_lane`/attempt count/live-claim.
2. For each task whose dependencies (from the manifest) are all `Done` —
   call `get_task {id}` on each to confirm current state rather than
   trusting the manifest's snapshot — create its branch/worktree exactly as
   `tools/project-manager/workflows/implement.md` describes, then
   spawn a subagent (Cline: new_task) with the `krill-worker` agent as its mode with `<krill-session-id>`, `<task-id>`, and
   `<worktree-path>` only (not an issue number, and not the task's
   title/body — worker reads those via `get_task`; CONVENTIONS.md "Subagent
   dispatch: ids, not bodies").
3. Once a worker/validator returns, re-read the task via `get_task {id}` to
   see the lane krill itself moved it to (`complete_task`'s pass/fail
   delta) — this is what decides the next dispatch, not a `Status` field
   you set yourself.
4. Batch and hand off to `mergepush` exactly as project-manager's
   `implement` does — `mergepush` gets each ready task's `task_id` and
   branch name in place of `{task-issue-number, title}`, and reads title/body
   itself via `get_task` (see `the krill-mergepush agent in .cline/agents`).

Every `worker`/`validator` dispatch you make has a working
`claim_task`/`complete_task` surface — see `the krill-worker agent in .cline/agents`. A
`forbidden` from either is a real regression; don't route around it,
surface it in your own report as-is.

## Steps (no-Milestone GitHub fallback)

Identical to `tools/project-manager/workflows/implement.md` — `<n>` is
the GitHub tracking issue the `krill-planner` agent minted, dispatch
the `krill-worker` agent/`validator`/`mergepush` in place of the
`project-manager:*` personas. Read that file for the full process; it is
not duplicated here since none of it changed on this path.
