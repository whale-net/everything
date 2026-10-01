---
name: plan
description: Task breakdown — converts a krill-design design session that ended in a signoff revision event (signoff_status approved) into real krill Task entities under a krill Milestone, by dispatching the planner persona. Requires a Milestone id; with none, it stops and says to cut the milestone first. Idempotent. Run after /krill-design:review (or /krill-design:loop-design-panel) approves the design, before /krill-work:implement. Also the right target for "just create the tasks, don't start work" / "plan only".
---

# plan

Turns a signed-off krill design into krill `Task` entities, by dispatching
`krill-work:planner`. Pure task breakdown — no code written, no branches
touched.

## Usage

```
/krill-work:plan <feature-set-id> --milestone-id <milestone-id>
/krill-work:plan <feature-set-id> --milestone-id <milestone-id> --planner-model sonnet
```

`--milestone-id <id>` — **required.** The krill Milestone (or milepebble)
the tasks belong to; `create_task` accepts nothing else. If you have none,
stop and report the "Milestone required" hard stop in
`krill/plugin/shared/CONVENTIONS.md`: cut the milestone first via
`/krill-design:product`/`/krill-design:design`. Never proceed without it.
`--planner-model <model>` — model override passed to the `Agent` call that
dispatches `planner`, default `opus` (task breakdown is the highest-leverage
reasoning step in the pipeline; running it as a subagent also keeps the
Task-creation traffic out of this skill's context).

## Steps

The unit of planning is the **Milestone**, not the FeatureSet. A
milestone's delivered scope routinely spans several FeatureSets — krill's
own are named `Now`/`Next`/`Later`, which is a naming convention, not a
second delivery parent — so nothing below requires a separate run per
FeatureSet. `planner` records the whole scope with one `add_delivers`
call, and that call refuses an entity another milestone of the same
product already delivers: re-cut it with `move_delivery_scope` rather
than opening a second owner.

1. **Confirm and idempotency-check.** If no `--milestone-id` was given,
   stop here with the hard stop above. Call `get_feature_set_slice {id}` and
   confirm its design session's last event was `signoff` with
   `signoff_status: approved` (if you only have the design-session id,
   `get_design_session` gives you both). If not, point the user to
   `/krill-design:design`, `/krill-design:review`, or
   `/krill-design:loop-design-panel`. Call `get_milestone {id}` to confirm
   the Milestone exists and belongs to the same Product. For idempotency,
   don't trust bare `get_milestone_status {id}` alone: `planned` is
   ambiguous — `/krill-design:design`/`review`/
   `loop-design-panel` all set a krill-hosted milestone to `planned` the
   moment its design signs off, before any task exists, and `planner` sets
   the same status again once it actually creates tasks (there is no
   separate status value for the two). Call
   `get_milestone_status_history {id}` and read the note on the latest
   `planned`-or-later transition instead — `planner` always names the
   created task ids in that note (`agents/planner.md` step 4); a note that
   names task ids means a prior `plan` run already created them: re-derive
   its manifest with `list_tasks {milestone_id}` (CONVENTIONS.md "Work
   axis") rather than re-running `planner`. A
   note that names a design-session/signoff event instead (no task ids)
   means this is genuinely the first planning pass — proceed. If the note
   is ambiguous, ask the user to confirm before dispatching `planner`.
2. **Task breakdown.** Dispatch `krill-work:planner` — via `Agent` with
   `model` set to `--planner-model` (default `opus`) — with the FeatureSet
   id and Milestone id. `planner` adds the Feature/Requirement entities to
   the milestone's `Delivers` set, creates krill Tasks with
   `create_task`/`declare_task_dependencies`, and sets the milestone's
   status — every one of these works from this dispatch today (see
   `agents/planner.md`) — and returns the task manifest.
3. **Report.** Relay `planner`'s full task manifest (every task id, title,
   starting lane, dependency edges) to the user verbatim — **this is the
   only durable record of what was just created**; nothing else can
   reconstruct it. Tell the user `/krill-work:implement <milestone-id>` is
   next, and that it needs this exact manifest.
