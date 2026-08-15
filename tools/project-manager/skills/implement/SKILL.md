---
name: implement
description: Runs the implementation phase of a project-manager plan — dispatches planner to create the task breakdown Project (if not already done), then loops worker/validator over available ready items until the plan's work is done or nothing more can proceed.
---

# implement

Drives everything from `plan:approved` through worker execution. See `tools/project-manager/CONVENTIONS.md` § Project setup, § Task issues, § Worker lifecycle, and § Git hygiene for the contract this dispatches against.

## Usage

```
/project-manager:implement 123
```

## Steps

1. `gh issue view <n>` — confirm the root issue is `plan:approved`. If it's only `plan:architect-approved`, tell the user to run `/project-manager:review <n>` first and stop. If it's an earlier state, point them at `/project-manager:plan <n>`.

2. **Branch.** Per CONVENTIONS.md § Git hygiene, ensure the plan's branch exists and is checked out before dispatching any worker: `git fetch origin main`, then `git checkout plan/<n>-<short-slug>` or, if it doesn't exist yet, `git checkout -b plan/<n>-<short-slug> origin/main`. Derive `<short-slug>` deterministically from the root issue title so a later re-invocation reuses the same branch rather than creating a second one. Workers/validators you dispatch inherit this checkout and commit to it directly.

3. **Task breakdown (skip if already done).** `gh issue view <n> --comments` — if a `Project board: <url>` comment exists, the plan's Project already exists; extract its number and skip to step 3. Otherwise dispatch `project-manager:planner` (foreground, via Agent tool) with the root issue number to run its Process: create the Project, break the plan into scaffold/implementation/testing/validation task issues with dependencies, add them to the Project with the right `Status`, and post the summary + `Project board:` comment.

4. **Work loop.** Repeat until a full pass finds nothing to do:
   a. For each phase in order — `Scaffold`, `Implementation`, `Testing`, `Validation` — list unclaimed items at that phase scoped to this plan, then filter to ones whose dependencies are actually closed (an unclaimed item isn't necessarily ready — its `Depends on:` issues may still be open):
      ```sh
      gh project item-list <project-number> --owner whale-net --query "status:<Phase> no:assignee" --format json \
        | jq -r '.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)")) | .content.number'
      ```
      For each candidate, check its `Depends on:` line (`gh issue view <candidate> --json body`) and confirm every listed issue is closed (`gh issue view <dep> --json state`) before treating it as ready.
   b. For every ready issue found, dispatch the matching worker — `project-manager:worker` for `Scaffold`/`Implementation`/`Testing`, `project-manager:validator` for `Validation` — via the Agent tool. Independent issues within the same phase (no dependency relationship between them) can run in parallel background agents; issues you know depend on each other should run sequentially. You don't have to hand-pick which issue each worker takes — the persona instructions already have it query and claim ready work itself — but scope the dispatch to this plan by telling the worker the plan's root issue number, project number, and, if there are several ready issues, which one to target. Parallel workers share the one plan branch and working tree — each worker's own `git add -A && git commit` per CONVENTIONS.md § Git hygiene only stages its own changes at the moment it commits, but if you dispatch truly parallel workers, prefer ones that touch disjoint files (as scaffold/implementation task issues normally do) to avoid one worker's commit racing another's uncommitted edits.
   c. After a batch of workers finishes, re-run the phase-by-phase listing. If new issues became ready (a dependency just closed) or a tester left a failure comment needing attention, continue the loop. Stop the loop when a full pass across all four phases finds no ready work left for this plan.

5. **Report.** Summarize what got closed (moved to `Done`), what's still waiting (and why — usually waiting on a tester-flagged implementation bug, which needs a worker re-run, or a genuinely open dependency), and whether every phase is fully closed. If everything under `Implementation`/`Testing` is `Done`, tell the user `/project-manager:validate <n>` is the next step. If something is stuck, say what and don't loop forever guessing at a fix.
