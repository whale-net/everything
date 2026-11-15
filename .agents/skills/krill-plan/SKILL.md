---
name: krill-plan
description: Task breakdown — converts a krill-design design session that ended in a signoff revision event (signoff_status approved) into real krill Task entities under a krill Milestone, by dispatching the planner persona. Requires a Milestone id; with none, it stops and says to cut the milestone first. Idempotent. Run after /krill-review (or /krill-loop-design-panel) approves the design, before /krill-implement. Also the right target for "just create the tasks, don't start work" / "plan only".
---

# krill-plan

Turns a signed-off krill design into krill `Task` entities, by dispatching
`krill-planner`. Pure task breakdown — no code written, no branches touched.

## Usage

```
/krill-plan <feature-set-id> --milestone-id <milestone-id>
```

`--milestone-id <id>` — **required.** The krill Milestone (or milepebble)
the tasks belong to; `create_task` accepts nothing else. If you have none,
stop and report the "Milestone required" hard stop in
`references/CONVENTIONS.md`: cut the milestone first via
`/krill-product`/`/krill-design`. Never proceed without it.

## Subagent setup

Call `get_skill_resources` for `["references/CONVENTIONS.md", "references/agents/planner.md"]`.
Define `krill-planner` once per conversation via `define_subagent`:
- `name`: `"krill-planner"`
- `system_prompt`: contents of `references/agents/planner.md`
- `enable_mcp_tools`: `true`
- `enable_write_tools`: `false`

## Steps

The unit of planning is the **Milestone**, not the FeatureSet. A milestone's
delivered scope routinely spans several FeatureSets, so `planner` records the
whole scope with one `add_delivers` call, and that call refuses an entity
another milestone of the same product already delivers: re-cut it with
`move_delivery_scope` rather than opening a second owner.

1. **Confirm and idempotency-check.** If no `--milestone-id` was given, stop
   here with the hard stop above. Call `get_feature_set_slice {id}` and confirm
   its design session's last event was `signoff` with `signoff_status: approved`
   (if you only have the design-session id, `get_design_session` gives you
   both). If not, point the user to `/krill-design`, `/krill-review`, or
   `/krill-loop-design-panel`. Call `get_milestone {id}` to confirm the
   Milestone exists and belongs to the same Product. Run `CONVENTIONS.md`
   "Milestone given: check its milepebbles first" — a milepebble's tasks never
   appear under its parent — and pass `krill-planner` only uncovered scope plus
   the milepebble id(s) it should target. For idempotency, call
   `get_milestone_status_history {id}` and read the note on the latest
   `planned`-or-later transition: a note that names task ids means a prior
   `plan` run already created them (re-derive its manifest with `list_tasks
   {milestone_id}` rather than re-running `krill-planner`). A note that names a
   design-session/signoff event instead means this is the first planning pass —
   proceed. If ambiguous, ask the user before dispatching `krill-planner`.
2. **Task breakdown.** Dispatch `krill-planner` via `invoke_subagent`
   (`Model: "inherit"`, `Workspace: "inherit"`) with the FeatureSet id and
   Milestone id. `krill-planner` adds the Feature/Requirement entities to the
   milestone's `Delivers` set, creates krill Tasks with
   `create_task`/`declare_task_dependencies`, sets the milestone's status, and
   returns the task manifest.
3. **Report.** Relay `krill-planner`'s full task manifest (every task id,
   title, starting lane, dependency edges) to the user verbatim — **this is
   the only durable record of what was just created**. Tell the user
   `/krill-implement <milestone-id>` is next.
