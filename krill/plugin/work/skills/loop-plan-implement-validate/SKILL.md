---
name: loop-plan-implement-validate
description: Drives a signed-off krill design (fork of project-manager's loop-plan-implement-validate) all the way to a set of merged PRs unattended — dispatches /krill-work:plan, /krill-work:implement, and /krill-work:validate each to their own fresh subagent, looping implement→validate again whenever validate routes findings back to Implementation, until validate reports a clean merge. Finishes with an independent final-verification subagent. Use for "run this plan end to end", "loop plan/implement/validate until done", or "get this whole plan merged without me babysitting each phase".
---

# loop-plan-implement-validate

Forked verbatim from `tools/project-manager/skills/loop-plan-implement-validate`
— mechanics unchanged, with `krill-work:plan`/`implement`/`validate` in
place of the `project-manager:*` skill names, and starting from a krill
FeatureSet/design-session id (once signed off) rather than a `plan:approved`
root Issue number. `<n>` throughout is the GitHub tracking issue
`krill-work:planner` mints — see `agents/planner.md` — not a krill entity.

## Usage

```
/krill-work:loop-plan-implement-validate <feature-set-id-or-tracking-issue>
/krill-work:loop-plan-implement-validate 123 --max-subagents 2 --planner-model opus
/krill-work:loop-plan-implement-validate 123 --max-iterations 8
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
