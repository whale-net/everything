---
name: plan
description: Task breakdown — converts a krill-design design session that ended in a signoff revision event (signoff_status approved) into real krill Task entities when a Milestone exists (M3/M4), by dispatching the planner persona; no GitHub tracking issue or Project board on that path. Falls back to a GitHub tracking issue and Project board only when no krill Milestone scopes the work. Idempotent. Run after /krill-design:review (or /krill-design:loop-design-panel) approves the design, before /krill-work:implement. Also the right target for "just create the tasks, don't start work" / "plan only".
---

# plan

Turns a signed-off krill design into krill `Task` entities, by dispatching
`krill-work:planner`. Pure task breakdown — no code written, no branches
touched.

## Usage

```
/krill-work:plan <feature-set-id> --milestone-id <milestone-id>   # krill-hosted product: mints real Task entities, no GitHub
/krill-work:plan <feature-set-id>                                 # no krill Milestone: GitHub Project fallback
/krill-work:plan <feature-set-id> --milestone-id <milestone-id> --planner-model sonnet
```

`--planner-model <model>` — model override passed to the `Agent` call that
dispatches `planner`, default `opus` (task breakdown is the highest-leverage
reasoning step in the pipeline; running it as a subagent also keeps the
Task/issue-creation traffic out of this skill's context).
`--milestone-id <id>` — pass when this FeatureSet's design was scoped to a
Milestone already authored in krill (`create_milestone`, M3 — only possible
for a product actually hosted in krill, e.g. krill's own domain or an
imported one). Without it, `planner` falls back to a GitHub tracking issue and Project
board entirely (CONVENTIONS.md "No-Milestone GitHub fallback") — this is a
real krill capability gap (no Task container exists outside a
Milestone), not a default worth avoiding when it doesn't apply.

## Steps (Milestone path)

The unit of planning is the **Milestone**, not the FeatureSet. A
milestone's delivered scope routinely spans several FeatureSets — krill's
own are named `Now`/`Next`/`Later`, which is a naming convention, not a
second delivery parent — so nothing below requires a separate run per
FeatureSet. `planner` records the whole scope with one `add_delivers`
call, and that call refuses an entity another milestone of the same
product already delivers: re-cut it with `move_delivery_scope` rather
than opening a second owner.

1. **Confirm and idempotency-check.** Call `get_feature_set_slice {id}` and
   confirm its design session's last event was `signoff` with
   `signoff_status: approved` (if you only have the design-session id,
   `get_design_session` gives you both). If not, point the user to
   `/krill-design:design`, `/krill-design:review`, or
   `/krill-design:loop-design-panel`. Call `get_milestone {id}` to confirm
   the Milestone exists and belongs to the same Product. For idempotency,
   don't trust bare `get_milestone_status {id}` alone: `planned` is
   ambiguous on this path — `/krill-design:design`/`review`/
   `loop-design-panel` all set a krill-hosted milestone to `planned` the
   moment its design signs off, before any task exists, and `planner` sets
   the same status again once it actually creates tasks (there is no
   separate status value for the two). Call
   `get_milestone_status_history {id}` and read the note on the latest
   `planned`-or-later transition instead — `planner` always names the
   created task ids in that note (`agents/planner.md` step 4); a note that
   names task ids means a prior `plan` run already created them, ask the
   user for that run's task manifest (there is no krill query to
   reconstruct it — CONVENTIONS.md) rather than re-running `planner`. A
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

## Steps (no-Milestone GitHub fallback)

1. **Idempotency check.** `gh issue list --search "krill feature-set-id:
   <id>"`; if a tracking issue exists, `gh issue view <n> --comments` and
   check for a `Project board: <url>` comment — if present, the breakdown
   already ran: report the existing project number and its task issues
   grouped by swimlane (`/krill-work:status <n>`) and stop.
2. **Confirm the design** signed off (as in step 1 above) and dispatch
   `krill-work:planner` with the FeatureSet id and no Milestone id, with
   `model` set to `--planner-model`. It mints a tracking issue citing
   `krill feature-set-id: <id>`, sets up the Project board, and creates
   the task issues.
3. **Report** the Project board URL and created task issues grouped by
   starting swimlane, and point to `/krill-work:implement <n>`.
