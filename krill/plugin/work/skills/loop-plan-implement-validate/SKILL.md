---
name: loop-plan-implement-validate
description: Drives a signed-off krill design (fork of project-manager's loop-plan-implement-validate) all the way to a set of merged PRs unattended — dispatches /krill-work:plan, /krill-work:implement, and /krill-work:validate each to their own fresh subagent, looping implement→validate again whenever validate routes findings back to Implementation, until validate reports a clean merge. Finishes with an independent final-verification subagent. Use for "run this plan end to end", "loop plan/implement/validate until done", or "get this whole plan merged without me babysitting each phase".
---

# loop-plan-implement-validate

Same loop-control and final-verification mechanics as
`tools/project-manager/skills/loop-plan-implement-validate`, with
`krill-work:plan`/`implement`/`validate` in place of the
`project-manager:*` skill names, starting from a krill FeatureSet/
design-session id (once signed off). **On the Milestone path, `<n>`
throughout is the Milestone id plus the task manifest `plan` produced — not
a GitHub tracking issue.** Hand each phase's subagent the Milestone id and
the manifest's task ids and dependency edges only — never task bodies; the
subagent reads each task with `get_task {id}` (CONVENTIONS.md "Subagent
dispatch: ids, not bodies"). A subagent missing the ids can still recover
the task set itself with `list_tasks {milestone_id}` (CONVENTIONS.md "Work
axis"). On the no-Milestone fallback, `<n>` is still
the GitHub tracking issue `krill-work:planner` mints.

On the Milestone path, every `worker`/`validator`/`system-validator`
dispatch inside `implement`/`validate` has a working task-lifecycle tool
surface — the persona gate that once rejected `claim_task`/`complete_task`
is fixed (#2930, #2933). A `forbidden` from one of those tools is a real
regression: report it, don't fall back to GitHub.

## Usage

```
/krill-work:loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id>   # Milestone path
/krill-work:loop-plan-implement-validate <feature-set-id>                                  # no-Milestone GitHub fallback
/krill-work:loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id> --max-subagents 2 --planner-model opus
/krill-work:loop-plan-implement-validate <feature-set-id> --milestone-id <milestone-id> --max-iterations 8
```

Parameters (`--max-subagents`, `--planner-model`, `--max-iterations`,
default 3) are unchanged from project-manager's.

## Steps

Identical to `tools/project-manager/skills/loop-plan-implement-validate`'s
steps 1-7, with two substitutions:

1. **Confirm root state** by checking the design session (or FeatureSet
   slice) ended in `signoff`/`approved` rather than checking a
   `plan:approved` label — same idempotency check `krill-work:plan` step 1
   already does.
2-7. Identical — dispatch fresh `general-purpose` subagents invoking
   `krill-work:plan`/`krill-work:implement`/`krill-work:validate` in place
   of the `project-manager:*` skills, same loop-control and final-
   verification mechanics. Read the original for the full step text; it is
   not duplicated here since none of it changed by this fork.
