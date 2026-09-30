# status

*Read-only status dashboard shared by krill-design and krill-work — for a design-session/product id, its revision-event timeline and open questions; for a Milestone plus its task manifest, per-task lane state via get_task (krill-native, no GitHub); for a no-Milestone GitHub tracking issue, the Project swimlane breakdown. Use to check where a design or plan stands before deciding which orchestration workflow to run next.*


Pure read — never appends revision events, never mutates a task, never
dispatches personas. This file is symlinked into both plugins' `workflows/`
from `krill/plugin-cline/{design,work}/workflows/status.md` (see
`krill/plugin-cline/shared/CONVENTIONS.md`).

## Usage

```
/status <design-session-id>
/status <milestone-id> [<task-manifest>] # Milestone path — manifest optional; list_tasks {milestone_id} re-derives it if omitted
/status <tracking-issue-number>          # no-Milestone GitHub fallback
```

## Steps

1. **Design-session id given**: call `get_design_session {id}`. Report:
   - The `opening_submission` and how many `revision_events` exist.
   - The last event's `event_type`. If it's `signoff` with
     `signoff_status: approved`, this design is fully approved for
     implementation — report the FeatureSet/Feature id(s) its `entity_deltas`
     touched and that the `plan` workflow <feature-set-id> [--milestone-id
     <id>]` is next.
   - If the last event is `signoff` with `changes_requested`, report that
     producer/architect need another round.
   - Otherwise (`draft`/`answer`/`reconciliation`), call `list_open_questions
     {id, blocking: true}`. **Zero blocking open questions after at least one
     `reconciliation` event means architect has signed off** (architect
     signals this with an empty `opened` list on a `reconciliation` event,
     not a distinct event type — see `krill/plugin-cline/design/agents/
     architect.md`) — report that the `review` workflow <id>` is next. One or
     more blocking open questions means the producer/architect loop is still
     open — report the count and, since this is often the real answer to
     "why hasn't this signed off yet," name them.

2. **Milestone path** — use the task manifest if you were handed one, or
   derive it yourself with `list_tasks {milestone_id}` (CONVENTIONS.md
   "Work axis"): call `get_task {id}` for every task id (both tools
   ungated, no session needed) and group by `current_lane`. Also call
   `get_milestone_status
   {milestone_id}` — `set_milestone_status` works from an ordinary Cline session today, so this reflects `planner`/`plan`/`validate`'s
   actual writes.

3. **GitHub tracking-issue number given** (no-Milestone fallback):
   `gh issue view <n> --comments` for the title and the `Project board:
   <url>` comment (without one, report that task breakdown hasn't started
   and the `plan` workflow is next), then list every Project item scoped to
   `Part of #<n>`:
   ```sh
   gh project item-list <project-number> --owner whale-net --field "Status" --format json \
     | jq '[.items[] | select(.content.body | test("Part of #<n>([^0-9]|$)"))]'
   ```
   Group by `Status` (`Scaffold`/`Implementation`/`Testing`/`Validation`/
   `Done`/`Noted`/`Carry-over`/`Deferred`). For each item not yet `Done`,
   check its `assignees` (claimed vs. unclaimed) and whether its `Depends
   on:` issues are closed (ready vs. blocked), batching every dependency
   across every item into one aliased `gh api graphql` call rather than one
   lookup per dependency (CONVENTIONS.md § "No-Milestone GitHub fallback").

4. Report a compact table for whichever applies: for a design session,
   revision-event count / last event type / open-question count; for a
   Milestone, Lane × task-id counts (Scaffold/Implementation/Testing/
   Validation/Done) straight from `get_task`; for a tracking issue,
   Swimlane × (Blocked / Ready / Claimed / Done) counts.

5. If every task is `Done` with no open findings, report that
   the `validate` workflow <milestone-id|n>` is available.
