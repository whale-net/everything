---
name: krill-loop-plan-implement-validate
description: Drives a signed-off krill design all the way to a set of merged PRs unattended — dispatches /krill-plan, /krill-implement, and /krill-validate each to their own fresh subagent, looping implement→validate again whenever validate routes findings back to Implementation, until validate reports a clean merge. Finishes with an independent final-verification subagent. Use for "run this plan end to end", "loop plan/implement/validate until done", or "get this whole plan merged without me babysitting each phase".
---

# krill-loop-plan-implement-validate

Chains `/krill-plan` → `/krill-implement` → `/krill-validate` to completion,
re-looping `implement`→`validate` whenever `validate` produces follow-up
findings, until every task PR is merged into `main`. Each phase runs in its own
fresh subagent that invokes that phase's skill via `lookup_skill`, so this
session never runs a phase's `gh`/`git` steps itself and only relays each
subagent's short summary.

Starts from a krill FeatureSet/design-session id (once signed off) and a krill
Milestone id. **`<milestone-id>` is required** — with none, stop and report the
"Milestone required" hard stop in `references/CONVENTIONS.md` (cut the
milestone first via `/krill-product`/`/krill-design`). Hand each phase's
subagent the Milestone id and the manifest's task ids and dependency edges only
— never task bodies.

## Usage

```
/krill-loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id>
/krill-loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id> --max-subagents 2
/krill-loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id> --max-iterations 8
```

- `--max-subagents <N>` — forwarded verbatim to `/krill-implement` on every iteration. Defaults to 4.
- `--max-iterations <N>` — safety cap on `implement`↔`validate` cycles before stopping and reporting the plan stuck. Defaults to 3.

## Subagent setup

Define two reusable subagent types once per conversation via `define_subagent`:
- `krill-phase-runner`: `enable_subagent_tools: true, enable_mcp_tools: true, enable_write_tools: true`, instructed to call `lookup_skill` for the requested phase skill (`krill-plan`, `krill-implement`, or `krill-validate`) and execute it end-to-end, returning only the requested summary.
- `krill-final-verifier`: `enable_subagent_tools: false, enable_mcp_tools: false, enable_write_tools: true` (for read-only `gh`/`git` checks via `run_command`), instructed to verify PRs and `origin/main` independently.

## Steps

1. **Confirm root state.** Check the design session (or FeatureSet slice)
   ended in `signoff`/`approved`; if not, point the user to `/krill-design`,
   `/krill-review`, or `/krill-loop-design-panel` and stop. Note whether tasks
   already exist (`list_tasks {milestone_id}`, plus each milepebble's per
   `CONVENTIONS.md` "Milestone given: check its milepebbles first").

2. **Plan phase (subagent), only if no tasks exist yet.** Dispatch a fresh
   `krill-phase-runner` via `invoke_subagent` (`Model: "inherit"`,
   `Workspace: "inherit"`): invoke `lookup_skill` on `krill-plan` with the
   FeatureSet id and `--milestone-id`, let it finish, then report back *only*
   the task manifest (ids, titles, starting lanes, dependency edges). If tasks
   already exist, skip and carry the manifest forward.

3. **Implement phase (subagent).** Dispatch a fresh `krill-phase-runner`:
   invoke `lookup_skill` on `krill-implement` with `<milestone-id>` (plus
   `--max-subagents <N>` if given), let it finish, then report back *only*
   whether every task reached `Done` and, per task, its id, branch, and PR
   number/URL (`<plan-branches>`). Keep this `{task → branch → PR}` list for
   step 6. If tasks remain blocked, surface it to the user and stop.

4. **Validate phase (subagent).** Dispatch a fresh `krill-phase-runner`:
   invoke `lookup_skill` on `krill-validate` with `<milestone-id>`, let it
   finish, then report back *only*:
   - **Clean pass:** the finalized `<plan-branches>` (branch → PR number/URL, all merged).
   - **Findings:** the new follow-up task ids `validate` had `planner` create.

5. **Loop control.** Clean pass → step 6. Findings → increment the iteration
   counter; if it has reached `--max-iterations`, stop and report the plan
   stuck, listing the outstanding findings and pointing to `/krill-implement
   <milestone-id>` to continue manually. Otherwise return to step 3.

6. **Final verification (subagent).** Dispatch `krill-final-verifier` (never
   inline). Hand it *only* the `{task → branch → PR number/URL}` list from
   steps 3-4. Ask it to confirm independently:
   - every listed PR is `MERGED` (`gh pr view <branch-or-number> --json state,mergedAt`),
   - no PR belonging to this plan is still open,
   - `main` contains every task's work (`git log --oneline origin/main` grep per task).
   It reports back a short pass/fail plus any discrepancy — nothing else.

7. **Report.** Give the user the final merged PR list (numbers + URLs) and
   step 6's verdict. If step 6 found a discrepancy, surface it plainly rather
   than declaring the plan done.
