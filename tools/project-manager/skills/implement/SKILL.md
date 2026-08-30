---
name: implement
description: Runs the swimlane execution phase of a project-manager plan — orchestrates worker and validator personas in parallel batches, via gh-stack-managed per-task branches, over ready tasks across swimlanes until all tasks reach Done, then hands each batch's push/PR integration off to a mergepush subagent so this session's own context stays free of git/gh-stack command output and the plan ends up as one flat branch history. Requires the Project board (from /project-manager:plan) to already exist.
---

# implement

Drives swimlane task execution against an existing Project board. The outer loop is an orchestrator, not a worker itself: it uses [`gh stack`](../../../../.claude/skills/gh-stack/SKILL.md) to give each task issue its own branch and PR, dispatches `worker`/`validator` subagents concurrently against those branches, then dispatches `mergepush` to flatten each batch's finished work onto the plan's running chain tip and open/refresh its PRs. See `tools/project-manager/CONVENTIONS.md` § Git hygiene for the exact stacking mechanics referenced below, and § Worker lifecycle for swimlane query/advance mechanics.

**Responsibilities.** This orchestrator owns all shared git/`gh stack` state, but doesn't run every step of it inline: it creates every branch and worktree itself, resolves any merge conflict that comes up while doing so (directly for a small conflict, or via an ad-hoc `general-purpose` merge subagent scoped to just that conflict when it's large or semantically ambiguous — CONVENTIONS.md § Git hygiene, "Division of responsibility"), then delegates flattening each task onto the plan's chain tip and creating/refreshing its PR (`gh stack submit --auto` plus a base correction — CONVENTIONS.md § Git hygiene step 6) to a `mergepush` subagent, dispatched once per batch and waited on synchronously before the next swimlane scan — so `gh stack` operations stay exactly as single-threaded as before, just running in a subagent's context instead of this session's own. Dispatched `worker`/`validator` subagents run entirely inside the worktree path they're handed: they edit files and commit on the task's branch, but never touch `git merge`, `gh stack`, or PR creation themselves — and neither, directly, does this orchestrator anymore.

**Chain tip.** Branch creation (step 3c below) still chains each task onto its own `Depends on:` dependency, which can fork into a tree when two tasks share a dependency — but the plan's pushed PR history must stay one flat chain, `main → task-a → task-b → ...`, so it stays reviewable and mergeable in order (CONVENTIONS.md § Git hygiene step 6, "Why flatten" in `mergepush`'s own instructions). This orchestrator therefore tracks a `<chain-tip>` session variable for the life of the run: seed it to `main` at the start of a fresh run (or, when resuming a plan that already has integrated tasks, to whatever branch is currently the base of no other open `pm*-<n>/*` PR — the one open PR whose branch never appears as another open PR's base); pass it into every `mergepush` dispatch (step 3e); and after each dispatch returns, set it to the new tip that dispatch reports back.

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
   e. **Wait for the whole batch to return, then dispatch `mergepush` once to integrate it** (CONVENTIONS.md § Git hygiene step 6): pass it `<n>` (the root issue), the current `<chain-tip>`, and, for every batch member — regardless of whether its phase advanced the swimlane or routed back — the same `<task-issue-number>`, `<branch-name>`, `<worktree-path>` triple resolved for it in step 3c, in a fixed order. `mergepush` removes each worktree, checks out its branch, rebases it onto `<chain-tip>` (advancing its own running copy of the tip as it works through the list, so later tasks in the batch chain onto earlier ones), pushes, runs `gh stack submit --auto` to open/refresh its draft PR, corrects the PR's base so the chain stays flat, sets title/body on first creation (CONVENTIONS.md § Git hygiene, "PR content"), and reports back one line per task — its PR URL and whether this created or updated it, or the exact error if a push failed — plus a final line giving the new `<chain-tip>`. This is the only point anything creates a PR — worker/validator never do, and this orchestrator no longer runs the push commands itself either, so their `git`/`gh stack`/`gh pr` output never lands in this session's own context. Update your `<chain-tip>` variable from that final line. Collect the PR URLs (and any failures) from `mergepush`'s report for step 4's summary; for a failed task, resolve it the same way an unresolved merge conflict from step 3c would be handled (inline, or an ad-hoc `general-purpose` subagent) before it's eligible for the next batch.
   f. Re-scan swimlanes after `mergepush` returns. Repeat from (a) until all tasks reach `Done` or a full pass finds no more ready unassigned tasks.

4. **Report.** Summarize progress across swimlanes, listing each task's PR URL (collected during step 3e, or via `gh stack view --json` per branch). If every task is `Status: Done`, tell the user `/project-manager:validate <n>` is the next step. If items remain blocked or waiting on dependency fixes, report them clearly.
