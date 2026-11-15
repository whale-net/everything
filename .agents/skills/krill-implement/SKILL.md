---
name: krill-implement
description: Runs the swimlane execution phase of a krill-work plan — orchestrates worker and validator personas in parallel batches, via per-task branches created in dedicated worktrees, over ready krill Tasks from planner's manifest until every task reaches Done, then hands each batch's push/PR integration and continuous trunk-merge off to a mergepush subagent. Requires a krill Milestone with the tasks /krill-plan created; with none, it stops and says to cut the milestone first.
---

# krill-implement

An orchestrator, not a worker: it gives each task its own branch and
worktree, dispatches `krill-worker`/`krill-validator` subagents concurrently
against them, then dispatches `krill-mergepush` once per batch to push,
open/refresh PRs, and merge into `main` whatever is `Done` and safe to land.
Git mechanics (branch naming, worktree creation, PR shape) are in
`references/CONVENTIONS.md` § "Git hygiene".

**Responsibilities.** The orchestrator creates every branch and its worktree
itself and resolves any merge conflict that arises (inline for a small one, or
via an ad-hoc subagent scoped to that one merge when it's large or ambiguous —
never hand a worker an unresolved conflict). Workers/validators only edit and
commit inside the worktree they're handed; they never push, merge, or open PRs.
Neither does the orchestrator — `krill-mergepush` runs all of that, dispatched
once per batch and waited on before the next scan.

## Usage

```
/krill-implement <milestone-id>
/krill-implement <milestone-id> --max-subagents 2
```

`<milestone-id>` is required. With no krill Milestone (or no tasks under it),
stop and report the "Milestone required" hard stop in `CONVENTIONS.md`: cut the
milestone first (`/krill-product`/`/krill-design`), then run `/krill-plan`.

`--max-subagents <N>` — workers/validators run concurrently per batch;
defaults to 4.

## Subagent setup

Call `get_skill_resources` for `["references/CONVENTIONS.md", "references/agents/worker.md", "references/agents/validator.md", "references/agents/mergepush.md"]`.
Define once per conversation via `define_subagent`:
- `krill-worker`: `system_prompt` from `references/agents/worker.md`, `enable_mcp_tools: true`, `enable_write_tools: true`.
- `krill-validator`: `system_prompt` from `references/agents/validator.md`, `enable_mcp_tools: true`, `enable_write_tools: false` (Wait: `validator` runs `bazel build`/`bazel test` via `run_command`, so set `enable_write_tools: true` while its prompt enforces read-only!).
- `krill-mergepush`: `system_prompt` from `references/agents/mergepush.md`, `enable_mcp_tools: true`, `enable_write_tools: true`.

## Steps

0. **Prerequisites:** `init_session {}` once and keep `session_id` as
   `<krill-session-id>` for every dispatch below (mint a fresh one if a call
   rejects it as unknown or expired); `git config rerere.enabled true`,
   `git config remote.pushDefault origin`, `git fetch origin main`. Track
   `<plan-branches>`: every branch on this plan, in dependency order. Seed
   it from every task past `Implementation` (`Testing`, `Validation`, `Done`)
   via `list_tasks`/`get_task`; append each batch's new branches after every
   `krill-mergepush` call.
1. **Discovery**: use the task manifest `/krill-plan` reported if you have it.
   If you don't, call `list_tasks {milestone_id}` for the task ids — and on each
   milepebble from `list_milepebbles`, whose tasks never list under the parent —
   then `get_task {id}` on each for its dependency edges.
2. For each task whose dependencies are all `Done` — call `get_task {id}` on
   each to confirm current state — take up to `max-subagents` ready tasks,
   prepare each one's branch/worktree, then dispatch in parallel (all entries
   in a single `invoke_subagent` call with `Model: "inherit"`,
   `Workspace: "inherit"`):
   - **Branch/worktree:** branch `pm[<attempt>]-<milestone>/<task>-<slug>`;
     look for an existing `pm*-<milestone>/<task>-*` branch first (local, then
     remote) and reuse it. Otherwise `git worktree add .claude/worktrees/<task>
     -b <branch> <fork-point>`, where `<fork-point>` is `origin/main` (never
     local `main`) or its dependency's branch; with several dependency
     branches, fork from one and `git -C .claude/worktrees/<task> merge
     --no-edit` the rest. Record `<parent>` — the PR base (`main`, or the
     dependency branch) — alongside branch and worktree.
   - **Dispatch:** `krill-worker` (`Scaffold`/`Implementation`/`Testing`) or
     `krill-validator` (`Validation`) with `<krill-session-id>`, `<task-id>`,
     and `<worktree-path>` only — not the task's title/body; they read those
     via `get_task`.
3. Once the whole batch returns, re-read each task via `get_task {id}` to see
   the lane krill itself moved it to (`complete_task`'s pass/fail delta).
4. Dispatch `krill-mergepush` once for the batch with `<milestone-id>` as root,
   the `<task-id>`, `<branch-name>`, `<worktree-path>`, `<parent>` tuple for
   every batch member in a fixed order, and `<done-task-ids>` — every task in
   `<plan-branches>` plus this batch whose `current_lane` is now `Done`. It
   pushes, opens/refreshes PRs, and merges ready tasks in dependency order. A
   failed task is resolved like a merge conflict before it's eligible again.
5. Re-scan and repeat from step 1 until every task is `Done` or a full pass
   finds no ready work.
6. **Report** each task's PR URL and whether it has merged (use the URLs already
   collected; otherwise one `gh pr list` call). If every task is `Done`, point
   to `/krill-validate <milestone-id>`; report anything blocked on a dependency
   clearly.

Every `krill-worker`/`krill-validator` dispatch has a working
`claim_task`/`complete_task` surface. A `forbidden` from either is a real
regression; don't route around it, surface it in your report as-is.
