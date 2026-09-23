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
a GitHub tracking issue** (CONVENTIONS.md "No task-discovery query exists");
each phase's subagent must be handed the manifest explicitly since nothing
can re-derive it. On the no-Milestone fallback, `<n>` is still the GitHub
tracking issue `krill-work:planner` mints.

On the Milestone path, every `worker`/`validator`/`system-validator`
dispatch inside `implement`/`validate` is expected to hit the known
task-lifecycle blocker and report `forbidden` — see `agents/worker.md`.
This loop does not retry around that failure or fall back to GitHub; it
surfaces the failure in its final report like any other blocker.

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
