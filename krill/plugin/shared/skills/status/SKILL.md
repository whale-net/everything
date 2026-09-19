---
name: status
description: Read-only status dashboard shared by krill-design and krill-work — for a design-session/product id, its revision-event timeline and open questions; for a signed-off design's tracking issue, the GitHub Project swimlane breakdown (TODO(M4) — will become a krill work-tracking query once that surface exists). Use to check where a design or plan stands before deciding which orchestration skill to run next.
---

# status

Pure read — never appends revision events, never edits Project items, never
dispatches personas. This file is symlinked into both plugins' `skills/` from
`krill/plugin/shared/skills/status/` (see
`krill/plugin/shared/CONVENTIONS.md`).

## Usage

```
/status <design-session-id>
/status <tracking-issue-number>
```

## Steps

1. **Design-session id given**: call `get_design_session {id}`. Report:
   - The `opening_submission` and how many `revision_events` exist.
   - The last event's `event_type`. If it's `signoff` with
     `signoff_status: approved`, this design is fully approved for
     implementation — report the FeatureSet/Feature id(s) its `entity_deltas`
     touched and that `/krill-work:plan <feature-set-id>` is next (TODO(M3)
     note: today `plan` still creates a GitHub tracking issue citing that id,
     since no `PointerArtifact` MCP write path exists yet).
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

2. **GitHub tracking-issue number given** (a `krill-work:plan`-created issue,
   or a legacy `project-manager` `plan:approved` issue): same as
   `project-manager`'s `status` skill from here — `gh issue view <n>
   --comments`, then list every Project item and group by swimlane
   (`Scaffold`/`Implementation`/`Testing`/`Validation`/`Done`/`Noted`/
   `Carry-over`/`Deferred`), batch-checking `Depends on:` closure in one
   `gh api graphql` call rather than one lookup per dependency. See
   `tools/project-manager/CONVENTIONS.md` §§ "Worker lifecycle", "Task issues
   & swimlane progression" for the exact queries — this fork doesn't repeat
   them, and **TODO(M4)**: this whole step becomes a krill work-tracking MCP
   query once that surface ships, replacing the `gh`/Project calls entirely.

3. Report a compact table for whichever of the two applies: for a design
   session, revision-event count / last event type / open-question count; for
   a tracking issue, Swimlane × (Blocked / Ready / Claimed / Done) counts.

4. If a tracking issue's items are all `Done` with no open `Validation`
   findings, report that `/krill-work:validate <n>` is available.
