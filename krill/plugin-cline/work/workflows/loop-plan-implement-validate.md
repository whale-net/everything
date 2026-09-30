# loop-plan-implement-validate

*Drives a signed-off krill design (fork of project-manager's loop-plan-implement-validate) all the way to a set of merged PRs unattended — dispatches the `plan` workflow, the `implement` workflow, and the `validate` workflow each to their own fresh subagent, looping implement→validate again whenever validate routes findings back to Implementation, until validate reports a clean merge. Finishes with an independent final-verification subagent. Use for "run this plan end to end", "loop plan/implement/validate until done", or "get this whole plan merged without me babysitting each phase".*


Same loop-control and final-verification mechanics as
`tools/project-manager/workflows/loop-plan-implement-validate`, with
the `plan` workflow/`implement`/`validate` in place of the
`project-manager:*` workflow names, starting from a krill FeatureSet/
design-session id (once signed off). **On the Milestone path, `<n>`
throughout is the Milestone id plus the task manifest `plan` produced — not
a GitHub tracking issue.** Hand each phase's subagent the Milestone id and
the manifest's task ids and dependency edges only — never task bodies; the
subagent reads each task with `get_task {id}` (CONVENTIONS.md "Subagent
dispatch: ids, not bodies"). A subagent missing the ids can still recover
the task set itself with `list_tasks {milestone_id}` (CONVENTIONS.md "Work
axis"). On the no-Milestone fallback, `<n>` is still
the GitHub tracking issue the `krill-planner` custom mode mints.

On the Milestone path, every `worker`/`validator`/`system-validator`
dispatch inside `implement`/`validate` has a working task-lifecycle tool
surface — the persona gate that once rejected `claim_task`/`complete_task`
is fixed (#2930, #2933). A `forbidden` from one of those tools is a real
regression: report it, don't fall back to GitHub.

## Usage

```
the `loop-plan-implement-validate` workflow <feature-set-id> --milestone-id <milestone-id>   # Milestone path
the `loop-plan-implement-validate` workflow <feature-set-id>                                  # no-Milestone GitHub fallback
the `loop-plan-implement-validate` workflow <feature-set-id> --milestone-id <milestone-id> --max-subagents 2 --planner-model opus
the `loop-plan-implement-validate` workflow <feature-set-id> --milestone-id <milestone-id> --max-iterations 8
```

Parameters (`--max-subagents`, `--planner-model`, `--max-iterations`,
default 3) are unchanged from project-manager's.

## Steps

Identical to `tools/project-manager/workflows/loop-plan-implement-validate`'s
steps 1-7, with two substitutions:

1. **Confirm root state** by checking the design session (or FeatureSet
   slice) ended in `signoff`/`approved` rather than checking a
   `plan:approved` label — same idempotency check the `plan` workflow step 1
   already does.
2-7. Identical — dispatch fresh subagents with the appropriate `krill-*` custom mode invoking
   the `plan` workflow/the `implement` workflow/the `validate` workflow in place
   of the `project-manager:*` workflows, same loop-control and final-
   verification mechanics. Read the original for the full step text; it is
   not duplicated here since none of it changed by this fork.
