---
name: krill-stakeholder-meeting
description: Runs a stakeholder meeting round on a krill design — dispatches one stakeholder persona per persona named in the specification, collects guidance, non-blocking feedback, and numbered blockers, and records the round on the DesignSession. Blockers route the design back through the producer/architect loop. Use after architect sign-off, or against a design that already received a signoff revision event.
---

# krill-stakeholder-meeting

Convenes every persona named in a design's specification for one round of
feedback. krill has no meeting entity, so a round is recorded on the
`DesignSession` itself as open questions (`SM-`/`SB-`/`SF-` ids — see
`references/CONVENTIONS.md` § "Stakeholder meeting records").

Callable directly, or automatically by `/krill-design --stakeholder-meeting`.

## Usage

```
/krill-stakeholder-meeting <design-session-id>
/krill-stakeholder-meeting <target> --personas "Operator,Release engineer"
/krill-stakeholder-meeting <target> --add-persona "On-call SRE"
```

## Subagent setup

Call `get_skill_resources` for `["references/CONVENTIONS.md", "references/agents/stakeholder.md", "references/agents/producer.md", "references/agents/architect.md"]`.
Register with `define_subagent` as needed:
- `krill-stakeholder`: `system_prompt` from `references/agents/stakeholder.md`, `enable_mcp_tools: true`, `enable_write_tools: false`.
- `krill-producer`: `system_prompt` from `references/agents/producer.md`, `enable_mcp_tools: true`, `enable_write_tools: true`.
- `krill-architect`: `system_prompt` from `references/agents/architect.md`, `enable_mcp_tools: true`, `enable_write_tools: false`.

## Steps

1. **Resolve the target and read the design.** Call `get_design_session {id}`
   and `list_open_questions {id, blocking: true}`. If the last
   `reconciliation` event still has open blocking questions, say so and ask
   the user whether to hold the meeting anyway. Read the spec from
   `get_design_session_slice {id}` — authoritative.

2. **Determine the round number.** Count the `SM-<N>` question ids opened in
   `get_design_session`'s events; this meeting is round `N+1`.

3. **Enumerate the personas.** Take the personas the spec names
   (`list_personas {product_id}` for the design's product, plus any named in
   prior `draft` event notes), apply `--personas`/`--add-persona`, deduplicate.
   Stop if none are named.

4. **Collect feedback.** Dispatch `krill-stakeholder` **in parallel — one
   entry per persona in a single `invoke_subagent` call** (`Model: "inherit"`,
   `Workspace: "inherit"`). Each gets: the persona name, the design-session id,
   and the round number. Each returns its Guidance / Feedback / Blockers
   sections as its dispatch response; it writes nothing to krill.

5. **Record the round.** Deduplicate blockers across personas, then append one
   `reconciliation` event (`verified_against: "main@<sha>"` from `git rev-parse
   origin/main`, `entity_deltas: []`) whose `open_questions_delta` follows
   `CONVENTIONS.md` § "Stakeholder meeting records": the `SM-<N>` marker
   (opened and resolved in the same event, text = attendees plus `cleared` or
   `blocked (<k> blockers)`), one blocking `SB-<N>.<n>` per consolidated
   blocker, one non-blocking `SF-<N>.<n>` per guidance/feedback item. Present
   the same minutes to the user: a persona/blockers/summary table, the `SB-`
   blockers, grouped non-blocking guidance/feedback, and a terminal
   `Stakeholder meeting: blocked (<k> blockers)` / `Stakeholder meeting: cleared`
   line.

6. **Route the outcome.**
   - **Cleared** — report to the user. If invoked from `/krill-design`, control
     returns there for hand-off to `/krill-review`.
   - **Blocked, session not yet signed off** — dispatch `krill-producer`
     (Mode 2) with the design-session id and round number to append an `answer`
     event resolving each `SB-<N>.<n>`, then dispatch `krill-architect` for a
     fresh `reconciliation`. Once clear, re-run this skill for round `N+1`. Cap
     at 3 meeting rounds; if blockers persist, stop and summarize for the user.
   - **Blocked, design already signed off** — report the blockers and confirm
     before proceeding; on confirmation, run the same producer/architect loop
     on the same design session.

7. **Do not create tasks.** A blocker changes the design; it does not become a
   task.
