---
name: plan
description: Task breakdown (krill-work fork) — converts a krill-design design session that ended in a signoff revision event (signoff_status approved) into a GitHub Project board with swimlanes and cohesive task issues, by dispatching the planner persona, which also creates real krill Task entities when a Milestone exists (M3/M4 FR1) and mints the bridging GitHub tracking issue (TODO(M4) for swimlane execution). Idempotent. Run after /krill-design:review (or /krill-design:loop-design-panel) approves the design, before /krill-work:implement. Also the right target for "just set up the board" / "create the tasks but don't start work" / "plan only".
---

# plan

Turns a signed-off krill design into executable, dependency-tracked task
issues on a Project board, by dispatching `krill-work:planner`. Pure task
breakdown — no code written, no branches touched. Forked from
`tools/project-manager/skills/plan`.

## Usage

```
/krill-work:plan <feature-set-id>
/krill-work:plan <feature-set-id> --milestone-id <milestone-id>   # krill-hosted product: also mints real Task entities
/krill-work:plan <feature-set-id> --planner-model sonnet
/krill-work:plan <tracking-issue-number>   # idempotency re-check on an already-planned FeatureSet/milestone
```

`--planner-model <model>` — same meaning as project-manager's, default `opus`.
`--milestone-id <id>` — pass when this FeatureSet's design was scoped to a
Milestone already authored in krill (`create_milestone`, M3 — only possible
for a product actually hosted in krill, e.g. krill's own domain or an
imported one). Without it, `planner` creates GitHub-only task issues, same
as before M3 existed.

## Steps

1. **Confirm and idempotency-check.** Given a FeatureSet id: call
   `get_feature_set_slice {id}` and confirm its design session's last event
   was `signoff` with `signoff_status: approved` (if you only have the
   design-session id, `get_design_session` gives you both). If not, point
   the user to `/krill-design:design`, `/krill-design:review`, or
   `/krill-design:loop-design-panel`. If `--milestone-id` was given, also
   call `get_milestone {id}` to confirm it exists and belongs to the same
   Product. Then check for an existing tracking issue citing this FeatureSet
   or Milestone id (**TODO(M3)** — `gh issue list --search "krill
   feature-set-id: <id>"` / `"krill milestone-id: <id>"`, since no
   `PointerArtifact` lookup exists for either yet). If found and it already
   has a `Project board: <url>` comment, the task breakdown has already
   run — report the existing project and task issues (grouped by swimlane,
   per `/status`) and stop.

   Given a tracking-issue number directly (from a prior `plan` run): same
   idempotency check via `gh issue view <n> --comments`.

2. **Task breakdown.** Otherwise, dispatch `krill-work:planner` — via
   `Agent` with `model` set to `--planner-model` (default `opus`) — with the
   FeatureSet id (and the Milestone id, if given), to add the Feature/
   Requirement entities to the milestone's `Delivers` set (Milestone path
   only), mint the tracking issue if needed, create the Project board with
   swimlanes, create cohesive task issues (calling `create_task` per task on
   the Milestone path — see `agents/planner.md` for the `PersonaSwarmOperator`
   restriction this requires), and post the summary comment.

   **If this FeatureSet is a milestone of a product brief not hosted in
   krill:** post `gh issue comment <product-issue> --body "Ledger: M<k> →
   in progress (Project board)"` on the tracking issue once the board
   exists. **If `--milestone-id` was given:** `planner` calls
   `set_milestone_status {milestone_id, status: "in progress"}` instead —
   this replaces the `Ledger:` comment for a krill-hosted milestone.

3. **Report.** Summarize the Project board URL, the tracking issue number
   (needed for `/krill-work:implement`/`validate`), and the created task
   issues by starting swimlane. Tell the user `/krill-work:implement <n>` is
   next.
