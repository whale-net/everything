---
name: plan
description: Task breakdown (krill-work fork) — converts a krill-design design session that ended in a signoff revision event (signoff_status approved) into a GitHub Project board with swimlanes and cohesive task issues, by dispatching the planner persona, which also mints the bridging GitHub tracking issue (TODO(M3)). Idempotent. Run after /krill-design:review (or /krill-design:loop-design-panel) approves the design, before /krill-work:implement. Also the right target for "just set up the board" / "create the tasks but don't start work" / "plan only".
---

# plan

Turns a signed-off krill design into executable, dependency-tracked task
issues on a Project board, by dispatching `krill-work:planner`. Pure task
breakdown — no code written, no branches touched. Forked from
`tools/project-manager/skills/plan`.

## Usage

```
/krill-work:plan <feature-set-id>
/krill-work:plan <feature-set-id> --planner-model sonnet
/krill-work:plan <tracking-issue-number>   # idempotency re-check on an already-planned FeatureSet
```

`--planner-model <model>` — same meaning as project-manager's, default `opus`.

## Steps

1. **Confirm and idempotency-check.** Given a FeatureSet id: call
   `get_feature_set_slice {id}` and confirm its design session's last event
   was `signoff` with `signoff_status: approved` (if you only have the
   design-session id, `get_design_session` gives you both). If not, point
   the user to `/krill-design:design`, `/krill-design:review`, or
   `/krill-design:loop-design-panel`. Then check for an existing tracking
   issue citing this FeatureSet id (**TODO(M3)** — `gh issue list --search
   "krill feature-set-id: <id>"`, since no `PointerArtifact` lookup exists
   yet). If found and it already has a `Project board: <url>` comment, the
   task breakdown has already run — report the existing project and task
   issues (grouped by swimlane, per `/status`) and stop.

   Given a tracking-issue number directly (from a prior `plan` run): same
   idempotency check via `gh issue view <n> --comments`.

2. **Task breakdown.** Otherwise, dispatch `krill-work:planner` — via
   `Agent` with `model` set to `--planner-model` (default `opus`) — with the
   FeatureSet id, to mint the tracking issue if needed, create the Project
   board with swimlanes, create cohesive task issues, and post the summary
   comment.

   **If this FeatureSet is a milestone of a product brief:** post
   `gh issue comment <product-issue> --body "Ledger: M<k> → in progress
   (Project board)"` on the tracking issue once the board exists.

3. **Report.** Summarize the Project board URL, the tracking issue number
   (needed for `/krill-work:implement`/`validate`), and the created task
   issues by starting swimlane. Tell the user `/krill-work:implement <n>` is
   next.
