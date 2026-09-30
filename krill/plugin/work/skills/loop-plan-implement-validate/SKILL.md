---
name: loop-plan-implement-validate
description: Drives a signed-off krill design all the way to a set of merged PRs unattended — dispatches /krill-work:plan, /krill-work:implement, and /krill-work:validate each to their own fresh subagent, looping implement→validate again whenever validate routes findings back to Implementation, until validate reports a clean merge. Finishes with an independent final-verification subagent. Use for "run this plan end to end", "loop plan/implement/validate until done", or "get this whole plan merged without me babysitting each phase".
---

# loop-plan-implement-validate

Chains `/krill-work:plan` → `/krill-work:implement` → `/krill-work:validate`
to completion, re-looping `implement`→`validate` whenever `validate`
produces follow-up findings, until every task PR is merged into `main`.
Each phase runs in its own fresh subagent that re-enters that phase's skill
via the `Skill` tool, so this session never runs a phase's `gh`/`git` steps
itself and only relays each subagent's short summary. Those phase
subagents dispatch their own personas (`plan` → `planner`; `implement` →
`worker`/`validator`/`mergepush`; `validate` → `system-validator`/
`planner`); this orchestrator never talks to a persona directly. It is a
convenience wrapper: it touches no GitHub state itself and adds no git
hygiene of its own.

Starts from a krill FeatureSet/design-session id (once signed off).
**On the Milestone path, `<n>` throughout is the Milestone id plus the task
manifest `plan` produced — not a GitHub tracking issue.** Hand each phase's
subagent the Milestone id and the manifest's task ids and dependency edges
only — never task bodies; the subagent reads each task with `get_task {id}`
(CONVENTIONS.md "Subagent dispatch: ids, not bodies"). A subagent missing
the ids can recover the task set with `list_tasks {milestone_id}`. On the
no-Milestone fallback, `<n>` is the GitHub tracking issue
`krill-work:planner` mints.

On the Milestone path, every `worker`/`validator`/`system-validator`
dispatch inside `implement`/`validate` has a working task-lifecycle tool
surface. A `forbidden` from one of those tools is a real regression:
report it, don't fall back to GitHub.

## Usage

```
/krill-work:loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id>   # Milestone path
/krill-work:loop-plan-implement-validate <feature-set-id>                                  # no-Milestone GitHub fallback
/krill-work:loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id> --max-subagents 2 --planner-model opus
/krill-work:loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id> --max-iterations 8
```

- `--max-subagents <N>` — forwarded verbatim to `/krill-work:implement` on
  every iteration. Defaults to 4.
- `--planner-model <model>` — forwarded to `/krill-work:plan`, first
  iteration only. Defaults to `opus`.
- `--max-iterations <N>` — safety cap on `implement`↔`validate` cycles
  before stopping and reporting the plan stuck. Defaults to 3; a
  well-scoped plan converges in 1-2, so hitting the cap points at
  recurring findings rather than a slow plan.

## Steps

1. **Confirm root state.** Check the design session (or FeatureSet slice)
   ended in `signoff`/`approved` — the same check `krill-work:plan` step 1
   does; if not, point the user to `/krill-design:design`,
   `/krill-design:review`, or `/krill-design:loop-design-panel` and stop.
   Note whether tasks already exist (`list_tasks {milestone_id}`, or on the
   fallback path a `Project board: <url>` comment on the tracking issue).

2. **Plan phase (subagent), only if no tasks exist yet.** Dispatch a fresh
   `general-purpose` subagent with a self-contained prompt: invoke `Skill`
   with `skill: "krill-work:plan"`, args the FeatureSet id (plus
   `--milestone-id`, and `--planner-model` if given), let it finish, then
   report back *only* the task manifest (ids, titles, starting lanes,
   dependency edges) or, on the fallback, the Project board URL/number and
   task issues by swimlane. No raw `gh`/`git` output should reach this
   session. If tasks already exist, skip the dispatch and carry the
   manifest forward.

3. **Implement phase (subagent).** Dispatch a fresh `general-purpose`
   subagent: invoke `Skill` with `skill: "krill-work:implement"`, args
   `<n>` (plus `--max-subagents <N>` if given), let it finish, then report
   back *only* whether every task reached `Done` and, per task, its id,
   branch, and PR number/URL (`implement`'s `<plan-branches>`). Keep this
   `{task → branch → PR}` list for step 6. If tasks remain blocked (not
   `Done`, not merely waiting on a dependency that will clear next pass),
   surface it to the user and stop — a stuck task is a condition for a
   human.

4. **Validate phase (subagent).** Dispatch a fresh `general-purpose`
   subagent: invoke `Skill` with `skill: "krill-work:validate"`, args
   `<n>`, let it finish, then report back *only*:
   - **Clean pass:** the finalized `<plan-branches>` (branch → PR
     number/URL, all merged).
   - **Findings:** the new follow-up task ids `validate` had `planner`
     create.

5. **Loop control.** Clean pass → step 6. Findings → increment the
   iteration counter; if it has reached `--max-iterations`, stop and report
   the plan stuck, listing the outstanding findings and pointing to
   `/krill-work:implement <n>` to continue manually. Otherwise return to
   step 3 — the next `implement` picks up the new follow-up tasks.

6. **Final verification (subagent).** Dispatch one more fresh
   `general-purpose` subagent, never inline. Hand it *only* the
   `{task → branch → PR number/URL}` list from steps 3-4 as its worklist.
   Ask it to confirm independently:
   - every listed PR is `MERGED` (`gh pr view <branch-or-number> --json
     state,mergedAt`),
   - no PR belonging to this plan is still open,
   - `main` contains every task's work (e.g. `git log --oneline
     origin/main` grep per task).

   It reports back a short pass/fail plus any discrepancy — nothing else.

7. **Report.** Give the user the final merged PR list (numbers + URLs) and
   step 6's verdict. If step 6 found a discrepancy, surface it plainly
   rather than declaring the plan done.
