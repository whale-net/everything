---
name: implement
description: Runs the implementation phase of a project-manager plan — dispatches planner to create the task breakdown (if not already done), then loops writer/tester/validator workers over available status:ready issues until the plan's work is done or nothing more can proceed.
---

# implement

Drives everything from `plan:approved` through worker execution. See `tools/project-manager/CONVENTIONS.md` § Task breakdown and § Worker lifecycle for the contract this dispatches against.

## Usage

```
/project-manager:implement 123
```

## Steps

1. `gh issue view <n>` — confirm the root issue is `plan:approved`. If it's only `plan:architect-approved`, tell the user to run `/project-manager:review <n>` first and stop. If it's an earlier state, point them at `/project-manager:plan <n>`.

2. **Task breakdown (skip if already done).** Check whether task issues already exist for this plan: `gh issue list --search "Part of #<n>" --state all --json number,body | jq -r '.[] | select(.body | test("Part of #" + "<n>" + "([^0-9]|$)")) | .number'` — the `--search` prefilter is just a coarse net here (fine for a one-time existence check, unlike the precise per-dependency logic workers use); if it returns anything, task issues already exist, skip to step 3. Otherwise dispatch `project-manager:planner` (foreground, via Agent tool) with the root issue number to run its Process: read the plan, break it into scaffold/implementation/testing/validation task issues with dependencies and initial `status:ready`/`status:blocked` labels, and post the summary comment.

3. **Work loop.** Repeat until a full pass finds nothing to do:
   a. For each phase in order — `scaffold`, `implementation`, `testing`, `validation` — list ready work scoped to this plan:
      ```sh
      gh issue list --label "status:ready" --label "phase:<phase>" --state open --json number,body \
        | jq -r '.[] | select(.body | test("Part of #<n>([^0-9]|$)")) | .number'
      ```
   b. For every issue found, dispatch the matching worker — `project-manager:writer` for `scaffold`/`implementation`, `project-manager:tester` for `testing`, `project-manager:validator` for `validation` — via the Agent tool. Independent issues within the same phase (no dependency relationship between them) can run in parallel background agents; issues you know depend on each other should run sequentially. You don't have to hand-pick which issue each worker takes — the persona instructions already have it query and claim `status:ready` work itself — but scope the dispatch to this plan by telling the worker the plan's root issue number and, if there are several ready issues, which one to target.
   c. After a batch of workers finishes, re-run the phase-by-phase listing. If new issues became `status:ready` (unblocked dependents) or a tester left a failure comment needing attention, continue the loop. Stop the loop when a full pass across all four phases finds no `status:ready` work left for this plan.

4. **Report.** Summarize what got closed, what's still `status:blocked` (and why — usually waiting on a tester-flagged implementation bug, which needs a writer re-run, or a genuinely external blocker), and whether every phase is fully closed. If everything under `phase:implementation`/`phase:testing` is closed, tell the user `/project-manager:validate <n>` is the next step. If something is stuck, say what and don't loop forever guessing at a fix.

