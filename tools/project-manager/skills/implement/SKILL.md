---
name: implement
description: Runs the implementation phase of a project-manager plan — dispatches planner to create the task breakdown Project with swimlanes (if not already done), then orchestrates worker and validator personas in parallel batches, via gh-stack-managed per-task branches, over ready tasks across swimlanes until all tasks reach Done. Can also stop right after planning when the user only wants the Project board created.
---

# implement

Drives implementation from `plan:approved` through swimlane task execution. The outer loop is an orchestrator, not a worker itself: it uses [`gh stack`](../../../../.claude/skills/gh-stack/SKILL.md) to give each task issue its own branch and PR, and dispatches `worker`/`validator` subagents concurrently against those branches. See `tools/project-manager/CONVENTIONS.md` § Git hygiene for the exact stacking mechanics referenced below, and § Worker lifecycle for swimlane query/advance mechanics.

## Usage

```
/project-manager:implement 123
/project-manager:implement 123 --max-subagents 2
/project-manager:implement 123 --create-plan-only
```

- `--max-subagents <N>` — how many worker/validator subagents to run concurrently per batch. **Defaults to 4** if the user doesn't specify a number.
- `--create-plan-only` — stop once the Project board and task issues exist; do not run the swimlane work loop. Recognize close phrasings the user might use instead of the literal flag ("plan only", "just set up the board", "don't implement yet", "create the tasks but don't start work") as the same request.

## Steps

1. `gh issue view <n>` — confirm the root issue is labeled `plan:approved`. If not, point the user to `/project-manager:plan` or `/project-manager:review`.

2. **Stack prerequisites** (CONVENTIONS.md § Git hygiene step 1):
   ```sh
   gh extension list | grep -q github/gh-stack || gh extension install github/gh-stack
   git config rerere.enabled true
   git config remote.pushDefault origin
   git fetch origin main
   ```

3. **Task breakdown (skip if already done).** `gh issue view <n> --comments` — if a `Project board: <url>` comment exists, extract the project number. Otherwise dispatch `project-manager:planner` with the root issue number to create the Project board with swimlanes, create cohesive task issues, and post the summary comment.

   **If a plan-only request was made:** report the Project board URL and the created task issues, grouped by starting swimlane, to the user and **stop here** — do not proceed to step 4.

4. **Swimlane work loop.** Repeat until a full pass finds no ready unassigned work:
   a. Check active swimlanes in progression order — `Scaffold`, `Implementation`, `Testing`, `Validation` — for unassigned items scoped to this plan (query per CONVENTIONS.md § Worker lifecycle step 1), and confirm every issue in each candidate's `Depends on:` line is `CLOSED`.
   b. **Batch.** Take up to `max-subagents` ready candidates across swimlanes — phases can mix freely in one batch (e.g. a `Scaffold` candidate and a `Validation` candidate together).
   c. **Prepare each candidate's branch and worktree, one at a time.** `git`/`gh stack` state is shared and must not be touched by two commands at once, so this sub-step is sequential even though dispatch in 4d is parallel:
      - Branch name: `plan/<n>-<task>`.
      - If the branch doesn't exist yet (first phase dispatched for this task), create it per CONVENTIONS.md § Git hygiene step 3 (fresh stack off `main`, or chained onto its `Depends on:` branch(es)).
      - `git checkout --detach HEAD` (releases the branch from the shared checkout) then `git worktree add .pm-worktrees/<task> plan/<n>-<task>` (CONVENTIONS.md § Git hygiene step 4).
   d. **Dispatch the batch in parallel** — one `Agent` call per candidate, all in a single message so they actually run concurrently:
      - `Scaffold` / `Implementation` / `Testing` candidates → `project-manager:worker` with `<n>`, `<project-number>`, `<Swimlane>`, the candidate issue number, and its worktree path.
      - `Validation` candidates → `project-manager:validator` with `<n>`, `<project-number>`, the candidate issue number, and its worktree path.
   e. **Integrate each result as its subagent returns** (CONVENTIONS.md § Git hygiene step 6): remove the worktree, check out the task's branch, `gh stack submit --auto` to push the phase's commit and refresh its draft PR, then detach again.
   f. Re-scan swimlanes after the batch completes. Repeat from (a) until all tasks reach `Done` or a full pass finds no more ready unassigned tasks.

5. **Report.** Summarize progress across swimlanes, listing each task's PR URL (collected during step 4e, or via `gh stack view --json` per branch). If every task is `Status: Done`, tell the user `/project-manager:validate <n>` is the next step. If items remain blocked or waiting on dependency fixes, report them clearly.
