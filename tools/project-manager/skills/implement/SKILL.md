---
name: implement
description: Runs the swimlane execution phase of a project-manager plan — orchestrates worker and validator personas in parallel batches, via gh-stack-managed per-task branches, over ready tasks across swimlanes until all tasks reach Done. Requires the Project board (from /project-manager:plan) to already exist.
---

# implement

Drives swimlane task execution against an existing Project board. The outer loop is an orchestrator, not a worker itself: it uses [`gh stack`](../../../../.claude/skills/gh-stack/SKILL.md) to give each task issue its own branch and PR, and dispatches `worker`/`validator` subagents concurrently against those branches. See `tools/project-manager/CONVENTIONS.md` § Git hygiene for the exact stacking mechanics referenced below, and § Worker lifecycle for swimlane query/advance mechanics.

**Responsibilities.** This orchestrator — not the subagents it dispatches — owns all shared git/`gh stack` state: it creates every branch and worktree, resolves any merge conflict that comes up while doing so (directly for a small conflict, or via an ad-hoc `general-purpose` merge subagent scoped to just that conflict when it's large or semantically ambiguous — CONVENTIONS.md § Git hygiene, "Division of responsibility"), and is the only persona that creates or refreshes a task's PR (`gh stack submit --auto`). Dispatched `worker`/`validator` subagents run entirely inside the worktree path they're handed: they edit files and commit on the task's branch, but never touch `git merge`, `gh stack`, or PR creation themselves.

## Usage

```
/project-manager:implement 123
/project-manager:implement 123 --max-subagents 2
```

- `--max-subagents <N>` — how many worker/validator subagents to run concurrently per batch. **Defaults to 4** if the user doesn't specify a number.

## Steps

1. `gh issue view <n> --comments` — confirm the root issue is labeled `plan:approved` and a `Project board: <url>` comment exists (extract the project number). If the label is missing, point the user to `/project-manager:design` or `/project-manager:review`. If the label is present but there's no `Project board:` comment yet, point the user to `/project-manager:plan <n>` first and stop.

2. **Stack prerequisites** (CONVENTIONS.md § Git hygiene step 1):
   ```sh
   gh extension list | grep -q github/gh-stack || gh extension install github/gh-stack
   git config rerere.enabled true
   git config remote.pushDefault origin
   git fetch origin main
   ```

3. **Swimlane work loop.** Repeat until a full pass finds no ready unassigned work:
   a. Check active swimlanes in progression order — `Scaffold`, `Implementation`, `Testing`, `Validation` — for unassigned items scoped to this plan (query per CONVENTIONS.md § Worker lifecycle step 1), and confirm every issue in each candidate's `Depends on:` line is `CLOSED`.
   b. **Batch.** Take up to `max-subagents` ready candidates across swimlanes — phases can mix freely in one batch (e.g. a `Scaffold` candidate and a `Validation` candidate together).
   c. **Prepare each candidate's branch and worktree, one at a time.** `git`/`gh stack` state is shared and must not be touched by two commands at once, so this sub-step is sequential even though dispatch in 3d is parallel:
      - Branch name: `pm[<attempt>]-<n>/<task>-<slug>` (CONVENTIONS.md § Git hygiene step 2). Look for an existing `pm*-<n>/<task>-*` branch first (local, then remote) and reuse it if found; only compute a fresh `<slug>` from the task issue's title, with `<attempt>` omitted, when none exists.
      - If the branch doesn't exist yet (first phase dispatched for this task), create it per CONVENTIONS.md § Git hygiene step 3 (fresh stack off `main`, or chained onto its `Depends on:` branch(es)). A task with more than one dependency branch merges the rest in here (`git merge --no-edit`) — if that merge conflicts, resolve it yourself before moving on, or, for a large/ambiguous conflict, dispatch an ad-hoc `general-purpose` subagent scoped to just that merge (CONVENTIONS.md § Git hygiene, "Division of responsibility"). Never hand an unresolved conflict to the worker.
      - `git checkout --detach HEAD` (releases the branch from the shared checkout) then `git worktree add .pm-worktrees/<task> pm[<attempt>]-<n>/<task>-<slug>` (CONVENTIONS.md § Git hygiene step 4).
   d. **Dispatch the batch in parallel** — one `Agent` call per candidate, all in a single message so they actually run concurrently:
      - `Scaffold` / `Implementation` / `Testing` candidates → `project-manager:worker` with `<n>`, `<project-number>`, `<Swimlane>`, the candidate issue number, and its worktree path.
      - `Validation` candidates → `project-manager:validator` with `<n>`, `<project-number>`, the candidate issue number, and its worktree path.
   e. **Integrate each result as its subagent returns** (CONVENTIONS.md § Git hygiene step 6): remove the worktree, check out the task's branch, `gh stack submit --auto` to push the phase's commit and open or refresh its draft PR, then detach again. This is the only point anything creates a PR — subagents never do — and this orchestrator is the one that owns it. The first time this creates a task's PR, follow up with `gh pr edit` to set its title and body per CONVENTIONS.md § Git hygiene, "PR content" — a title drawn from the task issue, and a body linking `Task: #<task>` plus a couple sentences of context, not the auto-generated commit-derived text.
   f. Re-scan swimlanes after the batch completes. Repeat from (a) until all tasks reach `Done` or a full pass finds no more ready unassigned tasks.

4. **Report.** Summarize progress across swimlanes, listing each task's PR URL (collected during step 3e, or via `gh stack view --json` per branch). If every task is `Status: Done`, tell the user `/project-manager:validate <n>` is the next step. If items remain blocked or waiting on dependency fixes, report them clearly.
