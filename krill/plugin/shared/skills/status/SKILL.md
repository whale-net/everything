---
name: status
description: Read-only status dashboard shared by krill-design and krill-work — for a design-session/product id, its revision-event timeline and open questions; for a Milestone plus its task manifest, per-task lane state via get_task. Use to check where a design or plan stands before deciding which orchestration skill to run next.
---

# status

Pure read — never appends revision events, never mutates a task, never
dispatches personas. This file is symlinked into both plugins' `skills/`
from `krill/plugin/shared/skills/status/` (see
`krill/plugin/shared/CONVENTIONS.md`).

## Usage

```
/status <design-session-id>
/status <milestone-id> [<task-manifest>] # manifest optional; list_tasks {milestone_id} re-derives it if omitted
```

## Steps

1. **Design-session id given**: call `get_design_session {id}`. Report:
   - The `opening_submission` and how many `revision_events` exist.
   - The last event's `event_type`. If it's `signoff` with
     `signoff_status: approved`, this design is fully approved for
     implementation — report the FeatureSet/Feature id(s) its `entity_deltas`
     touched and that `/krill-work:plan <feature-set-id> [--milestone-id
     <id>]` is next.
   - If the last event is `signoff` with `changes_requested`, report that
     producer/architect need another round.
   - Otherwise (`draft`/`answer`/`reconciliation`), call `list_open_questions
     {id, blocking: true}`. **Zero blocking open questions after at least one
     `reconciliation` event means architect has signed off** (architect
     signals this with an empty `opened` list on a `reconciliation` event,
     not a distinct event type — see `krill/plugin/design/agents/
     architect.md`) — report that `/krill-design:review <id>` is next. One or
     more blocking open questions means the producer/architect loop is still
     open — report the count and, since this is often the real answer to
     "why hasn't this signed off yet," name them.

2. **Milestone id given** — use the task manifest if you were handed one, or
   derive it yourself with `list_tasks {milestone_id}` (CONVENTIONS.md
   "Work axis"): call `get_task {id}` for every task id (both tools
   ungated, no session needed) and group by `current_lane`. Also call
   `get_milestone_status
   {milestone_id}` — this reflects `planner`/`plan`/`validate`'s
   actual writes.

3. Report a compact table for whichever applies: for a design session,
   revision-event count / last event type / open-question count; for a
   Milestone, Lane × task-id counts (Scaffold/Implementation/Testing/
   Validation/Done) straight from `get_task`.

4. If every task is `Done` with no open findings, report that
   `/krill-work:validate <milestone-id>` is available.
