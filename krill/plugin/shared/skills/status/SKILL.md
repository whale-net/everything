---
name: status
description: Read-only status dashboard shared by krill-design and krill-work — for a design-session/product id, its revision-event timeline and open questions; for a Milestone plus its task manifest, per-task lane state via get_task (krill-native, no GitHub); for a no-Milestone GitHub tracking issue, the Project swimlane breakdown. Use to check where a design or plan stands before deciding which orchestration skill to run next.
---

# status

Pure read — never appends revision events, never mutates a task, never
dispatches personas. This file is symlinked into both plugins' `skills/`
from `krill/plugin/shared/skills/status/` (see
`krill/plugin/shared/CONVENTIONS.md`).

## Usage

```
/status <design-session-id>
/status <milestone-id> <task-manifest>   # Milestone path — the manifest plan/implement returned; no krill query re-derives it
/status <tracking-issue-number>          # no-Milestone GitHub fallback
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

2. **Milestone id plus a task manifest given** (Milestone path — there is no
   krill query that lists a milestone's tasks, so you must be handed the
   manifest; CONVENTIONS.md "No task-discovery query exists"): call
   `get_task {id}` for every task id in the manifest (ungated, no session
   needed) and group by `current_lane`. Also call `get_milestone_status
   {milestone_id}` — `set_milestone_status` works from an ordinary Claude
   Code session today (whale-net/everything#2928), so this reflects
   `planner`/`plan`/`validate`'s actual writes.

3. **GitHub tracking-issue number given** (no-Milestone fallback, or a
   legacy `project-manager` `plan:approved` issue): same as
   `project-manager`'s `status` skill — `gh issue view <n> --comments`, then
   list every Project item and group by swimlane
   (`Scaffold`/`Implementation`/`Testing`/`Validation`/`Done`/`Noted`/
   `Carry-over`/`Deferred`), batch-checking `Depends on:` closure in one
   `gh api graphql` call rather than one lookup per dependency. See
   `tools/project-manager/CONVENTIONS.md` §§ "Worker lifecycle", "Task issues
   & swimlane progression" for the exact queries — this fork doesn't repeat
   them.

4. Report a compact table for whichever applies: for a design session,
   revision-event count / last event type / open-question count; for a
   Milestone, Lane × task-id counts (Scaffold/Implementation/Testing/
   Validation/Done) straight from `get_task`; for a tracking issue,
   Swimlane × (Blocked / Ready / Claimed / Done) counts.

5. If every task is `Done` with no open findings, report that
   `/krill-work:validate <milestone-id|n>` is available.
